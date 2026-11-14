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

	"github.com/cstone1983/Claude-server-agent/internal/proto"
)

func TestTelegramApprovalButtons(t *testing.T) {
	var mu sync.Mutex
	var sent []map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		m["_method"] = r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		mu.Lock()
		sent = append(sent, m)
		mu.Unlock()
		io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
	}))
	defer api.Close()
	old := telegramAPI
	telegramAPI = api.URL
	defer func() { telegramAPI = old }()

	dir := t.TempDir()
	tok := filepath.Join(dir, "tg")
	os.WriteFile(tok, []byte("123:abc\n"), 0o600)
	cfg := &Config{StateDir: filepath.Join(dir, "s"), AuditLog: filepath.Join(dir, "a.jsonl"), PublicURL: "https://agents.example.com",
		Notify: NotifyConfig{Telegram: TelegramConfig{BotTokenFile: tok, ChatID: 42, Buttons: true}}}
	cfg.applyDefaults()
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	agent := &AgentRec{ID: "a1", Name: "web"}
	ap := h.newApproval(agent, &proto.Call{Tool: "run_command", Args: json.RawMessage(`{}`)}, &proto.NeedsApproval{ActionHash: "h", Summary: "apt upgrade"})
	careful := h.newApproval(agent, &proto.Call{Tool: "run_command", Args: json.RawMessage(`{}`)}, &proto.NeedsApproval{ActionHash: "h2", Summary: "reboot", AskAlways: true})
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	var kb []string
	for _, m := range sent {
		b, _ := json.Marshal(m["reply_markup"])
		kb = append(kb, string(b))
	}
	mu.Unlock()
	if len(kb) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(kb))
	}
	var normal, ask string
	for _, k := range kb {
		if strings.Contains(k, careful.ID) || !strings.Contains(k, "ok:") {
			ask = k
		} else {
			normal = k
		}
	}
	if !strings.Contains(normal, "ok:"+ap.ID) || !strings.Contains(normal, "al:"+ap.ID) {
		t.Fatalf("normal approval lacks buttons: %s", normal)
	}
	if strings.Contains(ask, "callback_data") {
		t.Fatalf("ask-always approval must only link to the page: %s", ask)
	}

	press := func(from int64, data string) {
		var u tgUpdate
		json.Unmarshal([]byte(`{"update_id":1,"callback_query":{"id":"q","from":{"id":`+tgItoa(from)+`},"message":{"message_id":7,"chat":{"id":`+tgItoa(from)+`}},"data":"`+data+`"}}`), &u)
		h.tg.press(context.Background(), u)
	}
	press(99, "ok:"+ap.ID) // someone else
	if s, _ := h.appr.snapshot(ap.ID); s.Status != StatusPending {
		t.Fatal("a press from another account was accepted")
	}
	press(42, "ok:"+careful.ID) // ask-always cannot be approved by button
	if s, _ := h.appr.snapshot(careful.ID); s.Status != StatusPending {
		t.Fatal("ask-always approval was approved from Telegram")
	}
	press(42, "ok:"+ap.ID)
	if s, _ := h.appr.snapshot(ap.ID); s.Status != StatusApproved {
		t.Fatalf("approve button did not approve: %s", s.Status)
	}
}

func tgItoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
