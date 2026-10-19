// Package telegrambot is a minimal Telegram Bot API client and a small admin
// bot for the exit panel (--role=exit-panel): list, add, edit and remove
// clients and get a client's link and QR code from a phone, without an SSH
// tunnel to the web panel.
package telegrambot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultAPIBase is the real Bot API; tests point api.base at a local server.
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
	return a.post(method, "application/json", bytes.NewReader(body), out)
}

// post sends one Bot API request. The token is part of the URL, and
// net/http puts the URL into every transport error it returns ("Post
// \"https://api.telegram.org/bot<token>/getUpdates\": context deadline
// exceeded"), so such errors are rebuilt without it: whatever logs them
// must not leak the token.
func (a *api) post(method, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequest(http.MethodPost, a.base+"/bot"+a.token+"/"+method, body)
	if err != nil {
		return a.redact(err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := a.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("%s %s: %w", strings.ToUpper(ue.Op), method, a.redact(ue.Err))
		}
		return a.redact(err)
	}
	defer resp.Body.Close()

	var r apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("decode %s response (HTTP %d): %w", method, resp.StatusCode, err)
	}
	if !r.OK {
		return fmt.Errorf("telegram: %s", r.Description)
	}
	if out != nil {
		if err := json.Unmarshal(r.Result, out); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
	}
	return nil
}

// redact strips the token from an error that might still carry it.
func (a *api) redact(err error) error {
	if err == nil || a.token == "" || !strings.Contains(err.Error(), a.token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), a.token, "<token>"))
}

// User is a Telegram user.
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

// getUpdates long-polls for updates after offset (exclusive), waiting up to
// timeoutSec for at least one.
func (a *api) getUpdates(offset int64, timeoutSec int) ([]Update, error) {
	var out []Update
	err := a.call("getUpdates", map[string]any{"offset": offset, "timeout": timeoutSec}, &out)
	return out, err
}

type inlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type inlineKeyboard struct {
	InlineKeyboard [][]inlineKeyboardButton `json:"inline_keyboard"`
}

func row(buttons ...inlineKeyboardButton) []inlineKeyboardButton { return buttons }

func button(text, data string) inlineKeyboardButton {
	return inlineKeyboardButton{Text: text, CallbackData: data}
}

// sendMessage sends text to chatID. kb may be nil.
func (a *api) sendMessage(chatID int64, text string, kb *inlineKeyboard) error {
	params := map[string]any{"chat_id": chatID, "text": text}
	if kb != nil {
		params["reply_markup"] = kb
	}
	return a.call("sendMessage", params, nil)
}

// sendPhoto uploads photo (image bytes) to chatID with an optional caption.
// A file goes as multipart/form-data, not JSON.
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
	return a.post("sendPhoto", w.FormDataContentType(), &body, nil)
}

// answerCallbackQuery acknowledges a button press so the client stops its
// spinner. text, if set, is shown as a toast.
func (a *api) answerCallbackQuery(id, text string) error {
	params := map[string]any{"callback_query_id": id}
	if text != "" {
		params["text"] = text
	}
	return a.call("answerCallbackQuery", params, nil)
}
