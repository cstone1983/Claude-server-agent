package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/cstone1983/Claude-server-agent/internal/policy"
	"github.com/cstone1983/Claude-server-agent/internal/proto"
	"github.com/cstone1983/Claude-server-agent/internal/totp"
)

type devTest struct {
	t   *testing.T
	h   *Hub
	cfg *Config
	srv *httptest.Server
	c   *http.Client
	ag  *AgentRec
}

func newDevTest(t *testing.T) *devTest {
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
	t.Cleanup(srv.Close)
	cfg.PublicURL = srv.URL
	handler = h.Handler()
	ag := &AgentRec{ID: "a1", Name: "web"}
	h.store.Update(func(d *stateData) error { d.Agents["a1"] = ag; return nil })
	jar, _ := cookiejar.New(nil)
	return &devTest{t: t, h: h, cfg: cfg, srv: srv, c: &http.Client{Jar: jar}, ag: ag}
}

func (d *devTest) approval(askAlways bool) *Approval {
	na := &proto.NeedsApproval{ActionHash: randomToken("", 4), Summary: "run: apt-get update", Why: "x",
		SuggestedRule: policy.Rule{Argv: []string{"apt-get", "update"}}, AskAlways: askAlways}
	call := &proto.Call{Tool: "run_command", Args: json.RawMessage(`{"argv":["apt-get","update"]}`), Caller: "claude:test"}
	return d.h.newApproval(d.ag, call, na)
}

func (d *devTest) code() string {
	c, _ := totp.Code(d.cfg.TOTPSecret, time.Now())
	return c
}

// post sends a same-origin form post (as a browser would from the hub's own page).
func (d *devTest) post(path string, form url.Values) (*http.Response, string) {
	req, _ := http.NewRequest("POST", d.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := d.c.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func (d *devTest) status(id string) string {
	s, _ := d.h.appr.snapshot(id)
	return s.Status
}

func (d *devTest) cookie(name string) *http.Cookie {
	u, _ := url.Parse(d.srv.URL)
	for _, c := range d.c.Jar.Cookies(u) {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestRememberedDeviceApprovals(t *testing.T) {
	d := newDevTest(t)

	// First approval with a code and "remember this device".
	a1 := d.approval(false)
	if _, body := d.post("/approve/"+a1.ID, url.Values{"code": {d.code()}, "decision": {"approve"}, "remember": {"1"}}); d.status(a1.ID) != StatusApproved {
		t.Fatalf("first approval not approved:\n%s", body)
	}
	if d.cookie(deviceCookie) == nil || len(d.h.devices()) != 1 {
		t.Fatal("device not remembered")
	}

	// The approval page no longer asks for a code...
	a2 := d.approval(false)
	resp, _ := d.c.Get(d.srv.URL + "/approve/" + a2.ID)
	b, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(b), `name="code"`) || !strings.Contains(string(b), "Remembered device") {
		t.Fatalf("remembered device still asked for a code:\n%s", b)
	}
	// ...and approves without one.
	d.post("/approve/"+a2.ID, url.Values{"decision": {"approve"}})
	if d.status(a2.ID) != StatusApproved {
		t.Fatal("remembered device could not approve without a code")
	}
	if log, _ := os.ReadFile(d.cfg.AuditLog); !strings.Contains(string(log), "remembered device") {
		t.Fatal("decision not attributed to the device in the audit log")
	}

	// A cross-site post is refused even with the device cookie.
	a3 := d.approval(false)
	req, _ := http.NewRequest("POST", d.srv.URL+"/approve/"+a3.ID, strings.NewReader("decision=approve"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if resp, _ := d.c.Do(req); resp.StatusCode != http.StatusForbidden || d.status(a3.ID) != StatusPending {
		t.Fatalf("cross-site post with device cookie: %d, %s", resp.StatusCode, d.status(a3.ID))
	}

	// Requests that need their own OK still need a code.
	boot := d.approval(true)
	if resp, _ := d.post("/approve/"+boot.ID, url.Values{"decision": {"approve"}}); resp.StatusCode != http.StatusUnauthorized || d.status(boot.ID) != StatusPending {
		t.Fatalf("ask-always approved without a code: %d %s", resp.StatusCode, d.status(boot.ID))
	}
	d.post("/approve/"+boot.ID, url.Values{"decision": {"approve"}, "code": {d.code()}})
	if d.status(boot.ID) != StatusApproved {
		t.Fatal("ask-always not approved with a code")
	}

	// The device is logged in to the dashboard (no session cookie needed).
	resp, _ = d.c.Get(d.srv.URL + "/approvals")
	if strings.Contains(resp.Request.URL.Path, "/login") {
		t.Fatal("remembered device redirected to login")
	}
	b, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "this device") {
		t.Fatalf("devices list missing:\n%s", b)
	}
	// The dashboard also wants a code for ask-always approvals.
	boot2 := d.approval(true)
	d.post("/approvals", url.Values{"id": {boot2.ID}, "action": {"approve"}})
	if d.status(boot2.ID) != StatusPending {
		t.Fatal("dashboard approved an ask-always request without a code")
	}
	d.post("/approvals", url.Values{"id": {boot2.ID}, "action": {"approve"}, "code": {d.code()}})
	if d.status(boot2.ID) != StatusApproved {
		t.Fatal("dashboard did not approve an ask-always request with a code")
	}

	// Forgetting the device ends it: the approval page asks for a code again.
	id := d.h.devices()[0].ID
	d.post("/approvals", url.Values{"op": {"device_forget"}, "device_id": {id}})
	if len(d.h.devices()) != 0 {
		t.Fatal("device not forgotten")
	}
	a4 := d.approval(false)
	if resp, _ := d.post("/approve/"+a4.ID, url.Values{"decision": {"approve"}}); resp.StatusCode != http.StatusUnauthorized || d.status(a4.ID) != StatusPending {
		t.Fatalf("forgotten device approved without a code: %d", resp.StatusCode)
	}
}

func TestRememberedDeviceLoginLogoutAndCookies(t *testing.T) {
	d := newDevTest(t)
	resp, _ := d.post("/login", url.Values{"password": {"pw-123456789"}, "code": {d.code()}, "next": {"/approvals"}, "remember": {"1"}})
	if strings.Contains(resp.Request.URL.Path, "/login") {
		t.Fatal("login failed")
	}
	// Both cookies are Lax so they come along when a link is opened from the
	// Claude app; the device cookie is long-lived.
	var sessSet, devSet bool
	for _, r := range []*http.Response{resp.Request.Response, resp} {
		if r == nil {
			continue
		}
		for _, c := range r.Cookies() {
			switch c.Name {
			case sessionCookie:
				sessSet = c.SameSite == http.SameSiteLaxMode
			case deviceCookie:
				devSet = c.SameSite == http.SameSiteLaxMode && c.MaxAge >= int((365*24*time.Hour).Seconds()) && c.HttpOnly
			}
		}
	}
	if !sessSet || !devSet {
		t.Fatalf("cookies: session lax=%v, device lax/long/httponly=%v", sessSet, devSet)
	}
	if len(d.h.devices()) != 1 {
		t.Fatal("login did not remember the device")
	}
	// Without the session cookie the device alone keeps the browser logged in,
	// and the login page sends it straight on.
	u, _ := url.Parse(d.srv.URL)
	d.c.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1}})
	resp, _ = d.c.Get(d.srv.URL + "/login?next=/approvals")
	if !strings.HasSuffix(resp.Request.URL.Path, "/approvals") {
		t.Fatalf("remembered device shown the login page: %s", resp.Request.URL)
	}
	// Logging out forgets this device.
	d.c.Get(d.srv.URL + "/logout")
	if len(d.h.devices()) != 0 {
		t.Fatal("logout did not forget the device")
	}
	resp, _ = d.c.Get(d.srv.URL + "/approvals")
	if !strings.Contains(resp.Request.URL.Path, "/login") {
		t.Fatal("still logged in after logout")
	}
}

func TestDeviceLabel(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36":               "Chrome on Windows",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/604.1": "Safari on iPhone",
		"Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/26.0 Chrome/122.0 Safari/537.36":     "Samsung Internet on Android",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36 Edg/130.0":     "Edge on Windows",
		"": "Browser on unknown OS",
	}
	for ua, want := range cases {
		if got := deviceLabel(ua); got != want {
			t.Errorf("deviceLabel(%q) = %q, want %q", ua, got, want)
		}
	}
}

func TestRenameRememberedDevice(t *testing.T) {
	d := newDevTest(t)
	d.post("/login", url.Values{"password": {"pw-123456789"}, "code": {d.code()}, "next": {"/approvals"}, "remember": {"1"}})
	dev := d.h.devices()[0]

	_, body := d.post("/approvals", url.Values{"op": {"device_rename"}, "device_id": {dev.ID}, "name": {"  Chris's   phone "}})
	if got := d.h.devices()[0]; got.Name != "Chris's phone" || got.DisplayName() != "Chris's phone" || got.Label != dev.Label {
		t.Fatalf("rename: %+v", got)
	}
	if !strings.Contains(body, "Device renamed") || !strings.Contains(body, `value="Chris&#39;s phone"`) {
		t.Fatalf("approvals page after rename:\n%s", body)
	}
	// The approval page shows the new name.
	a := d.approval(false)
	resp, _ := d.c.Get(d.srv.URL + "/approve/" + a.ID)
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "Chris&#39;s phone") {
		t.Fatalf("approval page does not show the device name:\n%s", b)
	}
	// Too long is refused; empty goes back to the detected label.
	if _, err := d.h.renameDevice(dev.ID, strings.Repeat("x", 61), "t"); err == nil {
		t.Fatal("over-long name accepted")
	}
	d.post("/approvals", url.Values{"op": {"device_rename"}, "device_id": {dev.ID}, "name": {""}})
	if got := d.h.devices()[0]; got.Name != "" || got.DisplayName() != got.Label {
		t.Fatalf("clearing the name: %+v", got)
	}
	// Unknown device.
	if ok, _ := d.h.renameDevice("dev_nope", "x", "t"); ok {
		t.Fatal("renamed a device that does not exist")
	}
}
