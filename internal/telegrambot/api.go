// Package telegrambot is a minimal Telegram Bot API client and a small
// admin bot wired directly to exitmgr.Manager, so --role=exit-panel can be
// operated from a phone without an SSH tunnel to the web panel.
package telegrambot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"time"
)

// defaultAPIBase is the real Telegram Bot API; tests point api.base at a
// local httptest server instead.
const defaultAPIBase = "https://api.telegram.org"

type api struct {
	token  string
	base   string
	client *http.Client
}

func newAPI(token string) *api {
	return &api{
		token:  token,
		base:   defaultAPIBase,
		client: &http.Client{Timeout: 65 * time.Second},
	}
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func (a *api) call(method string, params map[string]any, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	url := fmt.Sprintf("%s/bot%s/%s", a.base, a.token, method)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var r apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !r.OK {
		return fmt.Errorf("telegram: %s", r.Description)
	}
	if out != nil {
		if err := json.Unmarshal(r.Result, out); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
	}
	return nil
}

// User is a Telegram user/chat participant.
type User struct {
	ID int64 `json:"id"`
}

// Chat is a Telegram chat (1:1 with the bot, for this bot's purposes).
type Chat struct {
	ID int64 `json:"id"`
}

// Message is the subset of Telegram's Message object this bot uses.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

// CallbackQuery is an inline-keyboard button press.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// Update is one item from getUpdates.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// getUpdates long-polls for new updates after offset (exclusive), waiting
// up to timeoutSec for at least one.
func (a *api) getUpdates(offset int64, timeoutSec int) ([]Update, error) {
	var out []Update
	err := a.call("getUpdates", map[string]any{
		"offset":  offset,
		"timeout": timeoutSec,
	}, &out)
	return out, err
}

// inlineKeyboardButton is one button in an inline keyboard row.
type inlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// inlineKeyboard is a grid of buttons attached to a message.
type inlineKeyboard struct {
	InlineKeyboard [][]inlineKeyboardButton `json:"inline_keyboard"`
}

func row(buttons ...inlineKeyboardButton) []inlineKeyboardButton { return buttons }

func button(text, data string) inlineKeyboardButton {
	return inlineKeyboardButton{Text: text, CallbackData: data}
}

// sendMessage sends text to chatID. kb may be nil for no keyboard.
func (a *api) sendMessage(chatID int64, text string, kb *inlineKeyboard) error {
	params := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if kb != nil {
		params["reply_markup"] = kb
	}
	return a.call("sendMessage", params, nil)
}

// sendPhoto uploads photo (raw image bytes) to chatID, with an optional
// caption. Unlike every other call here, Telegram requires this one as
// multipart/form-data rather than JSON since it carries a file.
func (a *api) sendPhoto(chatID int64, filename string, photo []byte, caption string) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("chat_id", fmt.Sprintf("%d", chatID)); err != nil {
		return err
	}
	if caption != "" {
		if err := w.WriteField("caption", caption); err != nil {
			return err
		}
	}
	fw, err := w.CreateFormFile("photo", filename)
	if err != nil {
		return err
	}
	if _, err := fw.Write(photo); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	url := fmt.Sprintf("%s/bot%s/sendPhoto", a.base, a.token)
	req, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var r apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !r.OK {
		return fmt.Errorf("telegram: %s", r.Description)
	}
	return nil
}

// answerCallbackQuery acknowledges a button press so the client stops
// showing its loading spinner. text, if non-empty, is a small popup/toast.
func (a *api) answerCallbackQuery(id, text string) error {
	params := map[string]any{"callback_query_id": id}
	if text != "" {
		params["text"] = text
	}
	return a.call("answerCallbackQuery", params, nil)
}
