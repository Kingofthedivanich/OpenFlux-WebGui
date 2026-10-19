package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/exitmgr"
	"github.com/p1neappleXpress/OpenFlux/share"
)

const (
	testToken = "123456:SECRET-token-value"
	adminID   = int64(1001)
	otherID   = int64(2002)
)

// ---- fake Telegram Bot API ----

type sentMessage struct {
	ChatID int64
	Text   string
	KB     *inlineKeyboard
}

type fakeTelegram struct {
	mu     sync.Mutex
	sent   []sentMessage
	photos []string // captions
	srv    *httptest.Server
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasPrefix(r.URL.Path, "/bot"+testToken+"/") {
			w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sendPhoto") {
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			file, _, err := r.FormFile("photo")
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			data, _ := io.ReadAll(file)
			file.Close()
			if !bytes.HasPrefix(data, []byte("\x89PNG")) {
				http.Error(w, "not a png", 400)
				return
			}
			f.mu.Lock()
			f.photos = append(f.photos, r.FormValue("caption"))
			f.mu.Unlock()
			w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		var body struct {
			ChatID      int64           `json:"chat_id"`
			Text        string          `json:"text"`
			ReplyMarkup *inlineKeyboard `json:"reply_markup"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			f.mu.Lock()
			f.sent = append(f.sent, sentMessage{ChatID: body.ChatID, Text: body.Text, KB: body.ReplyMarkup})
			f.mu.Unlock()
		}
		w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTelegram) last() sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return sentMessage{}
	}
	return f.sent[len(f.sent)-1]
}

func (f *fakeTelegram) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeTelegram) photoCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.photos)
}

// hasButton reports whether the last message offers a button with data.
func (f *fakeTelegram) hasButton(data string) bool {
	kb := f.last().KB
	if kb == nil {
		return false
	}
	for _, r := range kb.InlineKeyboard {
		for _, b := range r {
			if b.CallbackData == data {
				return true
			}
		}
	}
	return false
}

// ---- fixtures ----

type stubManager struct {
	mu      sync.Mutex
	clients map[string]exitmgr.ClientStatus
	logs    []string
}

func newStubManager() *stubManager {
	return &stubManager{clients: map[string]exitmgr.ClientStatus{}}
}

func (m *stubManager) List() []exitmgr.ClientStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []exitmgr.ClientStatus
	for _, c := range m.clients {
		out = append(out, c)
	}
	return out
}

func (m *stubManager) Get(id string) (exitmgr.ClientStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.clients[id]
	return c, ok
}

func (m *stubManager) Logs(id string) ([]string, bool) {
	if _, ok := m.Get(id); !ok {
		return nil, false
	}
	return m.logs, true
}

func (m *stubManager) Add(cfg exitmgr.ClientConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[cfg.ID] = exitmgr.ClientStatus{Config: cfg, Status: "running"}
	return nil
}

func (m *stubManager) Update(id string, cfg exitmgr.ClientConfig) error {
	cfg.ID = id
	if err := cfg.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.clients[id]; !ok {
		return fmt.Errorf("client %q not found", id)
	}
	m.clients[id] = exitmgr.ClientStatus{Config: cfg, Status: "running"}
	return nil
}

func (m *stubManager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.clients[id]; !ok {
		return fmt.Errorf("client %q not found", id)
	}
	delete(m.clients, id)
	return nil
}

func (m *stubManager) only(t *testing.T) exitmgr.ClientStatus {
	t.Helper()
	list := m.List()
	if len(list) != 1 {
		t.Fatalf("%d clients, want 1", len(list))
	}
	return list[0]
}

func newTestBot(t *testing.T) (*Bot, *fakeTelegram, *stubManager) {
	t.Helper()
	f := newFakeTelegram(t)
	mgr := newStubManager()
	b := New(testToken, []int64{adminID}, mgr)
	b.api.base = f.srv.URL
	return b, f, mgr
}

func msg(from int64, text string) Update {
	return Update{Message: &Message{From: &User{ID: from}, Chat: Chat{ID: from}, Text: text}}
}

func press(from int64, data string) Update {
	return Update{CallbackQuery: &CallbackQuery{ID: "cb", From: User{ID: from}, Message: &Message{Chat: Chat{ID: from}}, Data: data}}
}

// ---- the token never leaks ----

func TestTransportErrorsDoNotCarryTheToken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens: the request fails before any answer

	a := newAPI(testToken)
	a.base = "http://" + addr
	a.client.Timeout = 2 * time.Second
	for name, call := range map[string]func() error{
		"call":      func() error { _, err := a.getUpdates(0, 0); return err },
		"sendPhoto": func() error { return a.sendPhoto(1, "x.png", []byte("x"), "") },
	} {
		err := call()
		if err == nil {
			t.Fatalf("%s: no error from a dead server", name)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("%s: error carries the token: %v", name, err)
		}
	}
}

func TestRunLogsWithoutTheToken(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(io.Discard)

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond) // past the client's timeout
	}))
	defer slow.Close()
	b := New(testToken, []int64{adminID}, newStubManager())
	b.api.base = slow.URL
	b.api.client.Timeout = 100 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	b.Run(ctx)
	out := buf.String()
	if !strings.Contains(out, "getUpdates") {
		t.Fatalf("the failure was not logged: %q", out)
	}
	if strings.Contains(out, "SECRET") {
		t.Fatalf("the log carries the token: %q", out)
	}
}

// ---- commands ----

func TestNonAdminIsIgnored(t *testing.T) {
	b, f, _ := newTestBot(t)
	b.handleUpdate(msg(otherID, "/list"))
	b.handleUpdate(press(otherID, "removeyes:x"))
	if f.count() != 0 {
		t.Fatalf("answered a non-admin: %+v", f.last())
	}
}

func TestAddWizardWithSeveralTransports(t *testing.T) {
	b, f, mgr := newTestBot(t)
	b.handleUpdate(msg(adminID, "/add"))
	b.handleUpdate(msg(adminID, "Alice"))
	if !f.hasButton("w:t=vyandex") || !f.hasButton("w:t=cupsonline") {
		t.Fatalf("no transport buttons: %+v", f.last())
	}
	b.handleUpdate(press(adminID, "w:t=vyandex"))
	if !strings.Contains(f.last().Text, "docs.yandex.ru/edit/d/") || f.hasButton("w:gen") {
		t.Fatalf("vyandex prompt = %+v: want the editor address asked for, no create button", f.last())
	}
	b.handleUpdate(msg(adminID, " https://docs.yandex.ru/edit/d/abcdefghijklmnopqrstuv "))
	b.handleUpdate(press(adminID, "w:t=yandex"))
	b.handleUpdate(msg(adminID, "https://disk.yandex.ru/i/typed"))
	b.handleUpdate(press(adminID, "w:t=cupsonline"))
	b.handleUpdate(press(adminID, "w:done"))
	b.handleUpdate(msg(adminID, "7443"))

	c := mgr.only(t)
	want := []exitmgr.TransportEntry{
		{Type: "vyandex", URL: "https://docs.yandex.ru/edit/d/abcdefghijklmnopqrstuv"},
		{Type: "yandex", URL: "https://disk.yandex.ru/i/typed"},
		{Type: "cupsonline"},
	}
	if c.Config.Name != "Alice" || c.Config.DirectPort != 7443 || fmt.Sprint(c.Config.Transports) != fmt.Sprint(want) {
		t.Fatalf("config = %+v", c.Config)
	}
	if !strings.Contains(f.last().Text, "Добавлено") {
		t.Fatalf("last message = %q", f.last().Text)
	}
}

func TestWizardRejectsBadPortAndTypedChoices(t *testing.T) {
	b, f, mgr := newTestBot(t)
	b.handleUpdate(msg(adminID, "/add"))
	b.handleUpdate(msg(adminID, "Bob"))
	b.handleUpdate(msg(adminID, "w:t=cupsonline")) // typed, not pressed: ignored
	b.handleUpdate(press(adminID, "w:t=avito"))
	b.handleUpdate(press(adminID, "w:done"))
	b.handleUpdate(msg(adminID, "99999"))
	if !strings.Contains(f.last().Text, "Прямое подключение") {
		t.Fatalf("a bad port was not asked again: %q", f.last().Text)
	}
	b.handleUpdate(press(adminID, "w:nodirect"))
	c := mgr.only(t)
	if len(c.Config.Transports) != 1 || c.Config.Transports[0].Type != "avito" || c.Config.DirectPort != 0 {
		t.Fatalf("config = %+v", c.Config)
	}
}

func TestEditKeepsWhatIsNotChanged(t *testing.T) {
	b, f, mgr := newTestBot(t)
	orig := exitmgr.ClientConfig{
		ID: "abc", Name: "Carol", Mode: "l3", DirectPort: 5000, Context: "ctx",
		Transports: []exitmgr.TransportEntry{{Type: "cupsonline", URL: "rooms-kept"}},
	}
	mgr.clients["abc"] = exitmgr.ClientStatus{Config: orig, Status: "running"}

	b.handleUpdate(msg(adminID, "/edit abc"))
	b.handleUpdate(msg(adminID, "."))
	if !f.hasButton("w:keep") {
		t.Fatal("no keep-transports button when editing")
	}
	b.handleUpdate(press(adminID, "w:keep"))
	if !f.hasButton("w:keepdirect") {
		t.Fatal("no keep-port button when editing")
	}
	b.handleUpdate(press(adminID, "w:keepdirect"))
	c, _ := mgr.Get("abc")
	if fmt.Sprint(c.Config) != fmt.Sprint(orig) {
		t.Fatalf("edit changed what it kept:\n got %+v\nwant %+v", c.Config, orig)
	}
}

func TestStatusSendsLinkAndQR(t *testing.T) {
	b, f, mgr := newTestBot(t)
	link, err := share.MakeLink(share.Config{Transports: []share.Transport{{Type: "yandex", URL: "https://disk.yandex.ru/i/x"}}})
	if err != nil {
		t.Fatal(err)
	}
	mgr.clients["abc"] = exitmgr.ClientStatus{
		Config: exitmgr.ClientConfig{ID: "abc", Name: "Dan", Transports: []exitmgr.TransportEntry{{Type: "yandex", URL: "u"}}},
		Status: "running", Connected: true, Active: []string{"yandex"}, Link: link, StartedAt: time.Now(),
	}
	b.handleUpdate(msg(adminID, "/status abc"))
	last := f.last()
	if !strings.Contains(last.Text, link) || !strings.Contains(last.Text, "на связи через yandex") {
		t.Fatalf("status = %q", last.Text)
	}
	if !f.hasButton("logs:abc") || !f.hasButton("remove:abc") {
		t.Fatal("no action buttons under the status")
	}
	if f.photoCount() != 1 {
		t.Fatal("no QR code sent")
	}

	b.handleUpdate(msg(adminID, "/list"))
	if !f.hasButton("status:abc") {
		t.Fatalf("list has no button for the client: %+v", f.last())
	}
}

func TestRemoveNeedsConfirmation(t *testing.T) {
	b, f, mgr := newTestBot(t)
	mgr.clients["abc"] = exitmgr.ClientStatus{Config: exitmgr.ClientConfig{ID: "abc"}}
	b.handleUpdate(msg(adminID, "/remove abc"))
	if _, ok := mgr.Get("abc"); !ok || !f.hasButton("removeyes:abc") {
		t.Fatal("removed without asking, or no confirm button")
	}
	b.handleUpdate(press(adminID, "removeyes:abc"))
	if _, ok := mgr.Get("abc"); ok {
		t.Fatal("not removed after confirming")
	}
}

func TestLogsKeepTheNewestLinesWithinALimit(t *testing.T) {
	b, f, mgr := newTestBot(t)
	mgr.clients["abc"] = exitmgr.ClientStatus{Config: exitmgr.ClientConfig{ID: "abc"}}
	for i := 0; i < 100; i++ {
		mgr.logs = append(mgr.logs, fmt.Sprintf("line %03d %s", i, strings.Repeat("x", 200)))
	}
	b.handleUpdate(msg(adminID, "/logs abc"))
	text := f.last().Text
	if len([]rune(text)) > 4096 || !strings.Contains(text, "line 099") || strings.Contains(text, "line 050") {
		t.Fatalf("logs message: %d chars, newest kept=%v", len([]rune(text)), strings.Contains(text, "line 099"))
	}
}

func TestParseAdminIDs(t *testing.T) {
	ids, err := ParseAdminIDs(" 1, 22 ,333,")
	if err != nil || fmt.Sprint(ids) != "[1 22 333]" {
		t.Fatalf("ids = %v, %v", ids, err)
	}
	for _, bad := range []string{"", " , ", "12,abc"} {
		if _, err := ParseAdminIDs(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
