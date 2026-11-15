package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
)

// Telegram: approval requests (and alerts) as messages from a bot. With
// buttons enabled, ordinary action approvals get Approve / Deny / Always
// buttons; anything that needs its own OK every time (Proxmox reboots and
// other ask-always actions) and requests for new rules only get a link to the
// approval page, which asks for the code. Button presses are only accepted
// from the configured chat (your own Telegram account). The hub fetches
// button presses itself (long polling), so nothing new is exposed.

type TelegramConfig struct {
	// BotTokenFile holds the bot token (root-only file).
	BotTokenFile string `yaml:"bot_token_file,omitempty"`
	// ChatID is your own chat with the bot (a private chat: same as your user id).
	ChatID int64 `yaml:"chat_id,omitempty"`
	// Buttons enables Approve/Deny/Always buttons. Tapping one approves
	// without the dashboard code, like a remembered device.
	Buttons bool `yaml:"buttons,omitempty"`
}

var telegramAPI = "https://api.telegram.org"

type telegram struct {
	h      *Hub
	cfg    TelegramConfig
	token  string
	client *http.Client

	mu   sync.Mutex
	msgs map[string]int64 // approval ID -> message ID, to update the message
}

func newTelegram(h *Hub, cfg TelegramConfig) (*telegram, error) {
	if cfg.BotTokenFile == "" {
		return nil, nil
	}
	b, err := os.ReadFile(cfg.BotTokenFile)
	if err != nil {
		return nil, fmt.Errorf("notify.telegram.bot_token_file: %w", err)
	}
	if cfg.ChatID == 0 {
		return nil, errors.New("notify.telegram.chat_id is required")
	}
	return &telegram{h: h, cfg: cfg, token: strings.TrimSpace(string(b)), client: &http.Client{Timeout: 70 * time.Second}, msgs: map[string]int64{}}, nil
}

type tgButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	URL  string `json:"url,omitempty"`
}

func (t *telegram) call(ctx context.Context, method string, req, out any) error {
	b, _ := json.Marshal(req)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, telegramAPI+"/bot"+t.token+"/"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(r)
	if err != nil {
		// The URL holds the token: never log the raw error text.
		return fmt.Errorf("telegram %s: request failed", method)
	}
	defer resp.Body.Close()
	var env struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("telegram %s: %v", method, err)
	}
	if !env.OK {
		return fmt.Errorf("telegram %s: %s", method, env.Description)
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// Send posts a plain message (used for alerts).
func (t *telegram) Send(ctx context.Context, text string) error {
	if t == nil {
		return errors.New("telegram is not configured")
	}
	return t.call(ctx, "sendMessage", map[string]any{"chat_id": t.cfg.ChatID, "text": truncate(text, 4000), "disable_web_page_preview": true}, nil)
}

func approvalText(ap Approval) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Approval needed on %s</b>\n<pre>%s</pre>", html.EscapeString(ap.Server), html.EscapeString(truncate(ap.Summary, 2500)))
	if ap.Reason != "" {
		fmt.Fprintf(&b, "\nReason: %s", html.EscapeString(truncate(ap.Reason, 400)))
	}
	if ap.Why != "" {
		fmt.Fprintf(&b, "\n<i>%s</i>", html.EscapeString(truncate(ap.Why, 300)))
	}
	return b.String()
}

// buttonsFor returns the keyboard for an approval.
func (t *telegram) buttonsFor(ap Approval) [][]tgButton {
	open := []tgButton{{Text: "Open", URL: ap.URL}}
	if !t.cfg.Buttons || ap.Kind != "action" || ap.AskAlways {
		return [][]tgButton{open}
	}
	return [][]tgButton{
		{{Text: "Approve", Data: "ok:" + ap.ID}, {Text: "Deny", Data: "no:" + ap.ID}},
		{{Text: "Always on " + truncate(ap.Server, 30), Data: "al:" + ap.ID}, open[0]},
	}
}

func (t *telegram) notifyApproval(ap Approval) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var msg struct {
		MessageID int64 `json:"message_id"`
	}
	err := t.call(ctx, "sendMessage", map[string]any{
		"chat_id": t.cfg.ChatID, "text": approvalText(ap), "parse_mode": "HTML", "disable_web_page_preview": true,
		"reply_markup": map[string]any{"inline_keyboard": t.buttonsFor(ap)},
	}, &msg)
	if err != nil {
		slog.Warn("telegram notification failed", "err", err)
		return
	}
	t.mu.Lock()
	t.msgs[ap.ID] = msg.MessageID
	t.mu.Unlock()
}

type tgUpdate struct {
	UpdateID int64 `json:"update_id"`
	Callback *struct {
		ID   string `json:"id"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Message *struct {
			MessageID int64 `json:"message_id"`
			Chat      struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
		Data string `json:"data"`
	} `json:"callback_query"`
}

// run fetches button presses while this hub is the one serving.
func (t *telegram) run(ctx context.Context) {
	if !t.cfg.Buttons {
		return
	}
	slog.Info("telegram: listening for approval button presses")
	var offset int64
	for ctx.Err() == nil {
		if !t.h.replica.serving() {
			// A standby that forwards must not take the presses.
			sleepCtx(ctx, 15*time.Second)
			continue
		}
		var ups []tgUpdate
		err := t.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 50, "allowed_updates": []string{"callback_query"}}, &ups)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("telegram: fetching button presses failed", "err", err)
				sleepCtx(ctx, 30*time.Second)
			}
			continue
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			if u.Callback != nil {
				t.press(ctx, u)
			}
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (t *telegram) press(ctx context.Context, u tgUpdate) {
	cb := u.Callback
	answer := func(text string) {
		t.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID, "text": text}, nil)
	}
	if cb.From.ID != t.cfg.ChatID || cb.Message == nil || cb.Message.Chat.ID != t.cfg.ChatID {
		t.h.audit.Write(audit.Entry{Event: "telegram_refused", Detail: map[string]any{"from": cb.From.ID}})
		answer("Not allowed.")
		return
	}
	slog.Info("telegram: button pressed", "data", cb.Data)
	action, id, ok := strings.Cut(cb.Data, ":")
	if !ok {
		answer("Unknown button.")
		return
	}
	snap, found := t.h.appr.snapshot(id)
	if !found {
		answer("This approval has expired.")
		t.finish(ctx, cb.Message.MessageID, "", "expired")
		return
	}
	if snap.Kind != "action" || snap.AskAlways || !t.cfg.Buttons {
		answer("This one needs the approval page.")
		return
	}
	var err error
	var label string
	switch action {
	case "ok":
		_, err = t.h.Decide(id, true, false, "", "telegram")
		label = "Approved"
	case "no":
		_, err = t.h.Decide(id, false, false, "", "telegram")
		label = "Denied"
	case "al":
		_, err = t.h.Decide(id, true, true, "server", "telegram")
		label = "Approved, always allowed on " + snap.Server
	default:
		answer("Unknown button.")
		return
	}
	if err != nil {
		answer(truncate(err.Error(), 180))
		t.finish(ctx, cb.Message.MessageID, approvalText(snap), err.Error())
		return
	}
	answer(label)
	t.finish(ctx, cb.Message.MessageID, approvalText(snap), label+" (Telegram)")
}

// finish replaces the buttons with the outcome.
func (t *telegram) finish(ctx context.Context, msgID int64, text, outcome string) {
	if text == "" {
		text = "Approval"
	}
	t.call(ctx, "editMessageText", map[string]any{
		"chat_id": t.cfg.ChatID, "message_id": msgID, "parse_mode": "HTML", "disable_web_page_preview": true,
		"text": text + "\n\n<b>" + html.EscapeString(outcome) + "</b>",
	}, nil)
}

// decided updates the Telegram message when an approval was decided
// elsewhere (web page, dashboard, hands-off mode).
func (t *telegram) decided(ap Approval, via string) {
	if t == nil || via == "telegram" {
		return
	}
	t.mu.Lock()
	msgID, ok := t.msgs[ap.ID]
	delete(t.msgs, ap.ID)
	t.mu.Unlock()
	if !ok {
		return
	}
	outcome := "Denied"
	if ap.Status == StatusApproved {
		outcome = "Approved"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.finish(ctx, msgID, approvalText(ap), outcome+" ("+via+")")
}
