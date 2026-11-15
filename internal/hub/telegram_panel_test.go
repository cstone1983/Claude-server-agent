package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTelegramControlBar(t *testing.T) {
	var mu sync.Mutex
	var sent []map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &m)
		mu.Lock()
		sent = append(sent, m)
		mu.Unlock()
		io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer api.Close()
	old := telegramAPI
	telegramAPI = api.URL
	defer func() { telegramAPI = old }()

	dir := t.TempDir()
	tok := filepath.Join(dir, "tg")
	os.WriteFile(tok, []byte("1:x"), 0o600)
	cfg := &Config{StateDir: filepath.Join(dir, "s"), AuditLog: filepath.Join(dir, "a.jsonl"), PublicURL: "https://agents.example.com",
		Notify: NotifyConfig{Telegram: TelegramConfig{BotTokenFile: tok, ChatID: 42, Buttons: true}}}
	cfg.applyDefaults()
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	say := func(from int64, text string) {
		var u tgUpdate
		b, _ := json.Marshal(map[string]any{"update_id": 1, "message": map[string]any{"from": map[string]any{"id": from}, "chat": map[string]any{"id": from}, "text": text}})
		json.Unmarshal(b, &u)
		h.tg.panelMessage(ctx, u)
	}
	last := func() (string, string) {
		mu.Lock()
		defer mu.Unlock()
		m := sent[len(sent)-1]
		kb, _ := json.Marshal(m["reply_markup"])
		txt, _ := m["text"].(string)
		return txt, string(kb)
	}

	// A stranger is ignored.
	say(99, "Hands-off 1h")
	if h.autoApprove() != nil {
		t.Fatal("hands-off turned on by another account")
	}
	// /start shows the bar with the empty box.
	say(42, "/start")
	if _, kb := last(); !strings.Contains(kb, boxOff) || !strings.Contains(kb, "Hands-off 1h") || !strings.Contains(kb, `"is_persistent":true`) {
		t.Fatalf("bar: %s", kb)
	}
	// Tick the box, then 1h: covers Proxmox, box resets.
	say(42, boxOff)
	if _, kb := last(); !strings.Contains(kb, boxOn) {
		t.Fatalf("box not ticked: %s", kb)
	}
	say(42, "Hands-off 1h")
	aa := h.autoApprove()
	if aa == nil || !aa.IncludeCareful || aa.Until.Sub(time.Now()) < 55*time.Minute {
		t.Fatalf("hands-off not set right: %+v", aa)
	}
	txt, kb := last()
	if !strings.Contains(txt, "Hands-off ON") || !strings.Contains(kb, btnStopPrefx) || !strings.Contains(kb, "incl. Proxmox") {
		t.Fatalf("on: %q %s", txt, kb)
	}
	// Stop.
	say(42, btnStopPrefx+" (until 10:00, incl. Proxmox)")
	if h.autoApprove() != nil {
		t.Fatal("stop did not turn hands-off off")
	}
	if _, kb := last(); !strings.Contains(kb, boxOff) {
		t.Fatalf("box should be cleared after use: %s", kb)
	}
	// Turned on from the dashboard, then ended: the bar follows both.
	h.setAutoApprove(4*time.Hour, "all", false, "dashboard")
	h.tg.syncPanel(ctx)
	if txt, kb := last(); !strings.Contains(txt, "set elsewhere") || !strings.Contains(kb, btnStopPrefx) {
		t.Fatalf("dashboard on not followed: %q", txt)
	}
	h.clearAutoApprove("dashboard")
	h.tg.syncPanel(ctx)
	if txt, kb := last(); !strings.Contains(txt, "Hands-off ended") || !strings.Contains(kb, "Hands-off 1h") {
		t.Fatalf("end not followed: %q", txt)
	}
	n := len(sent)
	h.tg.syncPanel(ctx)
	if len(sent) != n {
		t.Fatal("bar re-sent without a change")
	}
}
