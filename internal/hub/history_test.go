package hub

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func (d *devTest) ruleCount() int {
	n := 0
	d.h.store.View(func(s *stateData) { n = len(s.Rules) })
	return n
}

func TestRecentDecisionsAllowAndRevoke(t *testing.T) {
	d := newDevTest(t)
	// Log in (remembered device keeps it simple).
	d.post("/login", url.Values{"password": {"pw-123456789"}, "code": {d.code()}, "next": {"/approvals"}, "remember": {"1"}})

	// A decided action without a rule shows an "Always allow" button.
	a := d.approval(false)
	d.post("/approve/"+a.ID, url.Values{"decision": {"approve"}})
	if d.status(a.ID) != StatusApproved {
		t.Fatal("setup: not approved")
	}
	resp, _ := d.c.Get(d.srv.URL + "/approvals")
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `value="history_allow"`) {
		t.Fatalf("no always-allow button in recent decisions:\n%s", b)
	}

	// Always allow it from history.
	_, body := d.post("/approvals", url.Values{"op": {"history_allow"}, "approval_id": {a.ID}, "scope": {"server"}})
	if d.ruleCount() != 1 || !strings.Contains(body, "Always allowed on web") {
		t.Fatalf("rule not added from history (rules=%d):\n%s", d.ruleCount(), body)
	}
	rv := d.h.recentDecisions(15)
	if len(rv) == 0 || rv[0].RuleID == "" || rv[0].RuleScope != "web" {
		t.Fatalf("recent view does not show the rule: %+v", rv)
	}
	// Adding it again does not duplicate the rule.
	d.post("/approvals", url.Values{"op": {"history_allow"}, "approval_id": {a.ID}, "scope": {"server"}})
	if d.ruleCount() != 1 {
		t.Fatal("duplicate rule")
	}
	if !strings.Contains(body, `value="rule_revoke"`) {
		t.Fatalf("no revoke button after allowing:\n%s", body)
	}

	// A second, later approval of the same action is shown as covered by the rule.
	a2 := d.approval(false)
	d.post("/approve/"+a2.ID, url.Values{"decision": {"deny"}})
	if rv := d.h.recentDecisions(15); rv[0].ID != a2.ID || rv[0].RuleID == "" {
		t.Fatalf("matching rule not shown for a later row: %+v", rv[0])
	}

	// Revoke it.
	_, body = d.post("/approvals", url.Values{"op": {"rule_revoke"}, "rule_id": {rv[0].RuleID}})
	if d.ruleCount() != 0 || !strings.Contains(body, "revoked") {
		t.Fatalf("rule not revoked (rules=%d):\n%s", d.ruleCount(), body)
	}
	for _, v := range d.h.recentDecisions(15) {
		if v.RuleID != "" {
			t.Fatal("revoked rule still shown")
		}
	}

	// Ask-always actions cannot be always-allowed from history.
	boot := d.approval(true)
	d.post("/approve/"+boot.ID, url.Values{"decision": {"deny"}, "code": {d.code()}})
	if _, err := d.h.allowFromHistory(boot.ID, "server", "test"); err == nil || d.ruleCount() != 0 {
		t.Fatal("ask-always action was always-allowed from history")
	}
	// Pending requests are not in history.
	p := d.approval(false)
	if _, err := d.h.allowFromHistory(p.ID, "server", "test"); err == nil {
		t.Fatal("pending request allowed from history")
	}
	// Cross-site posts are refused.
	req, _ := http.NewRequest("POST", d.srv.URL+"/approvals", strings.NewReader(url.Values{"op": {"history_allow"}, "approval_id": {a.ID}, "scope": {"all"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if resp, _ := d.c.Do(req); resp.StatusCode != http.StatusForbidden || d.ruleCount() != 0 {
		t.Fatalf("cross-site history_allow: %d, rules=%d", resp.StatusCode, d.ruleCount())
	}
}
