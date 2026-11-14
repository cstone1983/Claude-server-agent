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

func TestWatcherAlertsOnceAndResolves(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &m)
		if txt, _ := m["text"].(string); txt != "" {
			mu.Lock()
			msgs = append(msgs, txt)
			mu.Unlock()
		}
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
		Notify: NotifyConfig{Telegram: TelegramConfig{BotTokenFile: tok, ChatID: 1}},
		Alerts: AlertsConfig{Enabled: true, Checks: []Check{{Name: "report", Server: "web", Argv: []string{"/usr/local/sbin/health-report"}, Every: "1h"}}}}
	cfg.applyDefaults()
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.watch.start = time.Now().Add(-time.Hour)
	h.store.Update(func(d *stateData) error {
		d.Agents["a1"] = &AgentRec{ID: "a1", Name: "web", LastSeen: time.Now().Add(-10 * time.Minute)}
		d.Agents["a2"] = &AgentRec{ID: "a2", Name: "db", LastSeen: time.Now()}
		return nil
	})
	last := func() string {
		mu.Lock()
		defer mu.Unlock()
		if len(msgs) == 0 {
			return ""
		}
		return msgs[len(msgs)-1]
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(msgs) }

	// web offline 10 min -> one alert, not repeated.
	h.watch.evaluate(time.Now())
	if !strings.Contains(last(), "web is offline") {
		t.Fatalf("no offline alert: %q", last())
	}
	h.watch.evaluate(time.Now())
	if count() != 1 {
		t.Fatalf("alert repeated: %d messages", count())
	}
	// web back, db online with a full disk.
	h.reg.conns["a1"] = &agentConn{id: "a1", closed: make(chan struct{})}
	h.reg.conns["a2"] = &agentConn{id: "a2", closed: make(chan struct{})}
	var si sysInfo
	json.Unmarshal([]byte(`{"disks":[{"mount":"/","size_gb":10,"used_pct":96}],"failed_units":[]}`), &si)
	h.metrics.add("a2", si, time.Now())
	h.watch.evaluate(time.Now())
	if m := last(); !strings.Contains(m, "Resolved") || !strings.Contains(m, "web is offline") || !strings.Contains(m, "db: disk / is 96% full") {
		t.Fatalf("expected resolve + disk alert: %q", m)
	}
	// A check that prints an ALERT line.
	h.testSend = func(c *agentConn, env proto.Envelope) error {
		go h.reg.deliver(env.ID, &proto.Result{OK: true, Text: "OK backups\nALERT SAN disk 8 failed\n"})
		return nil
	}
	h.watch.runCheck(context.Background(), cfg.Alerts.Checks[0])
	if !strings.Contains(last(), "ALERT SAN disk 8 failed") {
		t.Fatalf("check alert missing: %q", last())
	}
}
