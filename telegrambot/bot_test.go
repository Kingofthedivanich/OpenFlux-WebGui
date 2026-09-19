package telegrambot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"openflux/exitmgr"
	"openflux/transport"
)

// ---- fake Telegram Bot API server ----

type sentMessage struct {
	ChatID int64
	Text   string
	KB     *inlineKeyboard
}

type fakeTelegram struct {
	mu   sync.Mutex
	sent []sentMessage
	srv  *httptest.Server
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ChatID      int64           `json:"chat_id"`
			Text        string          `json:"text"`
			ReplyMarkup *inlineKeyboard `json:"reply_markup"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			f.mu.Lock()
			f.sent = append(f.sent, sentMessage{ChatID: body.ChatID, Text: body.Text, KB: body.ReplyMarkup})
			f.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
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

func (f *fakeTelegram) all() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sentMessage, len(f.sent))
	copy(out, f.sent)
	return out
}

func (f *fakeTelegram) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// ---- test fixtures ----

type stubTransport struct{}

func (stubTransport) Start() error                    { return nil }
func (stubTransport) Stop() error                     { return nil }
func (stubTransport) Send(data []byte) error          { return nil }
func (stubTransport) Receive(cb func([]byte))         {}
func (stubTransport) IsConnected() bool               { return true }
func (stubTransport) Stats() transport.TransportStats { return transport.TransportStats{} }

func stubBuilder(exitmgr.ClientConfig, transport.TransportConfig) (transport.Transport, error) {
	return stubTransport{}, nil
}

const adminID = int64(1001)
const otherID = int64(2002)

func newTestBot(t *testing.T) (*Bot, *fakeTelegram) {
	t.Helper()
	ft := newFakeTelegram(t)
	mgr := exitmgr.NewManagerWithBuilder(nil, stubBuilder)
	b := New("test-token", []int64{adminID}, mgr, "panel-pub-key-abc")
	b.api.base = ft.srv.URL
	return b, ft
}

func msg(from int64, text string) Message {
	return Message{From: &User{ID: from}, Chat: Chat{ID: from}, Text: text}
}

func cb(from int64, chatID int64, data string) CallbackQuery {
	return CallbackQuery{ID: "cq1", From: User{ID: from}, Message: &Message{Chat: Chat{ID: chatID}}, Data: data}
}

// ---- tests ----

func TestNonAdminIsIgnored(t *testing.T) {
	b, ft := newTestBot(t)
	b.handleMessage(msg(otherID, "/list"))
	if ft.count() != 0 {
		t.Fatalf("bot replied to a non-admin: %+v", ft.all())
	}
}

func TestHelpCommand(t *testing.T) {
	b, ft := newTestBot(t)
	b.handleMessage(msg(adminID, "/start"))
	if got := ft.last().Text; !strings.Contains(got, "/list") {
		t.Fatalf("help text = %q, want it to mention /list", got)
	}
}

func TestKeyCommand(t *testing.T) {
	b, ft := newTestBot(t)
	b.handleMessage(msg(adminID, "/key"))
	if got := ft.last().Text; !strings.Contains(got, "panel-pub-key-abc") {
		t.Fatalf("/key reply = %q, want it to contain the public key", got)
	}
}

func TestListEmptyThenWithClient(t *testing.T) {
	b, ft := newTestBot(t)
	b.handleMessage(msg(adminID, "/list"))
	if got := ft.last().Text; !strings.Contains(got, "Нет ни одного клиента") {
		t.Fatalf("/list on empty manager = %q", got)
	}

	if err := b.mgr.AddClient(exitmgr.ClientConfig{ID: "c1", Name: "Alice", Transport: "yandex", URL: "https://disk.yandex.com/i/x"}); err != nil {
		t.Fatalf("AddClient: %v", err)
	}
	b.handleMessage(msg(adminID, "/list"))
	if got := ft.last().Text; !strings.Contains(got, "Alice") || !strings.Contains(got, "c1") {
		t.Fatalf("/list with a client = %q, want it to mention Alice and c1", got)
	}
}

func TestStatusUnknownID(t *testing.T) {
	b, ft := newTestBot(t)
	b.handleMessage(msg(adminID, "/status nope"))
	if got := ft.last().Text; !strings.Contains(got, "не найден") {
		t.Fatalf("/status on unknown id = %q, want a not-found message", got)
	}
}

// TestAddWizardYandexFlow drives the full /add conversation for a yandex
// client: transport button, URL text, name text, codec button, PSK skip
// button -- exactly what a phone user taps/types.
func TestAddWizardYandexFlow(t *testing.T) {
	b, ft := newTestBot(t)

	b.handleMessage(msg(adminID, "/add"))
	if got := ft.last().Text; !strings.Contains(got, "транспорт") {
		t.Fatalf("after /add = %q, want a transport prompt", got)
	}

	b.handleCallback(cb(adminID, adminID, "wizard:transport:yandex"))
	if got := ft.last().Text; !strings.Contains(got, "URL") {
		t.Fatalf("after transport=yandex = %q, want a URL prompt", got)
	}

	b.handleMessage(msg(adminID, "https://disk.yandex.com/i/newclient"))
	if got := ft.last().Text; !strings.Contains(got, "имя") {
		t.Fatalf("after URL = %q, want a name prompt", got)
	}

	b.handleMessage(msg(adminID, "New Client"))
	if got := ft.last().Text; !strings.Contains(got, "одек") {
		t.Fatalf("after name = %q, want a codec prompt", got)
	}

	b.handleCallback(cb(adminID, adminID, "wizard:codec:"))
	if got := ft.last().Text; !strings.Contains(got, "PSK") {
		t.Fatalf("after codec = %q, want a PSK prompt", got)
	}

	b.handleCallback(cb(adminID, adminID, "wizard:psk:"))

	list := b.mgr.List()
	if len(list) != 1 {
		t.Fatalf("clients registered = %d, want 1", len(list))
	}
	cfg := list[0].Config
	if cfg.Name != "New Client" || cfg.Transport != "yandex" || cfg.URL != "https://disk.yandex.com/i/newclient" || cfg.PSKFile != "" {
		t.Fatalf("registered config = %+v, want name/transport/url set and no PSK", cfg)
	}
	if got := ft.last().Text; !strings.Contains(got, "Добавлено") {
		t.Fatalf("final message = %q, want a success confirmation", got)
	}
}

// oneme has no URL; the wizard must ask for MAX token/uid instead.
func TestAddWizardOnemeAsksForTokenAndUid(t *testing.T) {
	b, ft := newTestBot(t)

	b.handleMessage(msg(adminID, "/add"))
	b.handleCallback(cb(adminID, adminID, "wizard:transport:oneme"))
	if got := ft.last().Text; !strings.Contains(got, "Token") {
		t.Fatalf("after transport=oneme = %q, want a MAX token prompt", got)
	}
	b.handleMessage(msg(adminID, "tok123"))
	if got := ft.last().Text; !strings.Contains(got, "User ID") {
		t.Fatalf("after token = %q, want a MAX user id prompt", got)
	}
	b.handleMessage(msg(adminID, "42"))
	if got := ft.last().Text; !strings.Contains(got, "имя") {
		t.Fatalf("after uid = %q, want a name prompt", got)
	}
	b.handleMessage(msg(adminID, "MAX Client"))
	b.handleCallback(cb(adminID, adminID, "wizard:codec:"))
	b.handleCallback(cb(adminID, adminID, "wizard:psk:"))

	list := b.mgr.List()
	if len(list) != 1 || list[0].Config.MaxToken != "tok123" || list[0].Config.MaxUid != "42" {
		t.Fatalf("clients = %+v, want one oneme client with token/uid set", list)
	}
}

// cupsonline needs no URL prompt at all -- the exit generates its own rooms.
func TestAddWizardCupsonlineSkipsURLPrompt(t *testing.T) {
	b, ft := newTestBot(t)

	b.handleMessage(msg(adminID, "/add"))
	b.handleCallback(cb(adminID, adminID, "wizard:transport:cupsonline"))
	if got := ft.last().Text; strings.Contains(got, "URL") {
		t.Fatalf("cupsonline must not prompt for a URL, got %q", got)
	}
	if got := ft.last().Text; !strings.Contains(got, "имя") {
		t.Fatalf("after transport=cupsonline = %q, want it to go straight to the name prompt", got)
	}
}

func TestRemoveConfirmFlow(t *testing.T) {
	b, ft := newTestBot(t)
	if err := b.mgr.AddClient(exitmgr.ClientConfig{ID: "c1", Name: "Bob", Transport: "yandex", URL: "https://x"}); err != nil {
		t.Fatalf("AddClient: %v", err)
	}

	b.handleMessage(msg(adminID, "/remove c1"))
	last := ft.last()
	if !strings.Contains(last.Text, "Bob") || last.KB == nil {
		t.Fatalf("/remove prompt = %+v, want a confirmation with Bob's name and buttons", last)
	}

	b.handleCallback(cb(adminID, adminID, "remove:no"))
	if _, ok := b.mgr.Get("c1"); !ok {
		t.Fatal("client was removed despite answering 'no'")
	}

	b.handleMessage(msg(adminID, "/remove c1"))
	b.handleCallback(cb(adminID, adminID, "remove:yes:c1"))
	if _, ok := b.mgr.Get("c1"); ok {
		t.Fatal("client still present after confirming removal")
	}
}

func TestEditWizardSkipKeepsExistingValue(t *testing.T) {
	b, ft := newTestBot(t)
	if err := b.mgr.AddClient(exitmgr.ClientConfig{ID: "c1", Name: "Carol", Transport: "yandex", URL: "https://old"}); err != nil {
		t.Fatalf("AddClient: %v", err)
	}

	b.handleMessage(msg(adminID, "/edit c1"))
	b.handleCallback(cb(adminID, adminID, "wizard:transport:yandex"))
	b.handleMessage(msg(adminID, ".")) // skip URL, keep "https://old"
	b.handleMessage(msg(adminID, "Carol Renamed"))
	b.handleCallback(cb(adminID, adminID, "wizard:codec:"))
	b.handleCallback(cb(adminID, adminID, "wizard:psk:"))

	status, ok := b.mgr.Get("c1")
	if !ok {
		t.Fatal("client missing after edit")
	}
	if status.Config.URL != "https://old" {
		t.Fatalf("URL = %q, want it unchanged (skipped with \".\")", status.Config.URL)
	}
	if status.Config.Name != "Carol Renamed" {
		t.Fatalf("Name = %q, want the updated name", status.Config.Name)
	}
	_ = ft
}

func TestCancelDuringWizardDropsIt(t *testing.T) {
	b, ft := newTestBot(t)
	b.handleMessage(msg(adminID, "/add"))
	b.handleMessage(msg(adminID, "/cancel"))
	if got := ft.last().Text; got != "Отменено." {
		t.Fatalf("after /cancel = %q, want the cancellation ack", got)
	}
	if len(b.mgr.List()) != 0 {
		t.Fatal("a client was registered despite cancelling the wizard")
	}

	// A fresh /add after cancelling must start clean, not resume old state.
	b.handleMessage(msg(adminID, "/add"))
	if got := ft.last().Text; !strings.Contains(got, "транспорт") {
		t.Fatalf("/add after cancel = %q, want a fresh transport prompt", got)
	}
}
