package hub

import (
	"errors"
	"fmt"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
)

// "Recent decisions" on the approvals page: turn a past action into an
// "always allow" rule, or revoke the rule that allows it now.

// recentView is one row of the recent decisions table.
type recentView struct {
	Approval
	// RuleID/RuleScope: the enabled rule that allows this action on its
	// server right now (whether or not this approval created it).
	RuleID, RuleScope string
	// CanAllow: the action can be turned into an "always allow" rule.
	CanAllow bool
}

// ruleScopeFor turns a form scope ("server", "all", "tag:x") into a rule
// scope for the approval's server.
func ruleScopeFor(ap Approval, scope string) (string, error) {
	switch {
	case scope == "" || scope == "server":
		return "server:" + ap.Server, nil
	case scope == "all":
		return "all", nil
	case len(scope) > 4 && scope[:4] == "tag:":
		return scope, nil
	}
	return "", fmt.Errorf("invalid scope %q", scope)
}

// canAlwaysAllow reports whether a past approval can become a rule.
func canAlwaysAllow(ap Approval) bool {
	return ap.Kind != "rule" && !ap.AskAlways && ap.Status != StatusPending && ap.Suggested.String() != ""
}

// allowFromHistory adds an "always allow" rule for an approval that was
// already decided (approved, denied or expired).
func (h *Hub) allowFromHistory(id, scope, via string) (string, error) {
	snap, ok := h.appr.snapshot(id)
	if !ok {
		return "", errors.New("that request is no longer in the recent list")
	}
	if !canAlwaysAllow(snap) {
		if snap.AskAlways {
			return "", errors.New("this action needs its own OK every time and cannot be always-allowed")
		}
		return "", errors.New("this request cannot be turned into an allow rule")
	}
	s, err := ruleScopeFor(snap, scope)
	if err != nil {
		return "", err
	}
	r := snap.Suggested
	r.Comment = "from approval of: " + truncate(snap.Summary, 120)
	ruleID, err := h.addRule(s, r, id)
	if err != nil {
		return "", err
	}
	h.appr.mu.Lock()
	if ap := h.appr.m[id]; ap != nil {
		ap.RuleID = ruleID
	}
	h.appr.mu.Unlock()
	h.audit.Write(audit.Entry{Event: "rule_from_history", Server: snap.Server, Tool: snap.Tool, Summary: snap.Summary, ApprovalID: id,
		Detail: map[string]string{"rule_id": ruleID, "scope": s, "via": via}})
	return s, nil
}

// revokeRule deletes an allow rule; the action needs approval again.
func (h *Hub) revokeRule(ruleID, via string) (string, bool) {
	var scope string
	found := false
	h.store.Update(func(d *stateData) error {
		for i, x := range d.Rules {
			if x.ID == ruleID {
				scope = x.Scope
				d.Rules = append(d.Rules[:i], d.Rules[i+1:]...)
				found = true
				break
			}
		}
		return nil
	})
	if !found {
		return "", false
	}
	h.appr.mu.Lock()
	for _, ap := range h.appr.m {
		if ap.RuleID == ruleID {
			ap.RuleID = ""
		}
	}
	h.appr.mu.Unlock()
	h.audit.Write(audit.Entry{Event: "rule_deleted", Detail: map[string]string{"id": ruleID, "via": via}})
	go h.pushRulesAll()
	return scope, true
}

// recentDecisions returns the last n decided approvals with the rule (if
// any) that allows each one now.
func (h *Hub) recentDecisions(n int) []recentView {
	var rules []HubRule
	h.store.View(func(d *stateData) {
		for _, x := range d.Rules {
			if !x.Disabled {
				rules = append(rules, *x)
			}
		}
	})
	var out []recentView
	for _, a := range h.appr.list() {
		if a.Status == StatusPending {
			continue
		}
		if len(out) >= n {
			break
		}
		v := recentView{Approval: a, CanAllow: canAlwaysAllow(a)}
		if a.Kind != "rule" {
			agent := &AgentRec{Name: a.Server, Tags: a.ServerTags}
			for i := range rules {
				x := &rules[i]
				if x.appliesTo(agent) && sameRule(x.Rule, a.Suggested) {
					v.RuleID, v.RuleScope = x.ID, scopeLabel(x.Scope)
					if x.ID == a.RuleID {
						break // prefer the rule this approval created
					}
				}
			}
		}
		out = append(out, v)
	}
	return out
}
