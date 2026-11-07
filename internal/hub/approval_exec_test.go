package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/policy"
	"github.com/cstone1983/Claude-server-agent/internal/proto"
)

// An approved action that runs longer than the caller waits must keep
// running, be reported as "executing", and never run twice.
func TestApprovedActionOutlivesCaller(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{StateDir: filepath.Join(dir, "s"), AuditLog: filepath.Join(dir, "a.jsonl"), PublicURL: "http://localhost"}
	cfg.applyDefaults()
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// A fake agent connection: count calls and answer after a delay.
	var calls atomic.Int32
	release := make(chan struct{})
	h.reg.conns["a1"] = &agentConn{id: "a1", pending: map[string]chan *proto.Result{}, closed: make(chan struct{})}
	h.testSend = func(c *agentConn, env proto.Envelope) error {
		calls.Add(1)
		go func() {
			<-release
			c.mu.Lock()
			ch := c.pending[env.ID]
			c.mu.Unlock()
			ch <- &proto.Result{OK: true, Text: "upgrade done"}
		}()
		return nil
	}
	old := executeWait
	executeWait = 100 * time.Millisecond
	defer func() { executeWait = old }()

	ap := h.newApproval(&AgentRec{ID: "a1", Name: "web"}, &proto.Call{Tool: "run_command", Args: json.RawMessage(`{}`)},
		&proto.NeedsApproval{ActionHash: "h", Summary: "long job", SuggestedRule: policy.Rule{Argv: []string{"x"}}})
	h.Decide(ap.ID, true, false, "", "test")

	// Caller gives up quickly (its context ends) while the action runs.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	res, st, err := h.executeApproved(ctx, ap.ID, "")
	cancel()
	if res != nil || st != StatusExecuting || err != nil {
		t.Fatalf("first check: %v %s %v", res, st, err)
	}
	out := h.checkApproval(context.Background(), Caller{}, ap.ID)
	if out.Text == "" || calls.Load() != 1 {
		t.Fatalf("second check: %q calls=%d", out.Text, calls.Load())
	}
	close(release)
	time.Sleep(50 * time.Millisecond)
	out = h.checkApproval(context.Background(), Caller{}, ap.ID)
	if out.IsError || calls.Load() != 1 || !containsStr(out.Text, "upgrade done") {
		t.Fatalf("final: %q calls=%d", out.Text, calls.Load())
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
