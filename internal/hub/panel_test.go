package hub

import (
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
	"github.com/cstone1983/Claude-server-agent/internal/totp"
)

func TestControlPanelPages(t *testing.T) {
	dir := t.TempDir()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw-123456789"), bcrypt.MinCost)
	cfg := &Config{StateDir: filepath.Join(dir, "s"), AuditLog: filepath.Join(dir, "a.jsonl"), PublicURL: "http://placeholder",
		AdminPasswordHash: string(hash), TOTPSecret: totp.NewSecret()}
	cfg.applyDefaults()
	h, _ := New(cfg)
	var handler http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	defer srv.Close()
	cfg.PublicURL = srv.URL
	handler = h.Handler()
	h.store.Update(func(d *stateData) error {
		d.Agents["a1"] = &AgentRec{ID: "a1", Name: "web01", Tags: []string{"web"}, OS: "Ubuntu", AgentVersion: "v0.1.1 (x)"}
		d.Rules = []*HubRule{{ID: "rule_1", Scope: "all", Rule: policy.Rule{Argv: []string{"systemctl", "restart", "**"}, Comment: "kit"}, CreatedAt: time.Now()}}
		return nil
	})
	si := sysInfo{OS: "Ubuntu", Load: "0.5 0.4 0.3", CPUs: 4, MemTotalMB: 8000, MemAvailMB: 2000, UptimeHours: 50}
	h.metrics.add("a1", si, time.Now().Add(-time.Minute))
	h.metrics.add("a1", si, time.Now())

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	get := func(path string) string {
		resp, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.HasPrefix(resp.Request.URL.Path, "/static") && !strings.Contains(csp, "script-src 'self'") {
			t.Fatalf("%s: CSP %q", path, csp)
		}
		return string(b)
	}
	if !strings.Contains(get("/servers"), "Admin password") {
		t.Fatal("panel pages must require login")
	}
	code, _ := totp.Code(cfg.TOTPSecret, time.Now())
	c.PostForm(srv.URL+"/login", url.Values{"password": {"pw-123456789"}, "code": {code}, "next": {"/"}})

	checks := map[string][]string{
		"/":                     {"Overview", "web01", "75% of 7.8 GB", "<svg class=\"spark\"", "agent v0.1.1"},
		"/approvals":            {"Nothing is waiting"},
		"/rules":                {"systemctl restart … (any arguments)", "Edit", "Disable", "Built in on every server"},
		"/rules/edit?id=rule_1": {"value=\"systemctl restart\"", "checked", "kit"},
		"/servers":              {"web01", "Add a server", "Rename"},
		"/activity":             {"Activity"},
		"/static/app.css":       {"--brand-1"},
		"/static/app.js":        {"data-theme-toggle"},
	}
	for path, wants := range checks {
		body := get(path)
		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("%s: missing %q", path, w)
			}
		}
	}

	// Disable the rule: it stops being pushed to agents.
	c.PostForm(srv.URL+"/rules", url.Values{"op": {"toggle"}, "rule_id": {"rule_1"}})
	if rules := h.rulesFor(&AgentRec{ID: "a1", Name: "web01"}); len(rules) != 0 {
		t.Fatalf("disabled rule still pushed: %v", rules)
	}
	// Edit it: new command, scoped to a tag, re-enabled.
	c.PostForm(srv.URL+"/rules/edit", url.Values{"rule_id": {"rule_1"}, "kind": {"command"}, "value": {"docker restart"}, "any_args": {"1"}, "scope": {"tag:web"}, "comment": {"edited"}})
	h.store.View(func(d *stateData) {
		r := d.Rules[0]
		if r.Disabled || r.Scope != "tag:web" || strings.Join(r.Rule.Argv, " ") != "docker restart **" || r.Rule.Comment != "edited" {
			t.Fatalf("after edit: %+v", r)
		}
	})
	// Create an install command from the Servers page.
	resp, _ := c.PostForm(srv.URL+"/servers", url.Values{"op": {"join"}, "uses": {"2"}, "ttl": {"30"}})
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "agent --hub "+srv.URL+" --join csa_join_") {
		t.Fatalf("join command not shown")
	}
	// Rename and tag from the Servers page.
	c.PostForm(srv.URL+"/servers", url.Values{"op": {"rename"}, "name": {"web01"}, "new_name": {"web-1"}})
	c.PostForm(srv.URL+"/servers", url.Values{"op": {"tags"}, "name": {"web-1"}, "tags": {"web, prod"}})
	if rec, ok := h.agentByName("web-1"); !ok || strings.Join(rec.Tags, ",") != "prod,web" {
		t.Fatalf("rename/tags: %+v", rec)
	}
}
