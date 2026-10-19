package exitmgr

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/p1neappleXpress/OpenFlux/share"
)

// Options configure a Manager.
type Options struct {
	// DataDir holds clients.json and one directory per client under clients/.
	DataDir string
	// Binary is the openflux core every channel runs. Default: this executable.
	Binary string
	// ExtraArgs go to every channel's core after the panel's own
	// (--share-host, --allow-private).
	ExtraArgs []string
	// Env is the channels' environment. nil: this process's environment.
	Env []string
	// RuntimeDir holds the channels' IPC sockets. A Unix socket path is
	// limited to ~100 bytes, which a data directory deep in a home folder
	// overruns. Default: a fresh private directory in the system temp.
	RuntimeDir string
}

// ClientStatus is one client's state for the panel's API.
type ClientStatus struct {
	Config    ClientConfig `json:"config"`
	Status    string       `json:"status"` // starting | running | restarting | stopped | error
	Error     string       `json:"error,omitempty"`
	StartedAt time.Time    `json:"started_at,omitempty"`
	Restarts  int          `json:"restarts,omitempty"`
	// Link is the openflux:// link (and QR code) the client connects with.
	// It carries the channel's encryption key.
	Link      string   `json:"link,omitempty"`
	Connected bool     `json:"connected"`
	BytesIn   uint64   `json:"bytes_in"`
	BytesOut  uint64   `json:"bytes_out"`
	Active    []string `json:"active,omitempty"`
	Check     *Check   `json:"check,omitempty"`
}

type entry struct {
	cfg  ClientConfig
	proc *proc  // nil when the channel could not be written
	err  string // why proc is nil
}

// Manager runs one exit channel per registered client.
type Manager struct {
	opts       Options
	store      *Store
	ownRuntime bool // RuntimeDir was made here: Shutdown removes it

	mu      sync.Mutex
	clients map[string]*entry
}

// New makes a Manager. Call Load to start the clients saved earlier.
func New(opts Options) (*Manager, error) {
	if opts.DataDir == "" {
		return nil, fmt.Errorf("exitmgr: DataDir is required")
	}
	// Every channel's core runs in its own directory: node.conf's paths
	// must not depend on the panel's working directory.
	dir, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return nil, fmt.Errorf("exitmgr: %w", err)
	}
	opts.DataDir = dir
	if opts.Binary == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("exitmgr: find own executable: %w", err)
		}
		opts.Binary = exe
	}
	if opts.Env == nil {
		opts.Env = os.Environ()
	}
	if err := os.MkdirAll(filepath.Join(opts.DataDir, "clients"), 0o700); err != nil {
		return nil, err
	}
	ownRuntime := opts.RuntimeDir == ""
	if ownRuntime {
		// 0700: nobody else on the host can connect to a channel's socket.
		rt, err := os.MkdirTemp("", "ofpanel-")
		if err != nil {
			return nil, fmt.Errorf("exitmgr: runtime directory: %w", err)
		}
		opts.RuntimeDir = rt
	} else if err := os.MkdirAll(opts.RuntimeDir, 0o700); err != nil {
		return nil, err
	}
	return &Manager{
		opts:       opts,
		store:      NewStore(filepath.Join(opts.DataDir, "clients.json")),
		ownRuntime: ownRuntime,
		clients:    make(map[string]*entry),
	}, nil
}

// Load starts every saved client. A client whose channel cannot be written
// stays listed with its error, so the operator sees it and can fix it.
func (m *Manager) Load() error {
	cfgs, err := m.store.Load()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	fixed := false
	for _, cfg := range cfgs {
		if cfg.normalize() {
			fixed = true
		}
		if cfg.Context == "" {
			cfg.Context = defaultContext(cfg.ID, cfg.Transports)
			fixed = true
		}
		m.clients[cfg.ID] = m.launch(cfg)
	}
	if fixed {
		return m.save()
	}
	return nil
}

// Add registers a client, writes its channel and starts it.
func (m *Manager) Add(cfg ClientConfig) error {
	cfg.Transports = append([]TransportEntry(nil), cfg.Transports...)
	cfg.normalize()
	if cfg.Context == "" {
		cfg.Context = defaultContext(cfg.ID, cfg.Transports)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.clients[cfg.ID]; ok {
		return fmt.Errorf("client %q already exists", cfg.ID)
	}
	if err := m.portFree(cfg); err != nil {
		return err
	}
	if err := portBindable(cfg.DirectPort); err != nil {
		return err
	}
	cfg.Transports = append([]TransportEntry(nil), cfg.Transports...)
	e := m.launch(cfg)
	m.clients[cfg.ID] = e
	if err := m.save(); err != nil {
		return fmt.Errorf("client added but not saved: %w", err)
	}
	if e.err != "" {
		return fmt.Errorf("%s", e.err)
	}
	return nil
}

// Update replaces a client's transports, name, mode or direct port and
// restarts its channel. The key and the encryption context stay, so a link
// already handed out keeps working for the carriers it still names.
func (m *Manager) Update(id string, cfg ClientConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.clients[id]
	if !ok {
		return fmt.Errorf("client %q not found", id)
	}
	cfg.ID = id
	cfg.Transports = append([]TransportEntry(nil), cfg.Transports...)
	cfg.normalize()
	cfg.Context = old.cfg.Context
	if cfg.Context == "" {
		cfg.Context = defaultContext(id, cfg.Transports)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := m.portFree(cfg); err != nil {
		return err
	}
	// The client's own channel holds its current port; only a new one is tried.
	if cfg.DirectPort != old.cfg.DirectPort {
		if err := portBindable(cfg.DirectPort); err != nil {
			return err
		}
	}
	if old.proc != nil {
		old.proc.stop()
	}
	cfg.Transports = append([]TransportEntry(nil), cfg.Transports...)
	e := m.launch(cfg)
	m.clients[id] = e
	if err := m.save(); err != nil {
		return fmt.Errorf("client updated but not saved: %w", err)
	}
	if e.err != "" {
		return fmt.Errorf("%s", e.err)
	}
	return nil
}

// Remove stops a client and deletes its channel, key included.
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.clients[id]
	if !ok {
		return fmt.Errorf("client %q not found", id)
	}
	if e.proc != nil {
		e.proc.stop()
	}
	delete(m.clients, id)
	if err := os.RemoveAll(m.channel(id).dir); err != nil {
		return fmt.Errorf("remove channel directory: %w", err)
	}
	return m.save()
}

// List returns every client, sorted by name then id.
func (m *Manager) List() []ClientStatus {
	m.mu.Lock()
	out := make([]ClientStatus, 0, len(m.clients))
	for _, e := range m.clients {
		out = append(out, statusOf(e))
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Config.Name != out[j].Config.Name {
			return out[i].Config.Name < out[j].Config.Name
		}
		return out[i].Config.ID < out[j].Config.ID
	})
	return out
}

// Get returns one client's status.
func (m *Manager) Get(id string) (ClientStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.clients[id]
	if !ok {
		return ClientStatus{}, false
	}
	return statusOf(e), true
}

// Logs returns the last lines a client's core printed.
func (m *Manager) Logs(id string) ([]string, bool) {
	m.mu.Lock()
	e, ok := m.clients[id]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	if e.proc == nil {
		return []string{e.err}, true
	}
	return e.proc.logTail(), true
}

// Shutdown stops every channel (they come back with Load on the next start).
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range m.clients {
		if e.proc != nil {
			wg.Add(1)
			go func(p *proc) { defer wg.Done(); p.stop() }(e.proc)
		}
	}
	wg.Wait()
	for _, e := range m.clients {
		os.Remove(m.channel(e.cfg.ID).socketPath())
	}
	if m.ownRuntime {
		os.RemoveAll(m.opts.RuntimeDir)
	}
}

func (m *Manager) channel(id string) channel {
	return channel{
		dir:  filepath.Join(m.opts.DataDir, "clients", id),
		sock: filepath.Join(m.opts.RuntimeDir, id+".sock"),
	}
}

// launch writes cfg's channel and starts its core. Caller holds m.mu.
func (m *Manager) launch(cfg ClientConfig) *entry {
	if err := cfg.Validate(); err != nil {
		return &entry{cfg: cfg, err: err.Error()}
	}
	ch := m.channel(cfg.ID)
	if err := ch.ensureKey(); err != nil {
		return &entry{cfg: cfg, err: "encryption key: " + err.Error()}
	}
	if err := ch.writeConf(cfg); err != nil {
		return &entry{cfg: cfg, err: "node.conf: " + err.Error()}
	}
	argv := append([]string{m.opts.Binary,
		"--config", ch.confPath(),
		"--share",
		"--ipc-socket", ch.socketPath(),
	}, m.opts.ExtraArgs...)
	if _, err := os.Stat(m.yandexCookiesPath()); err == nil {
		argv = append(argv, "--yandex-cookies-file", m.yandexCookiesPath())
	}
	e := &entry{cfg: cfg}
	// Off the proc's goroutine: Update and Remove hold m.mu while they wait
	// for a proc to stop.
	onLink := func(link string) { go m.keepRooms(cfg.ID, e, link) }
	e.proc = startProc(argv, m.opts.Env, ch.dir, ch.socketPath(), onLink)
	return e
}

// keepRooms writes the rooms a cupsonline or avito carrier made back into
// its client's config, from the link the core printed. The next start of
// the channel then rejoins them, so the link handed out keeps working
// across panel restarts and edits.
func (m *Manager) keepRooms(id string, e *entry, link string) {
	r := share.Read(link)
	if r.Config == nil {
		return
	}
	byName := map[string]string{}
	for _, t := range r.Config.Transports {
		name := t.Name
		if name == "" {
			name = t.Type
		}
		byName[name] = t.URL
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clients[id] != e {
		return // replaced or removed meanwhile
	}
	names := carrierNames(e.cfg.Transports)
	changed := false
	for i, t := range e.cfg.Transports {
		if makesOwnRooms[t.Type] && t.URL == "" {
			if u := byName[names[i]]; u != "" && confSafe(u) {
				e.cfg.Transports[i].URL = u
				changed = true
			}
		}
	}
	if !changed {
		return
	}
	if err := m.channel(id).writeConf(e.cfg); err != nil {
		log.Printf("exit panel: %s: keep rooms: %v", id, err)
	}
	if err := m.save(); err != nil {
		log.Printf("exit panel: %s: keep rooms: %v", id, err)
	}
}

// portFree rejects a direct port another client already listens on.
func (m *Manager) portFree(cfg ClientConfig) error {
	if cfg.DirectPort == 0 {
		return nil
	}
	for id, e := range m.clients {
		if id != cfg.ID && e.cfg.DirectPort == cfg.DirectPort {
			return fmt.Errorf("direct port %d is already used by %q", cfg.DirectPort, e.cfg.Name)
		}
	}
	return nil
}

// save writes the client list. Caller holds m.mu.
func (m *Manager) save() error {
	cfgs := make([]ClientConfig, 0, len(m.clients))
	for _, e := range m.clients {
		cfgs = append(cfgs, e.cfg)
	}
	sort.Slice(cfgs, func(i, j int) bool { return cfgs[i].ID < cfgs[j].ID })
	return m.store.Save(cfgs)
}

func statusOf(e *entry) ClientStatus {
	s := ClientStatus{Config: e.cfg}
	if e.proc == nil {
		s.Status, s.Error = "error", e.err
		return s
	}
	st := e.proc.state()
	s.Status = st.Status
	s.Error = st.Error
	s.StartedAt = st.StartedAt
	s.Restarts = st.Restarts
	s.Link = st.Link
	s.Connected = st.Connected
	s.BytesIn = st.BytesIn
	s.BytesOut = st.BytesOut
	s.Active = st.Active
	s.Check = st.Check
	return s
}
