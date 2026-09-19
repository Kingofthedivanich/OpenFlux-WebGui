package telegrambot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"openflux/exitmgr"
)

// Bot is a Telegram front end for exitmgr.Manager: full parity with the web
// panel (list/add/edit/remove/status/key), operable from a phone without an
// SSH tunnel. Only chat ids in adminIDs get a response; everyone else is
// silently ignored (no information leak about the bot's existence).
type Bot struct {
	api            *api
	mgr            *exitmgr.Manager
	adminIDs       map[int64]bool
	panelPublicKey string

	mu      sync.Mutex
	wizards map[int64]*wizard
	offset  int64
}

// New builds a Bot. adminIDs is the whitelist of Telegram user ids allowed
// to use it; panelPublicKey is shown by /key (the same value the web panel
// prints at startup and shows in its dashboard).
func New(token string, adminIDs []int64, mgr *exitmgr.Manager, panelPublicKey string) *Bot {
	ids := make(map[int64]bool, len(adminIDs))
	for _, id := range adminIDs {
		ids[id] = true
	}
	return &Bot{
		api:            newAPI(token),
		mgr:            mgr,
		adminIDs:       ids,
		panelPublicKey: panelPublicKey,
		wizards:        make(map[int64]*wizard),
	}
}

// Run long-polls for updates until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	log.Printf("[TGBOT] started, %d admin(s) whitelisted", len(b.adminIDs))
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		updates, err := b.api.getUpdates(b.offset+1, 30)
		if err != nil {
			log.Printf("[TGBOT] getUpdates: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			b.offset = u.UpdateID
			b.handleUpdate(u)
		}
	}
}

func (b *Bot) handleUpdate(u Update) {
	switch {
	case u.CallbackQuery != nil:
		b.handleCallback(*u.CallbackQuery)
	case u.Message != nil:
		b.handleMessage(*u.Message)
	}
}

func (b *Bot) isAdmin(userID int64) bool { return b.adminIDs[userID] }

func (b *Bot) handleMessage(m Message) {
	if m.From == nil || !b.isAdmin(m.From.ID) {
		return // silently ignore: no hint to non-admins that this bot does anything
	}
	chatID := m.Chat.ID
	text := strings.TrimSpace(m.Text)

	// A wizard in progress consumes plain text as its next answer, unless
	// the user explicitly bails with /cancel.
	if text == "/cancel" {
		b.mu.Lock()
		delete(b.wizards, chatID)
		b.mu.Unlock()
		b.send(chatID, "Отменено.")
		return
	}
	b.mu.Lock()
	w := b.wizards[chatID]
	b.mu.Unlock()
	if w != nil && !strings.HasPrefix(text, "/") {
		b.advanceWizard(chatID, w, text)
		return
	}

	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	cmd := fields[0]
	arg := ""
	if len(fields) > 1 {
		arg = fields[1]
	}

	switch cmd {
	case "/start", "/help":
		b.send(chatID, helpText)
	case "/key":
		b.send(chatID, fmt.Sprintf("Публичный ключ панели (--peer-key):\n%s", b.panelPublicKey))
	case "/list":
		b.send(chatID, b.formatList())
	case "/status":
		if arg == "" {
			b.send(chatID, "Использование: /status <id>")
			return
		}
		b.send(chatID, b.formatStatus(arg))
	case "/add":
		b.startAddWizard(chatID)
	case "/edit":
		if arg == "" {
			b.send(chatID, "Использование: /edit <id>")
			return
		}
		b.startEditWizard(chatID, arg)
	case "/remove":
		if arg == "" {
			b.send(chatID, "Использование: /remove <id>")
			return
		}
		b.confirmRemove(chatID, arg)
	default:
		b.send(chatID, "Неизвестная команда. /help — список команд.")
	}
}

const helpText = `OpenFlux admin bot

/list — список клиентов и их статус
/status <id> — подробный статус одного клиента
/key — публичный ключ панели (--peer-key)
/add — добавить клиента (пошагово)
/edit <id> — изменить клиента (пошагово)
/remove <id> — удалить клиента (с подтверждением)
/cancel — отменить текущий пошаговый ввод`

func (b *Bot) send(chatID int64, text string) {
	if err := b.api.sendMessage(chatID, text, nil); err != nil {
		log.Printf("[TGBOT] sendMessage: %v", err)
	}
}

func (b *Bot) sendKB(chatID int64, text string, kb *inlineKeyboard) {
	if err := b.api.sendMessage(chatID, text, kb); err != nil {
		log.Printf("[TGBOT] sendMessage: %v", err)
	}
}

// ---- /list, /status ----

func (b *Bot) formatList() string {
	list := b.mgr.List()
	if len(list) == 0 {
		return "Нет ни одного клиента. /add — добавить."
	}
	var sb strings.Builder
	for _, c := range list {
		dot := "🟢"
		if c.Status != "running" {
			dot = "🔴"
		}
		name := c.Config.Name
		if name == "" {
			name = c.Config.ID
		}
		fmt.Fprintf(&sb, "%s %s — %s (id: %s)\n", dot, name, transportLabel(c.Config.Transport), c.Config.ID)
	}
	return sb.String()
}

func (b *Bot) formatStatus(id string) string {
	c, ok := b.mgr.Get(id)
	if !ok {
		return fmt.Sprintf("Клиент %q не найден.", id)
	}
	name := c.Config.Name
	if name == "" {
		name = c.Config.ID
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (id: %s)\n", name, c.Config.ID)
	fmt.Fprintf(&sb, "Транспорт: %s\n", transportLabel(c.Config.Transport))
	fmt.Fprintf(&sb, "Статус: %s\n", c.Status)
	if c.Error != "" {
		fmt.Fprintf(&sb, "Ошибка: %s\n", c.Error)
	}
	if c.Config.PSKFile != "" {
		fmt.Fprintf(&sb, "🔒 PSK: %s\n", c.Config.PSKFile)
	}
	if docs := strings.Split(c.Config.URL, ","); c.Config.URL != "" && len(docs) > 1 {
		fmt.Fprintf(&sb, "Multi-stream: %d документов\n", len(docs))
	}
	if c.Stats != nil {
		fmt.Fprintf(&sb, "Аптайм: %s · активных: %d · ретрансм.: %d\n",
			formatUptime(c.Stats.UptimeSeconds), c.Stats.Established, c.Stats.Retransmits)
	}
	fmt.Fprintf(&sb, "Трафик: ↑%s ↓%s\n", formatBytes(c.BytesSent), formatBytes(c.BytesReceived))
	if c.CupsonlineRooms != "" {
		fmt.Fprintf(&sb, "URL для клиента (--url): %s\n", c.CupsonlineRooms)
	}
	return sb.String()
}

func transportLabel(t string) string {
	switch t {
	case "yandex":
		return "Yandex.Docs"
	case "vyandex":
		return "Yandex.Docs (Volga)"
	case "oneme":
		return "MAX Messenger"
	case "cupsonline":
		return "Cups.online"
	case "mailru":
		return "Mail.ru Docs"
	default:
		return t
	}
}

func formatUptime(seconds int64) string {
	if seconds <= 0 {
		return "0с"
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%dч %dм", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dм %dс", m, s)
	}
	return fmt.Sprintf("%dс", s)
}

func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d Б", n)
	}
	div, exp := uint64(unit), 0
	for n2 := n / unit; n2 >= unit; n2 /= unit {
		div *= unit
		exp++
	}
	units := []string{"КБ", "МБ", "ГБ"}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), units[exp])
}

// ---- /remove ----

func (b *Bot) confirmRemove(chatID int64, id string) {
	c, ok := b.mgr.Get(id)
	if !ok {
		b.send(chatID, fmt.Sprintf("Клиент %q не найден.", id))
		return
	}
	name := c.Config.Name
	if name == "" {
		name = id
	}
	kb := &inlineKeyboard{InlineKeyboard: [][]inlineKeyboardButton{
		row(button("Да, удалить", "remove:yes:"+id), button("Отмена", "remove:no")),
	}}
	b.sendKB(chatID, fmt.Sprintf("Удалить клиента «%s»?", name), kb)
}

func (b *Bot) handleCallback(cq CallbackQuery) {
	if !b.isAdmin(cq.From.ID) {
		return
	}
	_ = b.api.answerCallbackQuery(cq.ID, "")
	if cq.Message == nil {
		return
	}
	chatID := cq.Message.Chat.ID
	data := cq.Data

	switch {
	case data == "remove:no":
		b.send(chatID, "Отменено.")
	case strings.HasPrefix(data, "remove:yes:"):
		id := strings.TrimPrefix(data, "remove:yes:")
		if err := b.mgr.RemoveClient(id); err != nil {
			b.send(chatID, "Не удалось удалить: "+err.Error())
			return
		}
		b.send(chatID, "Удалено.")
	case strings.HasPrefix(data, "wizard:"):
		b.mu.Lock()
		w := b.wizards[chatID]
		b.mu.Unlock()
		if w == nil {
			return
		}
		b.advanceWizard(chatID, w, strings.TrimPrefix(data, "wizard:"))
	}
}

// ---- add/edit wizard ----

type wizardStep int

const (
	stepTransport wizardStep = iota
	stepURL
	stepMaxToken
	stepMaxUid
	stepName
	stepCodec
	stepPSK
)

type wizard struct {
	editID string // "" for /add, target client id for /edit
	step   wizardStep
	cfg    exitmgr.ClientConfig
}

func (b *Bot) startAddWizard(chatID int64) {
	w := &wizard{step: stepTransport}
	b.mu.Lock()
	b.wizards[chatID] = w
	b.mu.Unlock()
	b.promptTransport(chatID)
}

func (b *Bot) startEditWizard(chatID int64, id string) {
	c, ok := b.mgr.Get(id)
	if !ok {
		b.send(chatID, fmt.Sprintf("Клиент %q не найден.", id))
		return
	}
	w := &wizard{editID: id, step: stepTransport, cfg: c.Config}
	b.mu.Lock()
	b.wizards[chatID] = w
	b.mu.Unlock()
	b.send(chatID, fmt.Sprintf("Редактирование «%s». Текущие значения можно оставить: отправь \".\" чтобы пропустить шаг.", c.Config.Name))
	b.promptTransport(chatID)
}

func (b *Bot) promptTransport(chatID int64) {
	kb := &inlineKeyboard{InlineKeyboard: [][]inlineKeyboardButton{
		row(button("Yandex.Docs", "wizard:transport:yandex"), button("Yandex.Docs (Volga)", "wizard:transport:vyandex")),
		row(button("MAX Messenger", "wizard:transport:oneme"), button("Cups.online", "wizard:transport:cupsonline")),
		row(button("Mail.ru Docs", "wizard:transport:mailru")),
	}}
	b.sendKB(chatID, "Выбери транспорт:", kb)
}

// advanceWizard consumes one answer (text message or button payload) and
// moves to the next step, or finishes and calls AddClient/UpdateClient.
func (b *Bot) advanceWizard(chatID int64, w *wizard, input string) {
	switch w.step {
	case stepTransport:
		t := strings.TrimPrefix(input, "transport:")
		if !validTransport(t) {
			b.send(chatID, "Неизвестный транспорт.")
			return
		}
		w.cfg.Transport = t
		if t == "oneme" {
			w.step = stepMaxToken
			b.send(chatID, "Пришли MAX Access Token:")
		} else if t == "cupsonline" {
			w.cfg.URL = ""
			w.step = stepName
			b.send(chatID, "Комнаты создаются автоматически. Пришли имя клиента:")
		} else {
			w.step = stepURL
			hint := ""
			if t == "yandex" || t == "vyandex" {
				hint = " (несколько через запятую — multi-stream)"
			}
			b.send(chatID, "Пришли URL документа"+hint+":")
		}

	case stepURL:
		if !(input == "." && w.editID != "") {
			w.cfg.URL = input
		}
		w.step = stepName
		b.send(chatID, "Пришли имя клиента:")

	case stepMaxToken:
		if !(input == "." && w.editID != "") {
			w.cfg.MaxToken = input
		}
		w.step = stepMaxUid
		b.send(chatID, "Пришли MAX User ID:")

	case stepMaxUid:
		if !(input == "." && w.editID != "") {
			w.cfg.MaxUid = input
		}
		w.step = stepName
		b.send(chatID, "Пришли имя клиента:")

	case stepName:
		if !(input == "." && w.editID != "") {
			w.cfg.Name = input
		}
		w.step = stepCodec
		kb := &inlineKeyboard{InlineKeyboard: [][]inlineKeyboardButton{
			row(button("batched (по умолчанию)", "wizard:codec:"), button("legacy", "wizard:codec:legacy")),
		}}
		b.sendKB(chatID, "Кодек:", kb)

	case stepCodec:
		w.cfg.Codec = strings.TrimPrefix(input, "codec:")
		w.step = stepPSK
		kb := &inlineKeyboard{InlineKeyboard: [][]inlineKeyboardButton{
			row(button("Без PSK", "wizard:psk:")),
		}}
		b.sendKB(chatID, "Путь к файлу общего секрета PSK (необязательно), или нажми «Без PSK»:", kb)

	case stepPSK:
		if strings.HasPrefix(input, "psk:") {
			w.cfg.PSKFile = strings.TrimPrefix(input, "psk:")
		} else if !(input == "." && w.editID != "") {
			w.cfg.PSKFile = input
		}
		b.finishWizard(chatID, w)
		return
	}

	b.mu.Lock()
	b.wizards[chatID] = w
	b.mu.Unlock()
}

func (b *Bot) finishWizard(chatID int64, w *wizard) {
	b.mu.Lock()
	delete(b.wizards, chatID)
	b.mu.Unlock()

	if w.editID != "" {
		if err := b.mgr.UpdateClient(w.editID, w.cfg); err != nil {
			b.send(chatID, "Ошибка обновления: "+err.Error())
			return
		}
		b.send(chatID, "Сохранено.\n\n"+b.formatStatus(w.editID))
		return
	}

	if w.cfg.ID == "" {
		w.cfg.ID = generateID()
	}
	if err := b.mgr.AddClient(w.cfg); err != nil {
		b.send(chatID, "Ошибка добавления (клиент зарегистрирован с ошибкой): "+err.Error())
		return
	}
	b.send(chatID, "Добавлено.\n\n"+b.formatStatus(w.cfg.ID))
}

func validTransport(t string) bool {
	switch t {
	case "yandex", "vyandex", "oneme", "cupsonline", "mailru":
		return true
	default:
		return false
	}
}

// generateID mirrors panel.generateID (6 random bytes, hex).
func generateID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
