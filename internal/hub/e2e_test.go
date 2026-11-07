package hub

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/crypto/bcrypt"

	"github.com/cstone1983/Claude-server-agent/internal/agent"
	"github.com/cstone1983/Claude-server-agent/internal/totp"
)

type bearerRT struct{ tok string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(r)
}

func TestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	cfg := &Config{StateDir: filepath.Join(dir, "hub"), AuditLog: filepath.Join(dir, "hub-audit.jsonl"),
		AdminPasswordHash: string(hash), TOTPSecret: totp.NewSecret(), ApprovalWaitSec: 1, PublicURL: "http://placeholder"}
	cfg.applyDefaults()
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// httptest needs the handler before we know the URL; route through a pointer.
	var handler http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	defer srv.Close()
	cfg.PublicURL = srv.URL
	handler = h.Handler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- enroll and connect an agent ---
	joinTok := randomToken("csa_join_", 24)
	h.store.Update(func(d *stateData) error {
		d.JoinTokens[hashSecret(joinTok)] = &JoinToken{ExpiresAt: time.Now().Add(time.Hour), UsesLeft: 1, Tags: []string{"test"}}
		return nil
	})
	acfgPath := filepath.Join(dir, "agent", "agent.yaml")
	os.MkdirAll(filepath.Dir(acfgPath), 0o700)
	os.WriteFile(acfgPath, []byte("key_file: "+filepath.Join(dir, "agent", "agent.key")+"\npolicy_file: "+filepath.Join(dir, "agent", "policy.yaml")+"\nhub_rules_file: "+filepath.Join(dir, "agent", "hub-rules.yaml")+"\naudit_log: "+filepath.Join(dir, "agent", "audit.jsonl")+"\nhub_url: x\nagent_id: x\n"), 0o600)
	acfg, err := agent.Enroll(acfgPath, srv.URL, joinTok, "web01", []string{"web"}, false)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other", "agent.yaml")
	os.MkdirAll(filepath.Dir(other), 0o700)
	os.WriteFile(other, []byte("key_file: "+filepath.Join(dir, "other", "agent.key")+"\n"), 0o600)
	if _, err := agent.Enroll(other, srv.URL, joinTok, "web02", nil, false); err == nil {
		t.Fatal("join token should be single-use")
	}
	// A taken name is refused without consuming the token; --replace is required.
	tok2 := randomToken("csa_join_", 24)
	h.store.Update(func(d *stateData) error {
		d.JoinTokens[hashSecret(tok2)] = &JoinToken{ExpiresAt: time.Now().Add(time.Hour), UsesLeft: 1}
		return nil
	})
	if _, err := agent.Enroll(other, srv.URL, tok2, "web01", nil, false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := agent.Enroll(other, srv.URL, tok2, "web02", nil, false); err != nil {
		t.Fatalf("token should still be usable after a name conflict: %v", err)
	}
	ag, err := agent.New(acfg)
	if err != nil {
		t.Fatal(err)
	}
	go ag.Run(ctx)
	waitFor(t, func() bool { return h.reg.online(acfg.AgentID) })

	// --- OAuth: discovery, registration, login with password + TOTP, PKCE ---
	resp, _ := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if resp.StatusCode != 401 || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatalf("unauthenticated /mcp: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	if code := regStatus(srv.URL, "https://evil.example.com/cb"); code != 400 {
		t.Fatalf("evil redirect registered: %d", code)
	}
	redirect := "https://claude.ai/api/mcp/auth_callback"
	regBody, _ := json.Marshal(map[string]any{"redirect_uris": []string{redirect}, "client_name": "Claude"})
	resp, _ = http.Post(srv.URL+"/oauth/register", "application/json", strings.NewReader(string(regBody)))
	var reg struct {
		ClientID string `json:"client_id"`
	}
	json.NewDecoder(resp.Body).Decode(&reg)
	verifier := "test-verifier-0123456789-0123456789-0123456789"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{"client_id": {reg.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"xyz"}, "password": {"wrong"}, "code": {"000000"}}
	resp, _ = noRedirect.PostForm(srv.URL+"/oauth/authorize", form)
	if resp.StatusCode != 401 {
		t.Fatalf("bad login accepted: %d", resp.StatusCode)
	}
	code, _ := totp.Code(cfg.TOTPSecret, time.Now())
	form.Set("password", "correct horse")
	form.Set("code", code)
	resp, _ = noRedirect.PostForm(srv.URL+"/oauth/authorize", form)
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != 302 || loc.Query().Get("state") != "xyz" || loc.Query().Get("code") == "" {
		t.Fatalf("authorize: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = http.PostForm(srv.URL+"/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")},
		"client_id": {reg.ClientID}, "redirect_uri": {redirect}, "code_verifier": {"wrong-verifier"}})
	if resp.StatusCode != 400 {
		t.Fatalf("bad PKCE accepted")
	}
	// The code was consumed by the failed attempt; get a fresh one.
	resp, _ = noRedirect.PostForm(srv.URL+"/oauth/authorize", form)
	loc, _ = url.Parse(resp.Header.Get("Location"))
	resp, _ = http.PostForm(srv.URL+"/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")},
		"client_id": {reg.ClientID}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	json.NewDecoder(resp.Body).Decode(&tok)
	if tok.AccessToken == "" {
		t.Fatal("no access token")
	}
	resp, _ = http.PostForm(srv.URL+"/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok.RefreshToken}, "client_id": {reg.ClientID}})
	json.NewDecoder(resp.Body).Decode(&tok)
	if tok.AccessToken == "" {
		t.Fatal("refresh failed")
	}

	// --- MCP session ---
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerRT{tok.AccessToken}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	tl, err := sess.ListTools(ctx, nil)
	if err != nil || len(tl.Tools) < 10 {
		t.Fatalf("tools: %v %d", err, len(tl.Tools))
	}
	call := func(name string, args map[string]any) (string, bool) {
		r, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return r.Content[0].(*mcp.TextContent).Text, r.IsError
	}

	text, _ := call("list_servers", nil)
	if !strings.Contains(text, "web01 — online") || !strings.Contains(text, "web02 — offline") {
		t.Fatalf("list_servers: %s", text)
	}
	text, isErr := call("run_command", map[string]any{"server": "web01", "argv": []string{"uname", "-s"}})
	if isErr || !strings.Contains(text, "Linux") || !strings.Contains(text, "tier: read") {
		t.Fatalf("read command: %s", text)
	}
	text, _ = call("system_info", map[string]any{"server": "tag:web"})
	if !strings.Contains(text, "=== web01 ===") || !strings.Contains(text, "mem_total_mb") {
		t.Fatalf("system_info fan-out: %s", text)
	}

	// Elevated: needs approval, returns pending after the wait.
	marker := filepath.Join(dir, "touched")
	text, _ = call("run_command", map[string]any{"server": "web01", "argv": []string{"touch", marker}, "reason": "test"})
	if !strings.Contains(text, "NEEDS APPROVAL") {
		t.Fatalf("expected approval: %s", text)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("command ran without approval")
	}
	aps := h.appr.list()
	if len(aps) != 1 || !strings.Contains(text, aps[0].URL) {
		t.Fatalf("approval list: %+v", aps)
	}
	// Approve via the web page with a TOTP code, choosing "always allow".
	page, _ := http.Get(aps[0].URL)
	pb, _ := io.ReadAll(page.Body)
	if !strings.Contains(string(pb), "touch") {
		t.Fatalf("approval page: %s", pb)
	}
	code, _ = totp.Code(cfg.TOTPSecret, time.Now())
	http.PostForm(aps[0].URL, url.Values{"code": {code}, "decision": {"approve"}, "always": {"1"}, "scope": {"server"}})
	text, isErr = call("check_approval", map[string]any{"approval_id": aps[0].ID})
	if isErr || !strings.Contains(text, "approved by user") {
		t.Fatalf("check_approval: %s", text)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("approved command did not run")
	}
	text, _ = call("check_approval", map[string]any{"approval_id": aps[0].ID})
	if !strings.Contains(text, "already ran") {
		t.Fatalf("approved action should run once: %s", text)
	}
	// The rule was pushed to the agent: the same command now runs freely.
	waitFor(t, func() bool {
		b, _ := os.ReadFile(acfg.HubRulesFile)
		return strings.Contains(string(b), "touch")
	})
	os.Remove(marker)
	text, _ = call("run_command", map[string]any{"server": "web01", "argv": []string{"touch", marker}})
	if !strings.Contains(text, "tier: allowed") {
		t.Fatalf("rule not applied: %s", text)
	}
	// A different command is still elevated; deny it.
	text, _ = call("run_shell", map[string]any{"server": "web01", "script": "echo hi > " + marker})
	if !strings.Contains(text, "NEEDS APPROVAL") {
		t.Fatalf("shell should need approval: %s", text)
	}
	var shellID string
	for _, a := range h.appr.list() {
		if a.Status == StatusPending {
			shellID = a.ID
		}
	}
	if _, err := h.Decide(shellID, false, false, "", "test"); err != nil {
		t.Fatal(err)
	}
	if text, isErr = call("check_approval", map[string]any{"approval_id": shellID}); !isErr || !strings.Contains(text, "denied") {
		t.Fatalf("denied: %s", text)
	}
	// Denied by built-in policy: never even asks.
	text, isErr = call("run_command", map[string]any{"server": "web01", "argv": []string{"rm", "-rf", "/"}})
	if !isErr || !strings.Contains(text, "denied by this server's policy") {
		t.Fatalf("rm -rf / : %s", text)
	}
	// Files.
	f := filepath.Join(dir, "f.txt")
	os.WriteFile(f, []byte("hello world"), 0o644)
	text, _ = call("read_file", map[string]any{"server": "web01", "path": f})
	if text != "hello world" {
		t.Fatalf("read_file: %q", text)
	}
	text, _ = call("write_file", map[string]any{"server": "web01", "path": f, "content": "x"})
	if !strings.Contains(text, "NEEDS APPROVAL") {
		t.Fatalf("write_file: %s", text)
	}

	// --- REST API with a read-only token ---
	apiTok := randomToken("csa_api_", 32)
	h.store.Update(func(d *stateData) error {
		d.Tokens[hashSecret(apiTok)] = &Token{Kind: TokenAPI, Name: "n8n", Scope: ScopeRead}
		return nil
	})
	rest := func(tool, body string) Outcome {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/tools/"+tool, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+apiTok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var o Outcome
		json.NewDecoder(resp.Body).Decode(&o)
		return o
	}
	if o := rest("run_command", `{"server":"web01","argv":["uptime"]}`); o.IsError {
		t.Fatalf("rest read: %+v", o)
	}
	if o := rest("run_command", `{"server":"web01","argv":["touch","/tmp/nope"]}`); !o.IsError || !strings.Contains(o.Text, "read-only") {
		t.Fatalf("read-only token ran a write: %+v", o)
	}

	// Wrong Host header is rejected.
	req, _ := http.NewRequest("GET", srv.URL+"/healthz", nil)
	req.Host = "evil.example.com"
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("host guard: %d", resp.StatusCode)
	}
}

func regStatus(base, redirect string) int {
	b, _ := json.Marshal(map[string]any{"redirect_uris": []string{redirect}})
	resp, err := http.Post(base+"/oauth/register", "application/json", strings.NewReader(string(b)))
	if err != nil {
		return 0
	}
	return resp.StatusCode
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting")
}
