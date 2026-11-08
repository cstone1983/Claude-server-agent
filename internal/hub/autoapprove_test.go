package hub

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/proto"
)

func newTestHub(t *testing.T) *Hub {
	t.Helper()
	dir := t.TempDir()
	cfg := &Config{StateDir: filepath.Join(dir, "s"), AuditLog: filepath.Join(dir, "a.jsonl"), PublicURL: "http://x"}
	cfg.applyDefaults()
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestAutoApprove: hands-off mode approves covered actions, never reboots
// that need their own OK, skips Proxmox hosts unless included, respects the
// server scope, approves already-pending requests, and ends when time is up.
func TestAutoApprove(t *testing.T) {
	h := newTestHub(t)
	web := &AgentRec{ID: "w", Name: "web"}
	db := &AgentRec{ID: "d", Name: "db"}
	pve := &AgentRec{ID: "p", Name: "Proxmox", Tags: []string{"proxmox"}}
	h.store.Update(func(d *stateData) error {
		d.Agents["w"], d.Agents["d"], d.Agents["p"] = web, db, pve
		return nil
	})
	call := &proto.Call{Tool: "run_command", Args: json.RawMessage(`{"argv":["apt-get","update"]}`), Caller: "claude:test"}
	na := func(ask bool) *proto.NeedsApproval {
		return &proto.NeedsApproval{ActionHash: "h", Summary: "run: x", Why: "x", AskAlways: ask}
	}
	status := func(ap *Approval) string { s, _ := h.appr.snapshot(ap.ID); return s.Status }

	// Off: stays pending.
	waiting := h.newApproval(web, call, na(false))
	if status(waiting) != StatusPending {
		t.Fatal("approved while hands-off mode is off")
	}

	// On for all servers: the waiting request is approved too.
	n, err := h.setAutoApprove(time.Hour, "all", false, "test")
	if err != nil || n != 1 {
		t.Fatalf("setAutoApprove: n=%d err=%v", n, err)
	}
	if s, _ := h.appr.snapshot(waiting.ID); s.Status != StatusApproved || !s.Auto {
		t.Fatalf("pending request not auto-approved: %+v", s)
	}
	if a := h.newApproval(db, call, na(false)); status(a) != StatusApproved {
		t.Fatal("covered action not auto-approved")
	}
	if a := h.newApproval(web, call, na(true)); status(a) != StatusPending {
		t.Fatal("an ask-always action was auto-approved")
	}
	if a := h.newApproval(pve, call, na(false)); status(a) != StatusPending {
		t.Fatal("Proxmox host auto-approved without being included")
	}

	// Including Proxmox: its ordinary actions yes, its reboots never.
	if _, err := h.setAutoApprove(time.Hour, "all", true, "test"); err != nil {
		t.Fatal(err)
	}
	if a := h.newApproval(pve, call, na(false)); status(a) != StatusApproved {
		t.Fatal("included Proxmox action not auto-approved")
	}
	if a := h.newApproval(pve, call, na(true)); status(a) != StatusPending {
		t.Fatal("Proxmox reboot was auto-approved")
	}

	// One server only.
	if _, err := h.setAutoApprove(time.Hour, "server:web", false, "test"); err != nil {
		t.Fatal(err)
	}
	if a := h.newApproval(db, call, na(false)); status(a) != StatusPending {
		t.Fatal("server scope not respected")
	}
	if a := h.newApproval(web, call, na(false)); status(a) != StatusApproved {
		t.Fatal("scoped server not auto-approved")
	}

	// Limits and validation.
	if _, err := h.setAutoApprove(13*time.Hour, "all", false, "test"); err == nil {
		t.Fatal("accepted more than the maximum duration")
	}
	if _, err := h.setAutoApprove(time.Hour, "server:nope", false, "test"); err == nil {
		t.Fatal("accepted an unknown server")
	}

	// Expired: off again.
	h.store.Update(func(d *stateData) error { d.AutoApprove.Until = time.Now().Add(-time.Second); return nil })
	if a := h.newApproval(web, call, na(false)); status(a) != StatusPending {
		t.Fatal("approved after hands-off mode expired")
	}

	// Turned off.
	h.setAutoApprove(time.Hour, "all", false, "test")
	h.clearAutoApprove("test")
	if a := h.newApproval(web, call, na(false)); status(a) != StatusPending {
		t.Fatal("approved after hands-off mode was turned off")
	}
}
