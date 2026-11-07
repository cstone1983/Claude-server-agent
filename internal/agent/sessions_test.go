package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/policy"
	"github.com/cstone1983/Claude-server-agent/internal/proto"
)

func testExecutor(t *testing.T, allow []policy.Rule) (*Executor, string) {
	dir := t.TempDir()
	cfg := &Config{AuditLog: filepath.Join(dir, "audit.jsonl")}
	cfg.applyDefaults()
	al, err := audit.Open(cfg.AuditLog)
	if err != nil {
		t.Fatal(err)
	}
	return NewExecutor(cfg, policy.New(policy.File{Allow: allow}, nil), al), cfg.AuditLog
}

func call(e *Executor, tool string, args any) *proto.Result {
	b, _ := json.Marshal(args)
	return e.Handle(context.Background(), &proto.Call{Tool: tool, Args: b, Caller: "test"})
}

func sessionID(t *testing.T, r *proto.Result) string {
	t.Helper()
	for _, f := range strings.Fields(r.Text) {
		if strings.HasPrefix(f, "sess_") {
			return strings.TrimRight(f, "]")
		}
	}
	t.Fatalf("no session id in: %s %s", r.Text, r.Error)
	return ""
}

func TestInteractiveSession(t *testing.T) {
	script := `printf "Your name? "; read name; echo "hello $name"; read -s -p "Password: " pw; echo; echo "pw has ${#pw} chars"; read -p "Continue? [y/N] " a; echo "answer=$a"`
	e, auditPath := testExecutor(t, []policy.Rule{{Shell: script, As: "*"}})

	r := call(e, "session_start", map[string]any{"script": script, "wait_sec": 5})
	id := sessionID(t, r)
	if !strings.Contains(r.Text, "Your name?") || !strings.Contains(r.Text, "waiting for input") {
		t.Fatalf("start: %s", r.Text)
	}
	r = call(e, "session_send", map[string]any{"session_id": id, "input": "Chris", "wait_sec": 5})
	if !strings.Contains(r.Text, "hello Chris") || !strings.Contains(r.Text, "Password:") {
		t.Fatalf("send name: %s", r.Text)
	}
	r = call(e, "session_send", map[string]any{"session_id": id, "input": "hunter2-secret", "secret": true, "wait_sec": 5})
	if !strings.Contains(r.Text, "pw has 14 chars") || strings.Contains(r.Text, "hunter2") {
		t.Fatalf("send secret: %s", r.Text)
	}
	r = call(e, "session_send", map[string]any{"session_id": id, "input": "y", "wait_sec": 5})
	if !strings.Contains(r.Text, "answer=y") || !strings.Contains(r.Text, "program exited (code 0)") {
		t.Fatalf("send y: %s", r.Text)
	}
	if r := call(e, "session_send", map[string]any{"session_id": id, "input": "more"}); r.Error == "" {
		t.Fatal("sending to an exited session should fail")
	}
	call(e, "session_close", map[string]any{"session_id": id})
	b, _ := os.ReadFile(auditPath)
	if strings.Contains(string(b), "hunter2") || !strings.Contains(string(b), "[secret]") || !strings.Contains(string(b), "Chris") {
		t.Fatalf("audit log:\n%s", b)
	}
}

func TestSessionScreenAndKeys(t *testing.T) {
	// A "full-screen" program: clears the screen and draws at fixed positions,
	// then waits for Ctrl-C.
	script := `printf '\033[2J\033[3;5HMenu title\033[5;7H> Option one\033[6;7H  Option two'; trap 'echo; echo interrupted; exit 3' INT; while true; do sleep 1; done`
	e, _ := testExecutor(t, []policy.Rule{{Shell: script, As: "*"}})
	r := call(e, "session_start", map[string]any{"script": script, "cols": 40, "rows": 10, "wait_sec": 3})
	id := sessionID(t, r)
	r = call(e, "session_send", map[string]any{"session_id": id, "screen": true, "wait_sec": 1})
	lines := strings.Split(r.Text, "\n")
	found := false
	for _, l := range lines {
		if l == "    Menu title" {
			found = true
		}
	}
	if !found || !strings.Contains(r.Text, "      > Option one") {
		t.Fatalf("screen not rendered as expected:\n%s", r.Text)
	}
	r = call(e, "session_send", map[string]any{"session_id": id, "keys": []string{"ctrl-c"}, "wait_sec": 5})
	if !strings.Contains(r.Text, "interrupted") || !strings.Contains(r.Text, "exited (code 3)") {
		t.Fatalf("ctrl-c: %s", r.Text)
	}
	if r := call(e, "session_send", map[string]any{"session_id": id, "keys": []string{"bogus"}}); r.Error == "" {
		t.Fatal("unknown key should fail")
	}
	call(e, "session_close", map[string]any{"session_id": id})
	if r := call(e, "session_send", map[string]any{"session_id": id}); !strings.Contains(r.Error, "no such session") {
		t.Fatalf("closed session: %+v", r)
	}
}

func TestSessionNeedsApproval(t *testing.T) {
	e, _ := testExecutor(t, nil)
	r := call(e, "session_start", map[string]any{"argv": []string{"bash"}})
	if r.NeedsApproval == nil {
		t.Fatalf("interactive shell should need approval: %+v", r)
	}
	ro := &proto.Call{Tool: "session_start", Args: json.RawMessage(`{"argv":["bash"]}`), Caller: "t", MaxTier: "read"}
	if r := e.Handle(context.Background(), ro); r.Error == "" {
		t.Fatal("read-only caller must not start sessions")
	}
}
