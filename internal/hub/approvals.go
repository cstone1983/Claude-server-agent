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

	decided chan struct{}
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
		URL: h.cfg.PublicURL + "/approve/" + id, decided: make(chan struct{}),
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
		rule := &HubRule{ID: randomID("rule_"), Scope: s, Rule: snap.Suggested, ApprovalID: id, CreatedAt: time.Now().UTC()}
		rule.Rule.Comment = "from approval of: " + truncate(snap.Summary, 120)
		if err := h.store.Update(func(d *stateData) error { d.Rules = append(d.Rules, rule); return nil }); err != nil {
			return &snap, err
		}
		h.appr.mu.Lock()
		ap.RuleID = rule.ID
		h.appr.mu.Unlock()
		h.audit.Write(audit.Entry{Event: "rule_added", Summary: rule.Rule.String(), Detail: map[string]string{"id": rule.ID, "scope": s}})
		go h.pushRulesAll()
	}
	return &snap, nil
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
	case StatusApproved:
		ap.Status = StatusExecuting
	default:
		st := ap.Status
		h.appr.mu.Unlock()
		return nil, st, nil
	}
	call := &proto.Call{Tool: ap.Tool, Args: ap.Args, Caller: ap.Caller, MaxTier: maxTier, Approval: &proto.Approval{ID: ap.ID, ActionHash: ap.Hash}}
	agentID := ap.AgentID
	h.appr.mu.Unlock()

	res, err := h.callAgent(ctx, agentID, call)
	h.appr.mu.Lock()
	defer h.appr.mu.Unlock()
	if err != nil {
		ap.Status = StatusApproved // allow retry, e.g. if the server was offline
		return nil, StatusApproved, err
	}
	ap.Status = StatusExecuted
	ap.Result = res
	return res, StatusExecuted, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
