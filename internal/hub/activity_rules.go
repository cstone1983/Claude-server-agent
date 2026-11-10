package hub

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"encoding/json"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/policy"
)

// "Always allow" / "Revoke" on the activity page. Works from the hub's audit
// log, so it survives hub restarts: every approval request logs the rule that
// would allow it (requestDetail), and agents log the id of the hub rule that
// allowed a command.

// requestDetail is the Detail of an approval_requested audit entry.
type requestDetail struct {
	Rule      policy.Rule `json:"rule"`
	AskAlways bool        `json:"ask_always,omitempty"`
}

// knownAction is what the hub knows about an action that once needed approval.
type knownAction struct {
	Rule      policy.Rule
	AskAlways bool
}

// activityScanLines bounds how far back the hub log is read to find rules.
const activityScanLines = 20000

func actionKey(server, summary string) string { return server + "\x00" + summary }

// knownActions maps server+summary to the rule that would allow it, from the
// hub's audit log and the approvals still in memory (newest wins).
func (h *Hub) knownActions() map[string]knownAction {
	out := map[string]knownAction{}
	lines, err := audit.Tail(h.audit.Path(), activityScanLines)
	if err != nil && !os.IsNotExist(err) {
		return out
	}
	for _, l := range lines { // oldest first, so newer entries overwrite
		var e struct {
			Event   string        `json:"event"`
			Server  string        `json:"server"`
			Summary string        `json:"summary"`
			Detail  requestDetail `json:"detail"`
		}
		if json.Unmarshal(l, &e) != nil || e.Event != "approval_requested" || e.Server == "" {
			continue
		}
		if e.Detail.Rule.String() == "" && !e.Detail.AskAlways {
			continue // logged before rules were recorded
		}
		out[actionKey(e.Server, e.Summary)] = knownAction{Rule: e.Detail.Rule, AskAlways: e.Detail.AskAlways}
	}
	for _, a := range h.appr.list() {
		if a.Kind == "rule" {
			continue
		}
		k := actionKey(a.Server, a.Summary)
		if _, ok := out[k]; !ok {
			out[k] = knownAction{Rule: a.Suggested, AskAlways: a.AskAlways}
		}
	}
	return out
}

// activityRules fills in the always-allow controls for activity rows.
func (h *Hub) activityRules(entries []activityEntry) {
	known := h.knownActions()
	var rules []HubRule
	agents := map[string]*AgentRec{}
	h.store.View(func(d *stateData) {
		for _, x := range d.Rules {
			if !x.Disabled {
				rules = append(rules, *x)
			}
		}
		for _, a := range d.Agents {
			cp := *a
			agents[a.Name] = &cp
		}
	})
	for i := range entries {
		e := &entries[i]
		if e.Server == "" || e.FullSummary == "" {
			continue
		}
		agent := agents[e.Server]
		if agent != nil {
			e.ServerTags = agent.Tags
		}
		// Allowed by a hub rule that still exists: offer to revoke it.
		if e.RuleID != "" {
			for _, x := range rules {
				if x.ID == e.RuleID {
					e.ActiveRuleID, e.ActiveRuleScope = x.ID, scopeLabel(x.Scope)
				}
			}
			if e.ActiveRuleID != "" {
				continue
			}
		}
		k, ok := known[actionKey(e.Server, e.FullSummary)]
		if !ok || agent == nil {
			continue
		}
		if k.AskAlways {
			e.AskAlways = true
			continue
		}
		for _, x := range rules {
			if x.appliesTo(agent) && sameRule(x.Rule, k.Rule) {
				e.ActiveRuleID, e.ActiveRuleScope = x.ID, scopeLabel(x.Scope)
				break
			}
		}
		e.CanAllow = e.ActiveRuleID == ""
	}
}

// allowFromActivity adds an "always allow" rule for an action seen in the log.
func (h *Hub) allowFromActivity(server, summary, scope, via string) (string, error) {
	k, ok := h.knownActions()[actionKey(server, summary)]
	if !ok {
		return "", errors.New("the rule for this action is not known (it was requested before rules were recorded)")
	}
	if k.AskAlways {
		return "", errors.New("this action needs its own OK every time and cannot be always-allowed")
	}
	agent, ok := h.agentByName(server)
	if !ok {
		return "", errors.New("no such server")
	}
	s, err := ruleScopeFor(Approval{Server: agent.Name}, scope)
	if err != nil {
		return "", err
	}
	r := k.Rule
	r.Comment = "from activity: " + truncate(summary, 120)
	ruleID, err := h.addRule(s, r, "")
	if err != nil {
		return "", err
	}
	h.audit.Write(audit.Entry{Event: "rule_from_activity", Server: server, Summary: summary,
		Detail: map[string]string{"rule_id": ruleID, "scope": s, "via": via}})
	return s, nil
}

// handleActivityPost runs the activity page's buttons, then returns to the
// same view (POST-redirect-GET).
func (h *Hub) handleActivityPost(w http.ResponseWriter, r *http.Request) {
	via := "dashboard:" + clientIP(r)
	back := url.Values{}
	if s := r.PostFormValue("view_server"); s != "" {
		back.Set("server", s)
	}
	if n, _ := strconv.Atoi(r.PostFormValue("view_n")); n > 0 {
		back.Set("n", strconv.Itoa(n))
	}
	switch r.PostFormValue("op") {
	case "act_allow":
		if s, err := h.allowFromActivity(r.PostFormValue("server"), r.PostFormValue("summary"), r.PostFormValue("scope"), via); err != nil {
			back.Set("error", err.Error())
		} else {
			back.Set("done", "Always allowed on "+scopeLabel(s)+": next time it runs without asking.")
		}
	case "rule_revoke":
		if s, ok := h.revokeRule(r.PostFormValue("rule_id"), via); ok {
			back.Set("done", "Always-allow revoked ("+scopeLabel(s)+"): it needs approval again.")
		} else {
			back.Set("error", "That rule no longer exists.")
		}
	default:
		back.Set("error", "unknown action")
	}
	http.Redirect(w, r, "/activity?"+back.Encode(), http.StatusSeeOther)
}
