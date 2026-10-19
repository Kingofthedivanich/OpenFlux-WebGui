package telegrambot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/p1neappleXpress/OpenFlux/exitmgr"
	"github.com/p1neappleXpress/OpenFlux/share"
)

// Manager is what the bot needs from exitmgr.Manager (the web panel uses
// the same calls).
type Manager interface {
	List() []exitmgr.ClientStatus
	Get(id string) (exitmgr.ClientStatus, bool)
	Logs(id string) ([]string, bool)
	Add(cfg exitmgr.ClientConfig) error
	Update(id string, cfg exitmgr.ClientConfig) error
	Remove(id string) error
}

// DocMaker creates vyandex documents with the token set in the web panel.
type DocMaker interface {
	YandexDocAvailable() bool
	CreateYandexDoc(ctx context.Context, name string) (string, error)
}

// Bot is a Telegram front end for the exit panel. Only user ids in adminIDs
// get an answer; everyone else is ignored without a word, so the bot does
// not even reveal that it does anything.
type Bot struct {
	api      *api
	mgr      Manager
	docs     DocMaker // may be nil
	adminIDs map[int64]bool

	mu      sync.Mutex
	wizards map[int64]*wizard
	offset  int64
}

// New builds a Bot. docs may be nil (no document button).
func New(token string, adminIDs []int64, mgr Manager, docs DocMaker) *Bot {
	ids := make(map[int64]bool, len(adminIDs))
	for _, id := range adminIDs {
		ids[id] = true
	}
	return &Bot{
		api:      newAPI(token),
		mgr:      mgr,
		docs:     docs,
		adminIDs: ids,
		wizards:  make(map[int64]*wizard),
	}
}

// ParseAdminIDs reads a comma-separated list of Telegram user ids.
func ParseAdminIDs(s string) ([]int64, error) {
	var ids []int64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("admin id %q is not a number", part)
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no admin ids")
	}
	return ids, nil
}

// Run long-polls for updates until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	log.Printf("[TGBOT] started, %d admin(s)", len(b.adminIDs))
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

const helpText = `OpenFlux — бот панели выходной ноды

/list — клиенты и их статус
/status <id> — статус клиента, ссылка и QR для приложения OpenFlux
/logs <id> — последние строки лога клиента
/add — добавить клиента (пошагово)
/edit <id> — изменить клиента
/remove <id> — удалить клиента
/cancel — прервать пошаговый ввод`

func (b *Bot) handleMessage(m Message) {
	if m.From == nil || !b.isAdmin(m.From.ID) {
		return
	}
	chatID := m.Chat.ID
	text := strings.TrimSpace(m.Text)

	if text == "/cancel" {
		b.setWizard(chatID, nil)
		b.send(chatID, "Отменено.")
		return
	}
	// A wizard in progress takes plain text as its next answer.
	if w := b.getWizard(chatID); w != nil && !strings.HasPrefix(text, "/") {
		b.advanceWizard(chatID, w, text, false)
		return
	}

	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	cmd, arg := fields[0], ""
	if i := strings.IndexByte(cmd, '@'); i > 0 {
		cmd = cmd[:i] // /list@SomeBot in a group
	}
	if len(fields) > 1 {
		arg = fields[1]
	}
	needID := func(usage string) bool {
		if arg == "" {
			b.send(chatID, "Использование: "+usage)
			return false
		}
		return true
	}

	switch cmd {
	case "/start", "/help":
		b.send(chatID, helpText)
	case "/list":
		b.sendList(chatID)
	case "/status":
		if needID("/status <id>") {
			b.sendStatus(chatID, arg)
		}
	case "/logs":
		if needID("/logs <id>") {
			b.sendLogs(chatID, arg)
		}
	case "/add":
		b.startWizard(chatID, nil)
	case "/edit":
		if needID("/edit <id>") {
			c, ok := b.mgr.Get(arg)
			if !ok {
				b.send(chatID, fmt.Sprintf("Клиент %q не найден.", arg))
				return
			}
			b.startWizard(chatID, &c.Config)
		}
	case "/remove":
		if needID("/remove <id>") {
			b.confirmRemove(chatID, arg)
		}
	default:
		b.send(chatID, "Неизвестная команда. /help — список команд.")
	}
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
	verb, arg, _ := strings.Cut(cq.Data, ":")

	switch verb {
	case "status":
		b.sendStatus(chatID, arg)
	case "logs":
		b.sendLogs(chatID, arg)
	case "edit":
		c, ok := b.mgr.Get(arg)
		if !ok {
			b.send(chatID, fmt.Sprintf("Клиент %q не найден.", arg))
			return
		}
		b.startWizard(chatID, &c.Config)
	case "remove":
		b.confirmRemove(chatID, arg)
	case "removeyes":
		if err := b.mgr.Remove(arg); err != nil {
			b.send(chatID, "Не удалось удалить: "+err.Error())
			return
		}
		b.send(chatID, "Удалено. Ссылка клиента больше не работает.")
	case "cancel":
		b.setWizard(chatID, nil)
		b.send(chatID, "Отменено.")
	case "w":
		if w := b.getWizard(chatID); w != nil {
			b.advanceWizard(chatID, w, arg, true)
		}
	}
}

func (b *Bot) send(chatID int64, text string) { b.sendKB(chatID, text, nil) }

func (b *Bot) sendKB(chatID int64, text string, kb *inlineKeyboard) {
	if err := b.api.sendMessage(chatID, text, kb); err != nil {
		log.Printf("[TGBOT] sendMessage: %v", err)
	}
}

// ---- /list, /status, /logs ----

func (b *Bot) sendList(chatID int64) {
	list := b.mgr.List()
	if len(list) == 0 {
		b.send(chatID, "Клиентов нет. /add — добавить.")
		return
	}
	var sb strings.Builder
	kb := &inlineKeyboard{}
	for _, c := range list {
		fmt.Fprintf(&sb, "%s %s — %s\n", statusDot(c), displayName(c.Config), transportsLine(c.Config))
		kb.InlineKeyboard = append(kb.InlineKeyboard, row(button(displayName(c.Config), "status:"+c.Config.ID)))
	}
	b.sendKB(chatID, sb.String(), kb)
}

func (b *Bot) sendStatus(chatID int64, id string) {
	c, ok := b.mgr.Get(id)
	if !ok {
		b.send(chatID, fmt.Sprintf("Клиент %q не найден.", id))
		return
	}
	kb := &inlineKeyboard{InlineKeyboard: [][]inlineKeyboardButton{
		row(button("Логи", "logs:"+id), button("Изменить", "edit:"+id), button("Удалить", "remove:"+id)),
	}}
	b.sendKB(chatID, formatStatus(c), kb)
	if c.Link == "" {
		return
	}
	png, err := share.PNG(c.Link, 512)
	if err != nil {
		log.Printf("[TGBOT] qr for %s: %v", id, err)
		return
	}
	caption := "Отсканируй в приложении OpenFlux: " + displayName(c.Config)
	if err := b.api.sendPhoto(chatID, "openflux.png", png, caption); err != nil {
		log.Printf("[TGBOT] sendPhoto: %v", err)
	}
}

func formatStatus(c exitmgr.ClientStatus) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s (id %s)\n", statusDot(c), displayName(c.Config), c.Config.ID)
	fmt.Fprintf(&sb, "Транспорты: %s · %s\n", transportsLine(c.Config), modeOf(c.Config))
	status := statusText[c.Status]
	if status == "" {
		status = c.Status
	}
	if c.Status == "running" {
		if c.Connected {
			status += ", клиент на связи"
			if len(c.Active) > 0 {
				status += " через " + strings.Join(c.Active, ", ")
			}
		} else {
			status += ", ждёт клиента"
		}
		fmt.Fprintf(&sb, "Статус: %s\nАптайм: %s · трафик ↑%s ↓%s\n", status,
			formatUptime(time.Since(c.StartedAt)), formatBytes(c.BytesOut), formatBytes(c.BytesIn))
	} else {
		fmt.Fprintf(&sb, "Статус: %s\n", status)
	}
	if c.Restarts > 0 {
		fmt.Fprintf(&sb, "Перезапусков: %d\n", c.Restarts)
	}
	if c.Check != nil {
		// The check opens only from the server's address and session, so
		// its URL is no use here: the app passes it through the tunnel.
		how := "дай клиенту прямой порт: приложение проходит проверку через туннель, по работающему каналу"
		if c.Config.DirectPort > 0 {
			how = "пройди её в приложении OpenFlux: оно подключится через прямой порт и покажет проверку"
		}
		fmt.Fprintf(&sb, "⚠️ %s: Яндекс просит у сервера проверку (%s) — %s\n", c.Check.Transport, c.Check.Reason, how)
	}
	if c.Error != "" {
		fmt.Fprintf(&sb, "Ошибка: %s\n", c.Error)
	}
	if c.Link != "" {
		fmt.Fprintf(&sb, "\nСсылка (в ней ключ — только этому клиенту):\n%s", c.Link)
	} else {
		sb.WriteString("\nСсылки пока нет: канал запускается или ещё создаёт комнаты.")
	}
	return sb.String()
}

func (b *Bot) sendLogs(chatID int64, id string) {
	lines, ok := b.mgr.Logs(id)
	if !ok {
		b.send(chatID, fmt.Sprintf("Клиент %q не найден.", id))
		return
	}
	if len(lines) > 25 {
		lines = lines[len(lines)-25:]
	}
	text := strings.Join(lines, "\n")
	// A message holds 4096 characters; keep the newest lines.
	if r := []rune(text); len(r) > 3800 {
		text = "…" + string(r[len(r)-3800:])
	}
	if text == "" {
		text = "(лог пуст)"
	}
	b.send(chatID, text)
}

var statusText = map[string]string{
	"starting":   "запускается",
	"running":    "работает",
	"restarting": "перезапуск",
	"stopped":    "остановлен",
	"error":      "ошибка",
}

func statusDot(c exitmgr.ClientStatus) string {
	switch {
	case c.Connected:
		return "🟢"
	case c.Status == "running" || c.Status == "starting":
		return "🟡"
	default:
		return "🔴"
	}
}

func displayName(cfg exitmgr.ClientConfig) string {
	if cfg.Name != "" {
		return cfg.Name
	}
	return cfg.ID
}

func modeOf(cfg exitmgr.ClientConfig) string {
	if cfg.Mode == "" {
		return "l4"
	}
	return cfg.Mode
}

var typeLabels = map[string]string{
	"vyandex":    "Yandex Docs (Volga)",
	"yandex":     "Yandex Docs",
	"mailru":     "Mail.ru Docs",
	"telemost":   "Telemost",
	"boards":     "Yandex Boards",
	"avito":      "Avito Code",
	"cupsonline": "Cups.online",
}

func typeLabel(t string) string {
	if l := typeLabels[t]; l != "" {
		return l
	}
	return t
}

func transportsLine(cfg exitmgr.ClientConfig) string {
	var parts []string
	for _, t := range cfg.Transports {
		parts = append(parts, typeLabel(t.Type))
	}
	if cfg.DirectPort > 0 {
		parts = append(parts, fmt.Sprintf("direct :%d", cfg.DirectPort))
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, ", ")
}

func formatUptime(d time.Duration) string {
	s := int64(d.Seconds())
	if s <= 0 {
		return "0с"
	}
	h, m := s/3600, (s%3600)/60
	switch {
	case h > 0:
		return fmt.Sprintf("%dч %dм", h, m)
	case m > 0:
		return fmt.Sprintf("%dм %dс", m, s%60)
	default:
		return fmt.Sprintf("%dс", s)
	}
}

func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d Б", n)
	}
	div, exp := uint64(unit), 0
	for n2 := n / unit; n2 >= unit && exp < 3; n2 /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), []string{"КБ", "МБ", "ГБ", "ТБ"}[exp])
}

// ---- /remove ----

func (b *Bot) confirmRemove(chatID int64, id string) {
	c, ok := b.mgr.Get(id)
	if !ok {
		b.send(chatID, fmt.Sprintf("Клиент %q не найден.", id))
		return
	}
	kb := &inlineKeyboard{InlineKeyboard: [][]inlineKeyboardButton{
		row(button("Да, удалить", "removeyes:"+id), button("Отмена", "cancel:")),
	}}
	b.sendKB(chatID, fmt.Sprintf("Удалить клиента «%s»? Его ключ удалится, ссылка перестанет работать.", displayName(c.Config)), kb)
}

// ---- add / edit wizard ----

type wizardStep int

const (
	stepName wizardStep = iota
	stepTransports
	stepURL
	stepDirect
)

type wizard struct {
	old     *exitmgr.ClientConfig // nil for /add
	step    wizardStep
	cfg     exitmgr.ClientConfig
	pending string // the type whose URL stepURL waits for
}

func (b *Bot) getWizard(chatID int64) *wizard {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.wizards[chatID]
}

func (b *Bot) setWizard(chatID int64, w *wizard) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if w == nil {
		delete(b.wizards, chatID)
	} else {
		b.wizards[chatID] = w
	}
}

func (b *Bot) startWizard(chatID int64, old *exitmgr.ClientConfig) {
	w := &wizard{old: old, step: stepName}
	if old != nil {
		w.cfg = *old
		w.cfg.Transports = nil
		b.setWizard(chatID, w)
		b.send(chatID, fmt.Sprintf("Изменение «%s». Пришли новое имя или «.», чтобы оставить.", displayName(*old)))
		return
	}
	b.setWizard(chatID, w)
	b.send(chatID, "Новый клиент. Пришли имя:")
}

// advanceWizard takes one answer: typed text, or a button's payload
// (fromButton) — buttons are only trusted for the choices they offer.
func (b *Bot) advanceWizard(chatID int64, w *wizard, input string, fromButton bool) {
	switch w.step {
	case stepName:
		if fromButton {
			return
		}
		if !(input == "." && w.old != nil) {
			w.cfg.Name = input
		}
		w.step = stepTransports
		b.promptTransports(chatID, w)

	case stepTransports:
		if !fromButton {
			b.promptTransports(chatID, w)
			return
		}
		switch {
		case input == "done":
			if len(w.cfg.Transports) == 0 && w.old == nil {
				b.send(chatID, "Нужен хотя бы один транспорт (или потом прямой порт): выбери тип.")
				b.promptTransports(chatID, w)
				return
			}
			w.step = stepDirect
			b.promptDirect(chatID, w)
		case input == "keep" && w.old != nil:
			w.cfg.Transports = append([]exitmgr.TransportEntry(nil), w.old.Transports...)
			w.step = stepDirect
			b.promptDirect(chatID, w)
		case strings.HasPrefix(input, "t="):
			typ := strings.TrimPrefix(input, "t=")
			if _, ok := typeLabels[typ]; !ok {
				return
			}
			if exitmgr.MakesOwnRooms(typ) {
				w.cfg.Transports = append(w.cfg.Transports, exitmgr.TransportEntry{Type: typ})
				b.promptTransports(chatID, w)
				return
			}
			w.pending = typ
			w.step = stepURL
			b.promptURL(chatID, w)
		}

	case stepURL:
		url := input
		if fromButton {
			if input != "gen" || b.docs == nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			made, err := b.docs.CreateYandexDoc(ctx, w.cfg.Name)
			cancel()
			if err != nil {
				b.send(chatID, "Не удалось создать документ: "+err.Error())
				b.promptURL(chatID, w)
				return
			}
			url = made
			b.send(chatID, "Документ создан: "+url)
		}
		w.cfg.Transports = append(w.cfg.Transports, exitmgr.TransportEntry{Type: w.pending, URL: strings.TrimSpace(url)})
		w.pending = ""
		w.step = stepTransports
		b.promptTransports(chatID, w)

	case stepDirect:
		switch {
		case fromButton && input == "nodirect":
			w.cfg.DirectPort = 0
		case fromButton && input == "keepdirect":
			// cfg already carries the old port
		case !fromButton:
			port, err := strconv.Atoi(input)
			if err != nil || port < 1 || port > 65535 {
				b.send(chatID, "Нужен номер порта 1–65535.")
				b.promptDirect(chatID, w)
				return
			}
			w.cfg.DirectPort = port
		default:
			return
		}
		b.finishWizard(chatID, w)
	}
}

func (b *Bot) promptTransports(chatID int64, w *wizard) {
	var text string
	if len(w.cfg.Transports) == 0 {
		text = "Выбери транспорт. Можно несколько: клиент переключается между ними, если один сервис заблокируют."
	} else {
		var names []string
		for _, t := range w.cfg.Transports {
			names = append(names, typeLabel(t.Type))
		}
		text = "Транспорты: " + strings.Join(names, ", ") + ". Добавь ещё или нажми «Готово»."
	}
	kb := &inlineKeyboard{}
	types := exitmgr.Types()
	for i := 0; i < len(types); i += 2 {
		r := row(button(typeLabel(types[i]), "w:t="+types[i]))
		if i+1 < len(types) {
			r = append(r, button(typeLabel(types[i+1]), "w:t="+types[i+1]))
		}
		kb.InlineKeyboard = append(kb.InlineKeyboard, r)
	}
	last := row(button("Готово", "w:done"))
	if w.old != nil && len(w.cfg.Transports) == 0 {
		last = append(last, button(fmt.Sprintf("Оставить как было (%d)", len(w.old.Transports)), "w:keep"))
	}
	kb.InlineKeyboard = append(kb.InlineKeyboard, last, row(button("Отмена", "cancel:")))
	b.sendKB(chatID, text, kb)
}

func (b *Bot) promptURL(chatID int64, w *wizard) {
	text := fmt.Sprintf("Пришли ссылку для %s:", typeLabel(w.pending))
	var kb *inlineKeyboard
	if w.pending == "vyandex" && b.docs != nil && b.docs.YandexDocAvailable() {
		kb = &inlineKeyboard{InlineKeyboard: [][]inlineKeyboardButton{row(button("Создать документ на Яндекс Диске", "w:gen"))}}
	}
	b.sendKB(chatID, text, kb)
}

func (b *Bot) promptDirect(chatID int64, w *wizard) {
	kb := &inlineKeyboard{InlineKeyboard: [][]inlineKeyboardButton{row(button("Без прямого порта", "w:nodirect"))}}
	if w.old != nil && w.old.DirectPort > 0 {
		kb.InlineKeyboard[0] = append(kb.InlineKeyboard[0], button(fmt.Sprintf("Оставить %d", w.old.DirectPort), "w:keepdirect"))
	}
	b.sendKB(chatID, "Прямое подключение (рекомендуется): пришли TCP-порт и открой его в фаерволе. Это резерв, и через него приложение проходит капчу Яндекса, если её спросят у сервера.", kb)
}

func (b *Bot) finishWizard(chatID int64, w *wizard) {
	b.setWizard(chatID, nil)
	if w.old != nil {
		if err := b.mgr.Update(w.old.ID, w.cfg); err != nil {
			b.send(chatID, "Не удалось сохранить: "+err.Error())
			return
		}
		b.send(chatID, "Сохранено. Ссылка появится, когда канал поднимется: /status "+w.old.ID)
		return
	}
	w.cfg.ID = generateID()
	if err := b.mgr.Add(w.cfg); err != nil {
		b.send(chatID, "Не удалось добавить: "+err.Error())
		return
	}
	b.send(chatID, "Добавлено. Ссылка появится, когда канал поднимется: /status "+w.cfg.ID)
}

// generateID makes a client id like the web panel does (6 random bytes, hex).
func generateID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
