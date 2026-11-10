package hub

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func (d *devTest) activity() string {
	resp, err := d.c.Get(d.srv.URL + "/activity")
	if err != nil {
		d.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(b)
}

func TestActivityAllowAndRevoke(t *testing.T) {
	d := newDevTest(t)
	d.post("/login", url.Values{"password": {"pw-123456789"}, "code": {d.code()}, "next": {"/"}, "remember": {"1"}})

	a := d.approval(false)
	d.post("/approve/"+a.ID, url.Values{"decision": {"approve"}})

	// The approval request row offers "Always allow".
	if body := d.activity(); !strings.Contains(body, `value="act_allow"`) {
		t.Fatalf("no always-allow button on the activity page:\n%s", body)
	}

	// It still works after a hub restart: drop the in-memory approvals.
	d.h.appr.mu.Lock()
	d.h.appr.m = map[string]*Approval{}
	d.h.appr.mu.Unlock()
	if body := d.activity(); !strings.Contains(body, `value="act_allow"`) {
		t.Fatal("always-allow button gone after the approvals were cleared")
	}

	resp, _ := d.post("/activity", url.Values{"op": {"act_allow"}, "server": {"web"}, "summary": {a.Summary}, "scope": {"server"}})
	if d.ruleCount() != 1 || !strings.Contains(resp.Request.URL.RawQuery, "done=") {
		t.Fatalf("rule not added from activity (rules=%d, %s)", d.ruleCount(), resp.Request.URL)
	}
	body := d.activity()
	if !strings.Contains(body, `value="rule_revoke"`) || !strings.Contains(body, "always allowed") {
		t.Fatalf("no revoke button after allowing:\n%s", body)
	}
	if strings.Contains(body, `value="act_allow"`) {
		t.Fatal("still offers always-allow for an allowed action")
	}

	var ruleID string
	d.h.store.View(func(s *stateData) { ruleID = s.Rules[0].ID })
	d.post("/activity", url.Values{"op": {"rule_revoke"}, "rule_id": {ruleID}})
	if d.ruleCount() != 0 {
		t.Fatal("rule not revoked from activity")
	}

	// Ask-always actions cannot be allowed from the activity page.
	if _, err := d.h.allowFromActivity("web", "run: something never requested", "server", "t"); err == nil {
		t.Fatal("unknown action allowed")
	}
	d.approval(true) // logs an ask-always request for the same summary
	if _, err := d.h.allowFromActivity("web", a.Summary, "server", "t"); err == nil || d.ruleCount() != 0 {
		t.Fatal("ask-always action allowed from activity")
	}

	// Cross-site posts are refused.
	req, _ := http.NewRequest("POST", d.srv.URL+"/activity", strings.NewReader(url.Values{"op": {"act_allow"}, "server": {"web"}, "summary": {a.Summary}, "scope": {"server"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if resp, _ := d.c.Do(req); resp.StatusCode != http.StatusForbidden || d.ruleCount() != 0 {
		t.Fatalf("cross-site act_allow: %d", resp.StatusCode)
	}
}
