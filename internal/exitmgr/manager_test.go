package exitmgr

import (
	"fmt"
	"path/filepath"
	"testing"

	"openflux/internal/transport"
)

// stubTransport is an in-memory transport.Transport for testing Manager's
// lifecycle logic without any real network I/O.
type stubTransport struct {
	startErr  error
	startedCt int
	stopped   bool
}

func (s *stubTransport) Start() error {
	s.startedCt++
	return s.startErr
}
func (s *stubTransport) Stop() error                   { s.stopped = true; return nil }
func (s *stubTransport) Send(data []byte) error         { return nil }
func (s *stubTransport) Receive(cb func([]byte))        {}
func (s *stubTransport) IsConnected() bool              { return s.startErr == nil }
func (s *stubTransport) Stats() transport.TransportStats { return transport.TransportStats{} }

func stubBuilder(fail bool) func(ClientConfig, transport.TransportConfig) (transport.Transport, error) {
	return func(cfg ClientConfig, base transport.TransportConfig) (transport.Transport, error) {
		if fail {
			return &stubTransport{startErr: fmt.Errorf("simulated connect failure")}, nil
		}
		return &stubTransport{}, nil
	}
}

func validConfig(id string) ClientConfig {
	return ClientConfig{ID: id, Name: "test " + id, Transport: "yandex", URL: "https://disk.yandex.com/i/test"}
}

func TestClientConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ClientConfig
		wantErr bool
	}{
		{"valid yandex", ClientConfig{ID: "a", Transport: "yandex", URL: "https://x"}, false},
		{"missing id", ClientConfig{Transport: "yandex", URL: "https://x"}, true},
		{"unknown transport", ClientConfig{ID: "a", Transport: "carrier-pigeon"}, true},
		{"yandex missing url", ClientConfig{ID: "a", Transport: "yandex"}, true},
		{"oneme missing token", ClientConfig{ID: "a", Transport: "oneme", MaxUid: "1"}, true},
		{"oneme valid", ClientConfig{ID: "a", Transport: "oneme", MaxToken: "t", MaxUid: "1"}, false},
		{"bad codec", ClientConfig{ID: "a", Transport: "yandex", URL: "https://x", Codec: "zip"}, true},
		// cupsonline's exit side generates its own rooms and ignores cfg.URL
		// entirely (see exitmgr.BuildTransport / cupsonline.NewCupsonlineTransport),
		// unlike every other URL-based transport.
		{"cupsonline needs no url", ClientConfig{ID: "a", Transport: "cupsonline"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestManagerAddListRemove(t *testing.T) {
	m := NewManagerWithBuilder(nil, stubBuilder(false))

	if err := m.AddClient(validConfig("c1")); err != nil {
		t.Fatalf("AddClient: %v", err)
	}
	if err := m.AddClient(validConfig("c2")); err != nil {
		t.Fatalf("AddClient: %v", err)
	}

	list := m.List()
	if len(list) != 2 {
		t.Fatalf("List() returned %d clients, want 2", len(list))
	}

	status, ok := m.Get("c1")
	if !ok {
		t.Fatal("Get(c1) not found")
	}
	if status.Status != "running" {
		t.Fatalf("status = %q, want running", status.Status)
	}
	if status.Stats == nil {
		t.Fatal("expected a stats snapshot for a running client")
	}

	if err := m.RemoveClient("c1"); err != nil {
		t.Fatalf("RemoveClient: %v", err)
	}
	if _, ok := m.Get("c1"); ok {
		t.Fatal("client still present after RemoveClient")
	}
	if len(m.List()) != 1 {
		t.Fatalf("List() returned %d clients after removal, want 1", len(m.List()))
	}
}

func TestManagerAddClientRejectsDuplicateID(t *testing.T) {
	m := NewManagerWithBuilder(nil, stubBuilder(false))
	if err := m.AddClient(validConfig("dup")); err != nil {
		t.Fatalf("first AddClient: %v", err)
	}
	if err := m.AddClient(validConfig("dup")); err == nil {
		t.Fatal("expected an error adding a duplicate client ID")
	}
}

// A client whose transport fails to start must still show up in List()
// with an error status -- not vanish -- so the operator can see and fix it
// instead of wondering why nothing happened.
func TestManagerAddClientRecordsStartFailure(t *testing.T) {
	m := NewManagerWithBuilder(nil, stubBuilder(true))
	err := m.AddClient(validConfig("broken"))
	if err == nil {
		t.Fatal("expected AddClient to return the start error")
	}

	status, ok := m.Get("broken")
	if !ok {
		t.Fatal("failed client should still be registered")
	}
	if status.Status != "error" {
		t.Fatalf("status = %q, want error", status.Status)
	}
	if status.Error == "" {
		t.Fatal("expected a non-empty error message")
	}
}

func TestManagerRemoveClientStopsTransport(t *testing.T) {
	var started *stubTransport
	builder := func(cfg ClientConfig, base transport.TransportConfig) (transport.Transport, error) {
		started = &stubTransport{}
		return started, nil
	}
	m := NewManagerWithBuilder(nil, builder)
	if err := m.AddClient(validConfig("c1")); err != nil {
		t.Fatalf("AddClient: %v", err)
	}
	if err := m.RemoveClient("c1"); err != nil {
		t.Fatalf("RemoveClient: %v", err)
	}
	if !started.stopped {
		t.Fatal("transport.Stop() was not called on RemoveClient")
	}
}

func TestManagerUpdateClientReplacesConfigAndStopsOldTransport(t *testing.T) {
	var last *stubTransport
	builder := func(cfg ClientConfig, base transport.TransportConfig) (transport.Transport, error) {
		last = &stubTransport{}
		return last, nil
	}
	m := NewManagerWithBuilder(nil, builder)
	if err := m.AddClient(validConfig("c1")); err != nil {
		t.Fatalf("AddClient: %v", err)
	}
	first := last

	updated := validConfig("c1")
	updated.URL = "https://disk.yandex.com/i/changed"
	if err := m.UpdateClient("c1", updated); err != nil {
		t.Fatalf("UpdateClient: %v", err)
	}

	if !first.stopped {
		t.Fatal("old transport was not stopped on update")
	}
	status, ok := m.Get("c1")
	if !ok {
		t.Fatal("client missing after update")
	}
	if status.Config.URL != "https://disk.yandex.com/i/changed" {
		t.Fatalf("URL = %q, want the updated URL", status.Config.URL)
	}
}

func TestManagerUpdateClientRejectsMissingID(t *testing.T) {
	m := NewManagerWithBuilder(nil, stubBuilder(false))
	if err := m.UpdateClient("does-not-exist", validConfig("does-not-exist")); err == nil {
		t.Fatal("expected an error updating a client that was never added")
	}
}

// Mirrors TestManagerAddClientRecordsStartFailure: an update whose new
// config fails to start must leave the client visibly broken, not silently
// keep the old (now-stopped) transport running or vanish.
func TestManagerUpdateClientRecordsStartFailure(t *testing.T) {
	m := NewManagerWithBuilder(nil, stubBuilder(false))
	if err := m.AddClient(validConfig("c1")); err != nil {
		t.Fatalf("AddClient: %v", err)
	}

	m.buildTransport = func(cfg ClientConfig, base transport.TransportConfig) (transport.Transport, error) {
		return &stubTransport{startErr: fmt.Errorf("simulated connect failure")}, nil
	}
	if err := m.UpdateClient("c1", validConfig("c1")); err == nil {
		t.Fatal("expected UpdateClient to return the start error")
	}

	status, ok := m.Get("c1")
	if !ok {
		t.Fatal("client should still be registered after a failed update")
	}
	if status.Status != "error" {
		t.Fatalf("status = %q, want error", status.Status)
	}
}

func TestManagerUpdateClientPersists(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "clients.json"))

	m1 := NewManagerWithBuilder(store, stubBuilder(false))
	if err := m1.AddClient(validConfig("c1")); err != nil {
		t.Fatalf("AddClient: %v", err)
	}
	updated := validConfig("c1")
	updated.Name = "renamed"
	if err := m1.UpdateClient("c1", updated); err != nil {
		t.Fatalf("UpdateClient: %v", err)
	}

	m2 := NewManagerWithBuilder(store, stubBuilder(false))
	if err := m2.LoadPersisted(); err != nil {
		t.Fatalf("LoadPersisted: %v", err)
	}
	status, ok := m2.Get("c1")
	if !ok {
		t.Fatal("updated client was not restored into m2 from the store")
	}
	if status.Config.Name != "renamed" {
		t.Fatalf("Name = %q, want %q", status.Config.Name, "renamed")
	}
}

func TestManagerPersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "clients.json"))

	m1 := NewManagerWithBuilder(store, stubBuilder(false))
	if err := m1.AddClient(validConfig("persisted")); err != nil {
		t.Fatalf("AddClient: %v", err)
	}

	m2 := NewManagerWithBuilder(store, stubBuilder(false))
	if err := m2.LoadPersisted(); err != nil {
		t.Fatalf("LoadPersisted: %v", err)
	}
	if _, ok := m2.Get("persisted"); !ok {
		t.Fatal("client added via m1 was not restored into m2 from the store")
	}
}

func TestManagerRemoveClientDropsFromPersistence(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "clients.json"))

	m1 := NewManagerWithBuilder(store, stubBuilder(false))
	if err := m1.AddClient(validConfig("temp")); err != nil {
		t.Fatalf("AddClient: %v", err)
	}
	if err := m1.RemoveClient("temp"); err != nil {
		t.Fatalf("RemoveClient: %v", err)
	}

	m2 := NewManagerWithBuilder(store, stubBuilder(false))
	if err := m2.LoadPersisted(); err != nil {
		t.Fatalf("LoadPersisted: %v", err)
	}
	if _, ok := m2.Get("temp"); ok {
		t.Fatal("removed client was reloaded from the store")
	}
}
