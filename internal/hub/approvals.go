package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/policy"
	"github.com/cstone1983/Claude-server-agent/internal/proto"
)

const (
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusDenied    = "denied"
	StatusExpired   = "expired"
	StatusExecuting = "executing"
	StatusExecuted  = "executed"
)

type Approval struct {
	ID         string          `json:"id"`
	AgentID    string          `json:"agent_id"`
	Server     string          `json:"server"`
	ServerTags []string        `json:"server_tags"`
	Tool       string          `json:"tool"`
	Args       json.RawMessage `json:"args"`
	Caller     string          `json:"caller"`
	Hash       string          `json:"action_hash"`
	Summary    string          `json:"summary"`
	Why        string          `json:"why"`
	Reason     string          `json:"reason,omitempty"`
	Suggested  policy.Rule     `json:"suggested_rule"`
	Status     string          `json:"status"`
	CreatedAt  time.Time       `json:"created_at"`
	DecidedAt  time.Time       `json:"decided_at,omitempty"`
	RuleID     string          `json:"rule_id,omitempty"`
	Result     *proto.Result   `json:"result,omitempty"`
	URL        string          `json:"url"`
	// Kind is "action" (run something on a server) or "rule" (Claude asks to
	// add an allow rule; approving adds it for RuleScope).
	Kind      string `json:"kind"`
	RuleScope string `json:"rule_scope,omitempty"`

	decided chan struct{}
	ran     chan struct{} // closed when an approved action finished running
	lastErr string
}

type approvals struct {
	mu sync.Mutex
	m  map[string]*Approval
}

func (a *approvals) get(id string) *Approval {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.m[id]
}

// snapshot copies an approval for safe reading.
func (a *approvals) snapshot(id string) (Approval, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ap := a.m[id]
	if ap == nil {
		return Approval{}, false
	}
	return *ap, true
}

func (a *approvals) list() []Approval {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Approval
	for _, ap := range a.m {
		out = append(out, *ap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (h *Hub) expireApprovals() {
	ttl := time.Duration(h.cfg.ApprovalTTLMin) * time.Minute
	h.appr.mu.Lock()
	defer h.appr.mu.Unlock()
	for id, ap := range h.appr.m {
		age := time.Since(ap.CreatedAt)
		if ap.Status == StatusPending && age > ttl {
			ap.Status = StatusExpired
			close(ap.decided)
		}
		if age > ttl+24*time.Hour {
			delete(h.appr.m, id)
		}
	}
}

func (h *Hub) newApproval(agent *AgentRec, call *proto.Call, na *proto.NeedsApproval) *Approval {
	var reason struct {
		Reason string `json:"reason"`
	}
	json.Unmarshal(call.Args, &reason)
	id := randomToken("apr_", 12)
	ap := &Approval{
		ID: id, AgentID: agent.ID, Server: agent.Name, ServerTags: agent.Tags, Tool: call.Tool, Args: call.Args,
		Caller: call.Caller, Hash: na.ActionHash, Summary: na.Summary, Why: na.Why, Reason: reason.Reason,
		Suggested: na.SuggestedRule, Status: StatusPending, CreatedAt: time.Now().UTC(),
		URL: h.cfg.PublicURL + "/approve/" + id, decided: make(chan struct{}), Kind: "action",
	}
	h.appr.mu.Lock()
	h.appr.m[id] = ap
	h.appr.mu.Unlock()
	h.audit.Write(audit.Entry{Event: "approval_requested", Server: agent.Name, Tool: call.Tool, Caller: call.Caller, Summary: na.Summary, ApprovalID: id})
	go h.notify(*ap)
	return ap
}

// Decide approves or denies a pending approval. If always is set, a rule is
// added for scope ("server", "tag:<t>" or "all") and pushed to agents.
func (h *Hub) Decide(id string, approve, always bool, scope, via string) (*Approval, error) {
	h.appr.mu.Lock()
	ap := h.appr.m[id]
	if ap == nil {
		h.appr.mu.Unlock()
		return nil, errors.New("no such approval (it may have expired)")
	}
	if ap.Status != StatusPending {
		st := ap.Status
		h.appr.mu.Unlock()
		return nil, fmt.Errorf("approval is already %s", st)
	}
	ap.DecidedAt = time.Now().UTC()
	if ap.Kind == "rule" {
		return h.decideRule(ap, approve, via)
	}
	if approve {
		ap.Status = StatusApproved
	} else {
		ap.Status = StatusDenied
	}
	close(ap.decided)
	snap := *ap
	h.appr.mu.Unlock()

	ev := "approval_denied"
	if approve {
		ev = "approval_granted"
	}
	h.audit.Write(audit.Entry{Event: ev, Server: snap.Server, Tool: snap.Tool, Summary: snap.Summary, ApprovalID: id, Detail: map[string]any{"always": always, "scope": scope, "via": via}})

	if approve && always {
		var s string
		switch {
		case scope == "" || scope == "server":
			s = "server:" + snap.Server
		case scope == "all":
			s = "all"
		case len(scope) > 4 && scope[:4] == "tag:":
			s = scope
		default:
			return &snap, fmt.Errorf("invalid scope %q", scope)
		}
		r := snap.Suggested
		r.Comment = "from approval of: " + truncate(snap.Summary, 120)
		ruleID, err := h.addRule(s, r, id)
		if err != nil {
			return &snap, err
		}
		h.appr.mu.Lock()
		ap.RuleID = ruleID
		h.appr.mu.Unlock()
	}
	return &snap, nil
}

// proposeRule records Claude's request to always allow something. Nothing is
// allowed until the user approves it.
func (h *Hub) proposeRule(caller Caller, r policy.Rule, scope, reason string) *Approval {
	id := randomToken("apr_", 12)
	ap := &Approval{
		ID: id, Server: scopeLabel(scope), Tool: "request_rule", Caller: caller.Name, Kind: "rule", RuleScope: scope,
		Summary: r.String() + "\non " + scopeLabel(scope), Why: "Claude asked to allow this without approval from now on",
		Reason: reason, Suggested: r, Status: StatusPending, CreatedAt: time.Now().UTC(),
		URL: h.cfg.PublicURL + "/approve/" + id, decided: make(chan struct{}),
	}
	h.appr.mu.Lock()
	h.appr.m[id] = ap
	h.appr.mu.Unlock()
	h.audit.Write(audit.Entry{Event: "rule_requested", Caller: caller.Name, Summary: ap.Summary, ApprovalID: id})
	go h.notify(*ap)
	return ap
}

// decideRule finishes a rule request. Called with h.appr.mu held.
func (h *Hub) decideRule(ap *Approval, approve bool, via string) (*Approval, error) {
	defer close(ap.decided)
	if !approve {
		ap.Status = StatusDenied
		snap := *ap
		h.appr.mu.Unlock()
		h.audit.Write(audit.Entry{Event: "rule_request_denied", Summary: snap.Summary, ApprovalID: snap.ID, Detail: map[string]string{"via": via}})
		return &snap, nil
	}
	r, scope := ap.Suggested, ap.RuleScope
	if r.Comment == "" {
		r.Comment = "requested by " + ap.Caller
	}
	h.appr.mu.Unlock()
	ruleID, err := h.addRule(scope, r, ap.ID)
	h.appr.mu.Lock()
	if err != nil {
		ap.Status = StatusDenied
		snap := *ap
		h.appr.mu.Unlock()
		return &snap, err
	}
	ap.Status, ap.RuleID = StatusExecuted, ruleID
	ap.Result = &proto.Result{OK: true, Text: "Rule added: " + r.String() + " on " + scopeLabel(scope) + ". It runs without approval from now on."}
	snap := *ap
	h.appr.mu.Unlock()
	return &snap, nil
}

// addRule stores a hub rule and pushes it to agents. An identical rule with
// the same scope is not added twice (e.g. when bulk-approving one command on
// several servers with scope "all"); its ID is returned instead.
func (h *Hub) addRule(scope string, r policy.Rule, approvalID string) (string, error) {
	var id string
	added := false
	err := h.store.Update(func(d *stateData) error {
		for _, x := range d.Rules {
			if x.Scope == scope && sameRule(x.Rule, r) {
				id = x.ID
				return nil
			}
		}
		hr := &HubRule{ID: randomID("rule_"), Scope: scope, Rule: r, ApprovalID: approvalID, CreatedAt: time.Now().UTC()}
		d.Rules = append(d.Rules, hr)
		id, added = hr.ID, true
		return nil
	})
	if err != nil {
		return "", err
	}
	if added {
		h.audit.Write(audit.Entry{Event: "rule_added", Summary: r.String(), Detail: map[string]string{"id": id, "scope": scope}})
		go h.pushRulesAll()
	}
	return id, nil
}

func sameRule(a, b policy.Rule) bool {
	a.ID, b.ID, a.Comment, b.Comment = "", "", "", ""
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// waitDecision waits up to d for the user to decide.
func (h *Hub) waitDecision(ctx context.Context, ap *Approval, d time.Duration) {
	select {
	case <-ap.decided:
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// executeApproved runs an approved action exactly once.
func (h *Hub) executeApproved(ctx context.Context, id string, maxTier string) (*proto.Result, string, error) {
	h.appr.mu.Lock()
	ap := h.appr.m[id]
	if ap == nil {
		h.appr.mu.Unlock()
		return nil, "", errors.New("no such approval")
	}
	switch ap.Status {
	case StatusExecuted:
		res := ap.Result
		h.appr.mu.Unlock()
		return res, StatusExecuted, nil
	case StatusExecuting:
		ran := ap.ran
		h.appr.mu.Unlock()
		if ran == nil {
			return nil, StatusExecuting, nil
		}
		return h.awaitApproved(ctx, ap, ran, executeWait)
	case StatusApproved:
		ap.Status = StatusExecuting
	default:
		st := ap.Status
		h.appr.mu.Unlock()
		return nil, st, nil
	}
	call := &proto.Call{Tool: ap.Tool, Args: ap.Args, Caller: ap.Caller, MaxTier: maxTier, Approval: &proto.Approval{ID: ap.ID, ActionHash: ap.Hash}}
	agentID := ap.AgentID
	ap.ran = make(chan struct{})
	ran := ap.ran
	h.appr.mu.Unlock()

	// Run detached from the caller: a client that stops waiting (tool-call
	// timeouts are about a minute) must not abort the action or let it run
	// twice. The result is kept for the next check_approval.
	go func() {
		defer close(ran)
		res, err := h.callAgent(context.Background(), agentID, call)
		h.appr.mu.Lock()
		defer h.appr.mu.Unlock()
		switch {
		case err == nil:
			ap.Status, ap.Result = StatusExecuted, res
		case errors.Is(err, errOffline) || errors.Is(err, errNotSent):
			ap.Status = StatusApproved // never reached the server: allow a retry
			ap.lastErr = err.Error()
		default:
			// It may have run (e.g. the agent disconnected mid-way): don't run it again.
			ap.Status = StatusExecuted
			ap.Result = &proto.Result{Error: "approved, but the result was lost: " + err.Error() + ". Check the server (e.g. its audit_log) before retrying."}
		}
	}()
	return h.awaitApproved(ctx, ap, ran, executeWait)
}

// executeWait is how long a caller waits for an approved action before
// getting "still running" (kept under typical tool-call timeouts).
var executeWait = 40 * time.Second

func (h *Hub) awaitApproved(ctx context.Context, ap *Approval, ran chan struct{}, wait time.Duration) (*proto.Result, string, error) {
	select {
	case <-ran:
	case <-time.After(wait):
	case <-ctx.Done():
	}
	h.appr.mu.Lock()
	defer h.appr.mu.Unlock()
	switch ap.Status {
	case StatusExecuted:
		return ap.Result, StatusExecuted, nil
	case StatusApproved:
		return nil, StatusApproved, errors.New(ap.lastErr)
	}
	return nil, ap.Status, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
