package exitmgr

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
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
	cookies := fs.String("yandex-cookies-file", "", "")
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
	if *cookies != "" {
		fmt.Println("fake core: yandex cookies from " + *cookies)
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
		{Type: "vyandex", URL: "https://docs.yandex.ru/edit/d/doc1doc1doc1doc1doc1"},
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
	if s.Config.Context != "https://docs.yandex.ru/edit/d/doc1doc1doc1doc1doc1" {
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
		"Role = exit", "Mode = l4", "URL = https://docs.yandex.ru/edit/d/doc1doc1doc1doc1doc1",
		"[Transport cupsonline]\nType = cupsonline\nPriority = 70\n",
		"[Transport vyandex]\nType = vyandex\nPriority = 100\nURL = https://docs.yandex.ru/edit/d/doc1doc1doc1doc1doc1",
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
	if s.Config.Context != "https://docs.yandex.ru/edit/d/doc1doc1doc1doc1doc1" || s.Config.Name != "Alice 2" {
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

func TestVolgaLinks(t *testing.T) {
	m := newTestManager(t)
	const id = "aClML4UxXBd6dDfLn4d_ACPegnqahzm72s0qoIz-cKg6Tko1UUFIZnN0QQ"
	err := m.Add(ClientConfig{ID: "v", Transports: []TransportEntry{
		{Type: "vyandex", URL: "https://docs.yandex.com/edit/d/" + id + "?source=docs&sk=y6bb"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("v")
	if got := s.Config.Transports[0].URL; got != "https://docs.yandex.ru/edit/d/"+id {
		t.Fatalf("url = %q: want the browser's session key cut off and the yandex.ru host the sign-in is for", got)
	}
	for _, bad := range []string{
		"https://disk.yandex.ru/d/4wYUcwmQ13opeQ",
		"https://yadi.sk/d/gOBW-OE8UDTSkw",
		"https://disk.yandex.com/i/6Ut8tYNfABaA9w",
		"https://disk.yandex.ru/edit/d/4wYUcwmQ13opeQ", // a Disk id is no Docs id
	} {
		err := m.Add(ClientConfig{ID: "bad", Transports: []TransportEntry{{Type: "vyandex", URL: bad}}})
		if err == nil || !strings.Contains(err.Error(), "docs.yandex.ru/edit/d/") {
			t.Errorf("%s: err = %v, want a pointer to the editor address", bad, err)
		}
	}
}

func TestBusyDirectPortIsRefused(t *testing.T) {
	m := newTestManager(t)
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	busy := ln.Addr().(*net.TCPAddr).Port
	err = m.Add(ClientConfig{ID: "p", Transports: []TransportEntry{{Type: "cupsonline"}}, DirectPort: busy})
	if err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("err = %v, want the busy port refused", err)
	}
	if _, ok := m.Get("p"); ok {
		t.Fatal("a client with a busy port was registered")
	}

	if err := m.Add(ClientConfig{ID: "p", Transports: []TransportEntry{{Type: "cupsonline"}}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Update("p", ClientConfig{Transports: []TransportEntry{{Type: "cupsonline"}}, DirectPort: busy}); err == nil {
		t.Fatal("update to a busy port accepted")
	}
}

const signedIn = ".yandex.ru\tTRUE\t/\tTRUE\t0\tSession_id\tsecret-session\n.yandex.ru\tTRUE\t/\tFALSE\t0\tyandexuid\t123\n"

func TestYandexCookiesReachEveryChannel(t *testing.T) {
	m := newTestManager(t)
	if err := m.Add(docCfg); err != nil {
		t.Fatal(err)
	}
	if err := m.SetYandexCookies([]byte(".yandex.ru\tTRUE\t/\tFALSE\t0\tyandexuid\t123\n")); err == nil {
		t.Fatal("cookies of a signed-out tab accepted")
	}
	if err := m.SetYandexCookies([]byte("garbage")); err == nil {
		t.Fatal("garbage accepted")
	}
	if set, _ := m.YandexCookies(); set {
		t.Fatal("rejected cookies were kept")
	}

	if err := m.SetYandexCookies([]byte(signedIn)); err != nil {
		t.Fatal(err)
	}
	if set, _ := m.YandexCookies(); !set {
		t.Fatal("cookies not kept")
	}
	if st, err := os.Stat(m.yandexCookiesPath()); err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Fatalf("cookies file: %v %v, want root-only", st, err)
	}
	waitFor(t, "the restarted channel to get the cookies", func() bool {
		logs, _ := m.Logs("alice")
		return strings.Contains(strings.Join(logs, "\n"), "yandex cookies from "+m.yandexCookiesPath())
	})

	// A Cookie header copied from the browser's developer tools works too.
	if err := m.SetYandexCookies([]byte("Cookie: yandexuid=123; Session_id=3:abc.def; L=xyz")); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(m.yandexCookiesPath())
	if !strings.Contains(string(saved), ".yandex.ru\tTRUE\t/\tTRUE\t0\tSession_id\t3:abc.def\n") {
		t.Fatalf("header not turned into cookies.txt:\n%s", saved)
	}

	if err := m.ClearYandexCookies(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a channel without the cookies", func() bool {
		logs, _ := m.Logs("alice")
		joined := strings.Join(logs, "\n")
		return strings.Contains(joined, "Running as EXIT NODE") && !strings.Contains(joined, "yandex cookies from")
	})
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
