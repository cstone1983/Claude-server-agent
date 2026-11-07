package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/cstone1983/Claude-server-agent/internal/policy"
	"github.com/cstone1983/Claude-server-agent/internal/proto"
	"github.com/cstone1983/Claude-server-agent/internal/totp"
)

func TestDashboardBulkApproveAndRules(t *testing.T) {
	dir := t.TempDir()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw-123456789"), bcrypt.MinCost)
	cfg := &Config{StateDir: filepath.Join(dir, "s"), AuditLog: filepath.Join(dir, "a.jsonl"), PublicURL: "http://placeholder",
		AdminPasswordHash: string(hash), TOTPSecret: totp.NewSecret()}
	cfg.applyDefaults()
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var handler http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	defer srv.Close()
	cfg.PublicURL = srv.URL
	handler = h.Handler()

	a1 := &AgentRec{ID: "a1", Name: "n8n"}
	a2 := &AgentRec{ID: "a2", Name: "web"}
	h.store.Update(func(d *stateData) error { d.Agents["a1"], d.Agents["a2"] = a1, a2; return nil })
	na := &proto.NeedsApproval{ActionHash: "h", Summary: "run: apt-get update", Why: "x", SuggestedRule: policy.Rule{Argv: []string{"apt-get", "update"}}}
	call := &proto.Call{Tool: "run_command", Args: json.RawMessage(`{"argv":["apt-get","update"]}`), Caller: "claude:test"}
	ap1 := h.newApproval(a1, call, na)
	ap2 := h.newApproval(a2, call, na)

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	// Not logged in: redirected to the login page.
	resp, _ := c.Get(srv.URL + "/approvals")
	if !strings.Contains(resp.Request.URL.Path, "/login") {
		t.Fatalf("expected login redirect, got %s", resp.Request.URL)
	}
	code, _ := totp.Code(cfg.TOTPSecret, time.Now())
	// Log in the way a real browser with a strict referrer policy does:
	// Origin "null" plus Sec-Fetch-Site same-origin.
	lreq, _ := http.NewRequest("POST", srv.URL+"/login", strings.NewReader(url.Values{"password": {"pw-123456789"}, "code": {code}, "next": {"/approvals"}}.Encode()))
	lreq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lreq.Header.Set("Origin", "null")
	lreq.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, _ = c.Do(lreq)
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "apt-get update") || strings.Count(string(body), `name="id"`) != 2 {
		t.Fatalf("approvals page:\n%s", body)
	}
	// Cross-site post is refused.
	req, _ := http.NewRequest("POST", srv.URL+"/approvals", strings.NewReader("id="+ap1.ID+"&action=approve"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example.com")
	if resp, _ := c.Do(req); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site post: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", srv.URL+"/approvals", strings.NewReader("id="+ap1.ID+"&action=approve"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if resp, _ := c.Do(req); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site post (Sec-Fetch-Site): %d", resp.StatusCode)
	}
	if s, _ := h.appr.snapshot(ap1.ID); s.Status != StatusPending {
		t.Fatal("cross-site post changed an approval")
	}
	// Bulk approve + always allow on all servers: both approved, one rule.
	resp, _ = c.PostForm(srv.URL+"/approvals", url.Values{"id": {ap1.ID, ap2.ID}, "action": {"always"}, "scope": {"all"}})
	body, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Approved and allowed from now on: 2") {
		t.Fatalf("bulk approve:\n%s", body)
	}
	for _, id := range []string{ap1.ID, ap2.ID} {
		if s, _ := h.appr.snapshot(id); s.Status != StatusApproved {
			t.Fatalf("%s: %s", id, s.Status)
		}
	}
	h.store.View(func(d *stateData) {
		if len(d.Rules) != 1 || d.Rules[0].Scope != "all" {
			t.Fatalf("rules: %+v", d.Rules)
		}
	})

	// Add a rule from the simple form.
	resp, _ = c.PostForm(srv.URL+"/rules", url.Values{"op": {"add"}, "kind": {"command"}, "value": {"sudo systemctl restart 'nginx'"}, "any_args": {""}, "scope": {"server:web"}})
	body, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Added: argv systemctl restart nginx on web") {
		t.Fatalf("add rule:\n%s", body)
	}
	resp, _ = c.PostForm(srv.URL+"/rules", url.Values{"op": {"add"}, "kind": {"command"}, "value": {"ls | sh"}, "scope": {"all"}})
	body, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "one plain command") {
		t.Fatalf("pipe should be rejected:\n%s", body)
	}

	// Claude requests a rule; the user approves it on the dashboard.
	out := h.Invoke(context.Background(), Caller{Name: "claude:test"}, "request_rule", json.RawMessage(`{"value":"docker compose pull","any_args":true,"servers":"all","reason":"weekly image updates"}`))
	if out.IsError || !strings.Contains(out.Text, "not active yet") {
		t.Fatalf("request_rule: %s", out.Text)
	}
	var reqID string
	for _, a := range h.appr.list() {
		if a.Kind == "rule" && a.Status == StatusPending {
			reqID = a.ID
		}
	}
	resp, _ = c.PostForm(srv.URL+"/approvals", url.Values{"id": {reqID}, "action": {"approve"}})
	resp.Body.Close()
	if o := h.Invoke(context.Background(), Caller{Name: "claude:test"}, "check_approval", json.RawMessage(`{"approval_id":"`+reqID+`"}`)); !strings.Contains(o.Text, "Rule added: argv docker compose pull ** on all servers") {
		t.Fatalf("check_approval for rule: %s", o.Text)
	}
	if o := h.Invoke(context.Background(), Caller{Name: "x", ReadOnly: true}, "request_rule", json.RawMessage(`{"value":"reboot"}`)); !o.IsError {
		t.Fatal("read-only caller should not request rules")
	}
	h.store.View(func(d *stateData) {
		if len(d.Rules) != 3 {
			t.Fatalf("expected 3 rules, got %d", len(d.Rules))
		}
	})
}
