package exitmgr

import (
	"fmt"
	"sync"
	"time"

	"github.com/flynn/noise"

	"openflux/internal/transport"
	"openflux/internal/transport/cupsonline"
	"openflux/internal/tunnel"
)

// ClientStatus is a point-in-time snapshot of one client's state, safe to
// serialize and hand back over the panel's HTTP API.
type ClientStatus struct {
	Config        ClientConfig  `json:"config"`
	Status        string        `json:"status"` // "running" | "error"
	Error         string        `json:"error,omitempty"`
	StartedAt     time.Time     `json:"started_at,omitempty"`
	Stats         *tunnel.Stats `json:"stats,omitempty"`
	BytesSent     uint64        `json:"bytes_sent,omitempty"`
	BytesReceived uint64        `json:"bytes_received,omitempty"`
	// CupsonlineRooms is the base64 room list a cupsonline client needs as
	// its --url (unlike every other transport, the panel's own cfg.URL is
	// ignored for cupsonline exit clients -- the exit generates the rooms
	// itself). Empty for every other transport, and empty until the
	// client's transport has actually started.
	CupsonlineRooms string `json:"cupsonline_rooms,omitempty"`
}

type runningClient struct {
	cfg       ClientConfig
	trans     transport.Transport
	tun       *tunnel.TCPTunnel
	status    string
	lastErr   error
	startedAt time.Time
}

// Manager owns a set of concurrently running exit-node clients, each with
// its own transport and its own independent L4 tunnel (each gets its own
// gVisor stack instance, so clients never share network-stack state).
type Manager struct {
	mu      sync.RWMutex
	clients map[string]*runningClient
	base    transport.TransportConfig
	store   *Store

	// buildTransport constructs a client's transport stack. Overridable
	// (see NewManagerWithBuilder) so tests can exercise Manager's lifecycle
	// and concurrency logic with an in-memory stub instead of a real
	// network transport. The L4 tunnel itself (tunnel.NewTCPTunnelMode) is
	// always the real thing -- it only sets up an in-memory gVisor stack,
	// no network I/O, so it's already safe to use with a stub transport.
	buildTransport func(ClientConfig, transport.TransportConfig) (transport.Transport, error)
}

// NewManager creates an empty Manager. store may be nil to disable
// persistence (clients then only live for the process's lifetime).
// staticKey is the panel's own Noise identity, shared by every client's
// encrypted transport (see BuildTransport).
func NewManager(store *Store, staticKey noise.DHKey) *Manager {
	return NewManagerWithBuilder(store, func(cfg ClientConfig, base transport.TransportConfig) (transport.Transport, error) {
		return BuildTransport(cfg, base, staticKey)
	})
}

// NewManagerWithBuilder is NewManager with transport construction injected,
// for tests.
func NewManagerWithBuilder(
	store *Store,
	buildTransport func(ClientConfig, transport.TransportConfig) (transport.Transport, error),
) *Manager {
	return &Manager{
		clients:        make(map[string]*runningClient),
		base:           transport.DefaultConfig(),
		store:          store,
		buildTransport: buildTransport,
	}
}

// LoadPersisted starts every client found in the Manager's store, if one is
// configured. Errors starting an individual client are recorded on that
// client's status rather than aborting the rest.
func (m *Manager) LoadPersisted() error {
	if m.store == nil {
		return nil
	}
	cfgs, err := m.store.Load()
	if err != nil {
		return fmt.Errorf("load persisted clients: %w", err)
	}
	for _, cfg := range cfgs {
		if err := m.start(cfg); err != nil {
			// Keep it registered (as errored) rather than silently dropping
			// it, so the operator sees it in the panel and can fix/retry.
			m.mu.Lock()
			m.clients[cfg.ID] = &runningClient{cfg: cfg, status: "error", lastErr: err}
			m.mu.Unlock()
		}
	}
	return nil
}

// AddClient validates cfg, starts its transport and tunnel, and persists it
// (if a store is configured) so it survives a process restart.
func (m *Manager) AddClient(cfg ClientConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	m.mu.RLock()
	_, exists := m.clients[cfg.ID]
	m.mu.RUnlock()
	if exists {
		return fmt.Errorf("client %q already exists", cfg.ID)
	}

	startErr := m.start(cfg)
	if startErr != nil {
		m.mu.Lock()
		m.clients[cfg.ID] = &runningClient{cfg: cfg, status: "error", lastErr: startErr}
		m.mu.Unlock()
	}

	if m.store != nil {
		if err := m.store.Save(m.configs()); err != nil {
			return fmt.Errorf("client registered but failed to persist: %w", err)
		}
	}
	return startErr
}

// start builds the transport + tunnel for cfg and, on success, registers
// the running client. It does not touch persistence.
func (m *Manager) start(cfg ClientConfig) error {
	trans, err := m.buildTransport(cfg, m.base)
	if err != nil {
		return fmt.Errorf("build transport: %w", err)
	}
	if err := trans.Start(); err != nil {
		return fmt.Errorf("start transport: %w", err)
	}
	tun, err := tunnel.NewTCPTunnelMode(trans, true, tunnel.ExitModeL4)
	if err != nil {
		trans.Stop()
		return fmt.Errorf("start tunnel: %w", err)
	}

	m.mu.Lock()
	m.clients[cfg.ID] = &runningClient{
		cfg:       cfg,
		trans:     trans,
		tun:       tun,
		status:    "running",
		startedAt: time.Now(),
	}
	m.mu.Unlock()
	return nil
}

// UpdateClient replaces a registered client's config: stops its current
// transport/tunnel and starts a new one from cfg, keeping the same id. On a
// start failure the client is left registered in an "error" state (as
// AddClient does) rather than reverting to the old config, so the operator
// sees the failure and can fix it instead of the edit silently no-op'ing.
func (m *Manager) UpdateClient(id string, cfg ClientConfig) error {
	cfg.ID = id
	if err := cfg.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	old, exists := m.clients[id]
	m.mu.Unlock()
	if !exists {
		return fmt.Errorf("client %q not found", id)
	}
	if old.tun != nil {
		old.tun.Close()
	}
	if old.trans != nil {
		old.trans.Stop()
	}

	startErr := m.start(cfg)
	if startErr != nil {
		m.mu.Lock()
		m.clients[cfg.ID] = &runningClient{cfg: cfg, status: "error", lastErr: startErr}
		m.mu.Unlock()
	}

	if m.store != nil {
		if err := m.store.Save(m.configs()); err != nil {
			return fmt.Errorf("client updated but failed to persist: %w", err)
		}
	}
	return startErr
}

// RemoveClient stops and unregisters a client, and removes it from
// persistence so it doesn't come back on the next restart.
func (m *Manager) RemoveClient(id string) error {
	m.mu.Lock()
	rc, ok := m.clients[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("client %q not found", id)
	}
	delete(m.clients, id)
	m.mu.Unlock()

	if rc.tun != nil {
		rc.tun.Close()
	}
	if rc.trans != nil {
		rc.trans.Stop()
	}

	if m.store != nil {
		if err := m.store.Save(m.configs()); err != nil {
			return fmt.Errorf("client removed but failed to persist: %w", err)
		}
	}
	return nil
}

// List returns every registered client's current status, sorted by ID for
// stable output.
func (m *Manager) List() []ClientStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]ClientStatus, 0, len(m.clients))
	for _, rc := range m.clients {
		out = append(out, statusOf(rc))
	}
	return out
}

// Get returns one client's current status.
func (m *Manager) Get(id string) (ClientStatus, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rc, ok := m.clients[id]
	if !ok {
		return ClientStatus{}, false
	}
	return statusOf(rc), true
}

func statusOf(rc *runningClient) ClientStatus {
	s := ClientStatus{
		Config:    rc.cfg,
		Status:    rc.status,
		StartedAt: rc.startedAt,
	}
	if rc.lastErr != nil {
		s.Error = rc.lastErr.Error()
	}
	if rc.tun != nil {
		stats := rc.tun.StatsSnapshot()
		s.Stats = &stats
	}
	if rc.trans != nil {
		ts := rc.trans.Stats()
		s.BytesSent = ts.BytesSent
		s.BytesReceived = ts.BytesReceived
		if cups, ok := unwrapCupsonline(rc.trans); ok {
			s.CupsonlineRooms = cups.RoomsPacked()
		}
	}
	return s
}

// unwrapCupsonline walks down BuildTransport's codec/encryption wrapper
// chain to the raw cupsonline transport underneath, if that's what this
// client uses. Mirrors the exact wrap order BuildTransport uses (see
// exitmgr/transport_test.go's regression test for that order).
func unwrapCupsonline(t transport.Transport) (*cupsonline.CupsonlineTransport, bool) {
	for {
		switch v := t.(type) {
		case *transport.BatchedTransport:
			t = v.Transport
		case *transport.CompressedTransport:
			t = v.Transport
		case *transport.EncryptedTransport:
			t = v.Transport
		case *cupsonline.CupsonlineTransport:
			return v, true
		default:
			return nil, false
		}
	}
}

// configs returns the persisted-config view of every registered client
// (including ones currently in an error state, so a fixable misconfig
// isn't silently dropped from the saved set). Caller must not hold m.mu.
func (m *Manager) configs() []ClientConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ClientConfig, 0, len(m.clients))
	for _, rc := range m.clients {
		out = append(out, rc.cfg)
	}
	return out
}

// Shutdown stops every running client. Intended for process shutdown.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	clients := m.clients
	m.clients = make(map[string]*runningClient)
	m.mu.Unlock()

	for _, rc := range clients {
		if rc.tun != nil {
			rc.tun.Close()
		}
		if rc.trans != nil {
			rc.trans.Stop()
		}
	}
}
