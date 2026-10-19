package exitmgr

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/share"
	"github.com/p1neappleXpress/OpenFlux/transport/ipc"
)

// The test binary doubles as a channel's core: with EXITMGR_FAKE_CORE set
// it reads --config, prints a share link and reports status over IPC, the
// way openflux --config --share --ipc-socket does.
func TestMain(m *testing.M) {
	if os.Getenv("EXITMGR_FAKE_CORE") != "" {
		fakeCore()
		return
	}
	os.Exit(m.Run())
}

func fakeCore() {
	fs := flag.NewFlagSet("core", flag.ExitOnError)
	conf := fs.String("config", "", "")
	sock := fs.String("ipc-socket", "", "")
	fs.Bool("share", false, "")
	fs.Bool("allow-private", false, "")
	_ = fs.Parse(os.Args[1:])

	b, err := os.ReadFile(*conf)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read config:", err)
		os.Exit(2)
	}
	if os.Getenv("EXITMGR_FAKE_CRASH") != "" {
		fmt.Println("crashing on purpose")
		os.Exit(3)
	}
	srv := ipc.NewServer(*sock, nil)
	if err := srv.Listen(); err != nil {
		fmt.Fprintln(os.Stderr, "ipc:", err)
		os.Exit(2)
	}
	fmt.Println("2026/10/19 12:00:00 Running as EXIT NODE (mode=l4)")
	if os.Getenv("EXITMGR_FAKE_ROOMS") != "" {
		// A cups exit prints its link again once it has made its rooms.
		link, err := share.MakeLink(share.Config{
			Negotiate: true,
			Secret:    "0123456789abcdef0123",
			Transports: []share.Transport{
				{Type: "cupsonline", URL: "made-rooms-1", Priority: 70},
				{Type: "cupsonline", Name: "cupsonline-2", URL: "made-rooms-2", Priority: 70},
			},
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "make link:", err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, link)
	}
	fmt.Fprintln(os.Stderr, "█▀▀▀█ ▄ █▀▀▀█")
	fmt.Fprintf(os.Stderr, "openflux://v1/fake-%d\n", len(b))

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	tick := time.NewTicker(50 * time.Millisecond)
	for {
		select {
		case <-sig:
			return
		case <-tick.C:
			_ = srv.SendStatus(&ipc.StatusPayload{Running: true, Connected: true, BytesIn: 10, BytesOut: 20, Active: "vyandex"})
		}
	}
}

func newTestManager(t *testing.T, env ...string) *Manager {
	t.Helper()
	// A data directory far deeper than a Unix socket path may be long.
	dir := filepath.Join(t.TempDir(), strings.Repeat("deep-directory/", 8))
	m, err := New(Options{
		DataDir: dir,
		Binary:  os.Args[0],
		Env:     append(append(os.Environ(), "EXITMGR_FAKE_CORE=1"), env...),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Shutdown(); os.RemoveAll(m.opts.RuntimeDir) })
	return m
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

var docCfg = ClientConfig{
	ID:   "alice",
	Name: "Alice",
	Transports: []TransportEntry{
		{Type: "cupsonline"},
		{Type: "vyandex", URL: "https://disk.yandex.ru/i/doc1"},
		{Type: "yandex", URL: "https://disk.yandex.ru/i/doc2"},
	},
}

func TestAddRunsChannelAndReportsLinkAndStatus(t *testing.T) {
	m := newTestManager(t)
	if err := m.Add(docCfg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "link and connected status", func() bool {
		s, _ := m.Get("alice")
		return s.Link != "" && s.Connected
	})
	s, _ := m.Get("alice")
	if s.Status != "running" || !strings.HasPrefix(s.Link, "openflux://v1/fake-") {
		t.Fatalf("status = %+v", s)
	}
	if s.BytesIn != 10 || s.BytesOut != 20 || len(s.Active) != 1 || s.Active[0] != "vyandex" {
		t.Fatalf("ipc status not taken over: %+v", s)
	}
	if s.Config.Context != "https://disk.yandex.ru/i/doc1" {
		t.Fatalf("context = %q, want the highest-priority document", s.Config.Context)
	}
	logs, _ := m.Logs("alice")
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "Running as EXIT NODE") || strings.Contains(joined, "█") || strings.Contains(joined, "openflux://") {
		t.Fatalf("logs should keep the core's lines without the QR code or the link:\n%s", joined)
	}

	conf, err := os.ReadFile(m.channel("alice").confPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Role = exit", "Mode = l4", "URL = https://disk.yandex.ru/i/doc1",
		"[Transport cupsonline]\nType = cupsonline\nPriority = 70\n",
		"[Transport vyandex]\nType = vyandex\nPriority = 100\nURL = https://disk.yandex.ru/i/doc1",
		"EncryptionKeyFile = " + m.channel("alice").keyPath(),
	} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("node.conf lacks %q:\n%s", want, conf)
		}
	}
}

func TestUpdateKeepsKeyAndContext(t *testing.T) {
	m := newTestManager(t)
	if err := m.Add(docCfg); err != nil {
		t.Fatal(err)
	}
	key1, err := os.ReadFile(m.channel("alice").keyPath())
	if err != nil || len(strings.TrimSpace(string(key1))) < 16 {
		t.Fatalf("key = %q, %v", key1, err)
	}
	upd := ClientConfig{Name: "Alice 2", Transports: []TransportEntry{{Type: "mailru", URL: "https://cloud.mail.ru/public/x"}}, DirectPort: 40001}
	if err := m.Update("alice", upd); err != nil {
		t.Fatal(err)
	}
	key2, _ := os.ReadFile(m.channel("alice").keyPath())
	if string(key1) != string(key2) {
		t.Fatal("update replaced the encryption key: links handed out would stop working")
	}
	s, _ := m.Get("alice")
	if s.Config.Context != "https://disk.yandex.ru/i/doc1" || s.Config.Name != "Alice 2" {
		t.Fatalf("config after update = %+v", s.Config)
	}
	conf, _ := os.ReadFile(m.channel("alice").confPath())
	if !strings.Contains(string(conf), "[Transport direct]\nType = direct\nPriority = 50\nListen = 0.0.0.0:40001") ||
		strings.Contains(string(conf), "vyandex") {
		t.Fatalf("node.conf after update:\n%s", conf)
	}
	waitFor(t, "restarted channel", func() bool { s, _ := m.Get("alice"); return s.Connected })
}

func TestRemoveStopsAndDeletesChannel(t *testing.T) {
	m := newTestManager(t)
	if err := m.Add(docCfg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool { s, _ := m.Get("alice"); return s.Connected })
	if err := m.Remove("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.channel("alice").dir); !os.IsNotExist(err) {
		t.Fatalf("channel directory still there: %v", err)
	}
	if len(m.List()) != 0 {
		t.Fatal("client still listed")
	}
	saved, _ := m.store.Load()
	if len(saved) != 0 {
		t.Fatalf("client still saved: %+v", saved)
	}
}

func TestLoadRestartsSavedClients(t *testing.T) {
	m := newTestManager(t)
	if err := m.Add(docCfg); err != nil {
		t.Fatal(err)
	}
	m.Shutdown()

	m2, err := New(m.opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m2.Shutdown)
	if err := m2.Load(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reloaded client", func() bool { s, ok := m2.Get("alice"); return ok && s.Connected })
}

func TestCrashingCoreIsRestarted(t *testing.T) {
	m := newTestManager(t, "EXITMGR_FAKE_CRASH=1")
	if err := m.Add(docCfg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a restart", func() bool { s, _ := m.Get("alice"); return s.Restarts >= 1 })
	s, _ := m.Get("alice")
	if s.Connected {
		t.Fatal("a crashing core reported connected")
	}
	logs, _ := m.Logs("alice")
	if !strings.Contains(strings.Join(logs, "\n"), "crashing on purpose") {
		t.Fatalf("logs = %q", logs)
	}
}

func TestValidation(t *testing.T) {
	m := newTestManager(t)
	bad := []ClientConfig{
		{ID: "../x", Transports: docCfg.Transports},
		{ID: "a"},
		{ID: "a", Transports: []TransportEntry{{Type: "oneme", URL: "x"}}},
		{ID: "a", Transports: []TransportEntry{{Type: "yandex"}}},
		{ID: "a", Transports: []TransportEntry{{Type: "yandex", URL: "https://x/#frag"}}},
		{ID: "a", Transports: []TransportEntry{{Type: "yandex", URL: "https://x"}}, Mode: "l7"},
	}
	for i, c := range bad {
		if err := m.Add(c); err == nil {
			t.Errorf("case %d: %+v accepted", i, c)
		}
	}
	if err := m.Add(ClientConfig{ID: "a", Transports: []TransportEntry{{Type: "avito"}}, DirectPort: 40002}); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(ClientConfig{ID: "b", Transports: []TransportEntry{{Type: "avito"}}, DirectPort: 40002}); err == nil {
		t.Fatal("a second client got the same direct port")
	}
	if _, err := os.Stat(filepath.Join(m.opts.DataDir, "clients", "b")); !os.IsNotExist(err) {
		t.Fatal("a rejected client left a channel directory")
	}
}

func TestRoomsTheExitMadeAreKept(t *testing.T) {
	m := newTestManager(t, "EXITMGR_FAKE_ROOMS=1")
	cfg := ClientConfig{ID: "cups", Transports: []TransportEntry{{Type: "cupsonline"}, {Type: "cupsonline"}}}
	if err := m.Add(cfg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "rooms written back", func() bool {
		saved, _ := m.store.Load()
		return len(saved) == 1 && saved[0].Transports[1].URL == "made-rooms-2"
	})
	s, _ := m.Get("cups")
	if s.Config.Transports[0].URL != "made-rooms-1" {
		t.Fatalf("transports = %+v", s.Config.Transports)
	}
	if s.Config.Context != "openflux-panel-cups" {
		t.Fatalf("context = %q: a channel without a document needs a fixed one", s.Config.Context)
	}
	conf, _ := os.ReadFile(m.channel("cups").confPath())
	for _, want := range []string{
		"[Transport cupsonline]\nType = cupsonline\nPriority = 70\nURL = made-rooms-1",
		"[Transport cupsonline-2]\nType = cupsonline\nPriority = 70\nURL = made-rooms-2",
		"URL = openflux-panel-cups",
	} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("node.conf lacks %q:\n%s", want, conf)
		}
	}
}

func TestVolgaPublicLinkBecomesEditorLink(t *testing.T) {
	m := newTestManager(t)
	saved := []ClientConfig{{
		ID: "old", Context: "https://disk.yandex.ru/d/4wYUcwmQ13opeQ",
		Transports: []TransportEntry{{Type: "vyandex", URL: "https://disk.yandex.ru/d/4wYUcwmQ13opeQ"}},
	}}
	if err := m.store.Save(saved); err != nil {
		t.Fatal(err)
	}
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	const editor = "https://disk.yandex.ru/edit/d/4wYUcwmQ13opeQ"
	s, _ := m.Get("old")
	if s.Config.Transports[0].URL != editor {
		t.Fatalf("url = %q, want the editor link", s.Config.Transports[0].URL)
	}
	if s.Config.Context != saved[0].Context {
		t.Fatalf("context changed to %q: links handed out would stop decrypting", s.Config.Context)
	}
	back, _ := m.store.Load()
	conf, _ := os.ReadFile(m.channel("old").confPath())
	if back[0].Transports[0].URL != editor || !strings.Contains(string(conf), "URL = "+editor) {
		t.Fatalf("fix not saved: %+v\n%s", back, conf)
	}

	if err := m.Add(ClientConfig{ID: "new", Transports: []TransportEntry{{Type: "vyandex", URL: "https://disk.yandex.ru/d/abcdefgh"}}}); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.Get("new"); s.Config.Transports[0].URL != "https://disk.yandex.ru/edit/d/abcdefgh" {
		t.Fatalf("Add kept the preview link: %+v", s.Config.Transports)
	}
}

func TestRelativeDataDirBecomesAbsolute(t *testing.T) {
	m, err := New(Options{DataDir: filepath.Join("testdata-rel", "pd"), Binary: os.Args[0]})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll("testdata-rel") })
	if !filepath.IsAbs(m.opts.DataDir) {
		t.Fatalf("DataDir %q is relative: the core, run in its channel directory, would not find node.conf", m.opts.DataDir)
	}
}

func TestSameTypeCarriersGetUniqueNames(t *testing.T) {
	cfg := ClientConfig{ID: "x", Transports: []TransportEntry{
		{Type: "yandex", URL: "https://a"}, {Type: "yandex", URL: "https://b"},
	}}
	conf := nodeConf(cfg, channel{dir: "/tmp/x"})
	if !strings.Contains(conf, "[Transport yandex]\n") || !strings.Contains(conf, "[Transport yandex-2]\n") {
		t.Fatalf("node.conf:\n%s", conf)
	}
}
