package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/proto"
	"github.com/cstone1983/Claude-server-agent/internal/tools"
)

// Caller identifies who is invoking a tool.
type Caller struct {
	Name     string   // "claude:<client name>" or "api:<token name>"
	ReadOnly bool     // only read-tier actions
	Servers  []string // restrict to these servers/tags (empty = all)
}

func (c Caller) maxTier() string {
	if c.ReadOnly {
		return "read"
	}
	return ""
}

// ServerResult is one server's outcome.
type ServerResult struct {
	Server     string          `json:"server"`
	OK         bool            `json:"ok"`
	Text       string          `json:"text,omitempty"`
	Error      string          `json:"error,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	ApprovalID string          `json:"approval_id,omitempty"`
	Pending    bool            `json:"pending_approval,omitempty"`
}

// Outcome is the overall result of a tool invocation.
type Outcome struct {
	Text    string         `json:"text"`
	IsError bool           `json:"is_error"`
	Results []ServerResult `json:"results,omitempty"`
	Data    any            `json:"data,omitempty"`
}

// Invoke runs a tool for a caller. args is the raw JSON arguments including
// "server" for per-server tools.
func (h *Hub) Invoke(ctx context.Context, caller Caller, tool string, args json.RawMessage) Outcome {
	def, ok := tools.Get(tool)
	if !ok {
		return Outcome{Text: "unknown tool " + tool, IsError: true}
	}
	switch tool {
	case "list_servers":
		return h.listServers(caller)
	case "request_rule":
		return h.requestRule(caller, args)
	case "check_approval":
		var a struct {
			ApprovalID string `json:"approval_id"`
		}
		json.Unmarshal(args, &a)
		return h.checkApproval(ctx, caller, a.ApprovalID)
	}
	if def.HubOnly {
		return Outcome{Text: "not implemented", IsError: true}
	}

	var m map[string]json.RawMessage
	if len(args) > 0 {
		if err := json.Unmarshal(args, &m); err != nil {
			return Outcome{Text: "arguments must be a JSON object", IsError: true}
		}
	}
	var server string
	if raw, ok := m["server"]; ok {
		json.Unmarshal(raw, &server)
		delete(m, "server")
	}
	targets, err := h.resolveTargets(server, caller.Servers)
	if err != nil {
		return Outcome{Text: err.Error(), IsError: true}
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	agentArgs, _ := json.Marshal(m)

	results := make([]ServerResult, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t *AgentRec) {
			defer wg.Done()
			results[i] = h.invokeOne(ctx, caller, t, tool, agentArgs)
		}(i, t)
	}
	wg.Wait()
	return combine(results, len(targets) > 1 || strings.HasPrefix(server, "tag:") || server == "all")
}

func (h *Hub) invokeOne(ctx context.Context, caller Caller, agent *AgentRec, tool string, args json.RawMessage) ServerResult {
	call := &proto.Call{Tool: tool, Args: args, Caller: caller.Name, MaxTier: caller.maxTier()}
	sr := ServerResult{Server: agent.Name}
	res, err := h.callAgent(ctx, agent.ID, call)
	if err != nil {
		sr.Error = err.Error()
		return sr
	}
	if res.NeedsApproval == nil {
		return fromResult(sr, res)
	}
	ap := h.newApproval(agent, call, res.NeedsApproval)
	sr.ApprovalID = ap.ID
	h.waitDecision(ctx, ap, time.Duration(h.cfg.ApprovalWaitSec)*time.Second)
	snap, _ := h.appr.snapshot(ap.ID)
	switch snap.Status {
	case StatusApproved:
		res, _, err := h.executeApproved(ctx, ap.ID, caller.maxTier())
		if err != nil {
			sr.Error = "approved, but running it failed: " + err.Error() + ". Retry with check_approval approval_id=" + ap.ID
			return sr
		}
		sr = fromResult(sr, res)
		sr.Text = "(approved by user)\n" + sr.Text
		return sr
	case StatusDenied:
		sr.Error = "the user denied this action"
		return sr
	}
	sr.Pending = true
	sr.Text = fmt.Sprintf("NEEDS APPROVAL — not run yet.\nAction: %s\nWhy: %s\n\nAsk the user to approve it here: %s\nThen call check_approval with approval_id=%q.\n(Approving with \"always allow\" lets this exact action run without asking next time. All pending approvals can be reviewed together at %s/approvals)", snap.Summary, snap.Why, snap.URL, snap.ID, h.cfg.PublicURL)
	return sr
}

func fromResult(sr ServerResult, res *proto.Result) ServerResult {
	sr.OK, sr.Text, sr.Error, sr.Data = res.OK, res.Text, res.Error, res.Data
	return sr
}

func combine(results []ServerResult, multi bool) Outcome {
	out := Outcome{Results: results}
	if !multi && len(results) == 1 {
		r := results[0]
		out.Text = r.Text
		if r.Error != "" {
			out.Text = strings.TrimSpace("Error: " + r.Error + "\n" + r.Text)
			out.IsError = true
		}
		if len(r.Data) > 0 {
			out.Data = r.Data
		}
		return out
	}
	var sb strings.Builder
	errs := 0
	for _, r := range results {
		fmt.Fprintf(&sb, "=== %s ===\n", r.Server)
		if r.Error != "" {
			errs++
			sb.WriteString("Error: " + r.Error + "\n")
		}
		if r.Text != "" {
			sb.WriteString(r.Text + "\n")
		}
		sb.WriteString("\n")
	}
	out.Text = sb.String()
	out.IsError = errs == len(results)
	return out
}

func (h *Hub) listServers(caller Caller) Outcome {
	type srv struct {
		Name            string    `json:"name"`
		Online          bool      `json:"online"`
		Tags            []string  `json:"tags"`
		Hostname        string    `json:"hostname"`
		OS              string    `json:"os"`
		Arch            string    `json:"arch"`
		AgentVersion    string    `json:"agent_version"`
		LastSeen        time.Time `json:"last_seen"`
		LatestVersion   string    `json:"latest_version,omitempty"`
		UpdateAvailable bool      `json:"update_available"`
	}
	var list []srv
	h.store.View(func(d *stateData) {
		for _, a := range d.Agents {
			if !serverAllowed(a, caller.Servers) {
				continue
			}
			latest, upd := h.updateAvailable(a.AgentVersion)
			list = append(list, srv{a.Name, h.reg.online(a.ID), a.Tags, a.Hostname, a.OS, a.Arch, a.AgentVersion, a.LastSeen, latest, upd})
		}
	})
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	var sb strings.Builder
	updates := 0
	if len(list) == 0 {
		sb.WriteString("No servers have joined yet. On a server, run the install script with a join token from `csa-hub join-token`.")
	}
	for _, s := range list {
		st := "offline"
		if s.Online {
			st = "online"
		}
		fmt.Fprintf(&sb, "%s — %s — %s %s", s.Name, st, s.OS, s.Arch)
		if len(s.Tags) > 0 {
			fmt.Fprintf(&sb, " — tags: %s", strings.Join(s.Tags, ","))
		}
		if s.AgentVersion != "" {
			fmt.Fprintf(&sb, " — agent %s", versionOf(s.AgentVersion))
		}
		if s.UpdateAvailable {
			fmt.Fprintf(&sb, " — UPDATE AVAILABLE: %s", s.LatestVersion)
			updates++
		}
		if !s.Online && !s.LastSeen.IsZero() {
			fmt.Fprintf(&sb, " — last seen %s", s.LastSeen.Format(time.RFC3339))
		}
		sb.WriteString("\n")
	}
	if updates > 0 {
		fmt.Fprintf(&sb, "\n%d server(s) can be updated to %s. Tell the user, and run update_server only if they say to.\n", updates, h.releases.Latest())
	}
	if caller.ReadOnly {
		sb.WriteString("\n(This connection is read-only.)\n")
	}
	return Outcome{Text: sb.String(), Data: list}
}

func (h *Hub) checkApproval(ctx context.Context, caller Caller, id string) Outcome {
	snap, ok := h.appr.snapshot(id)
	if !ok {
		return Outcome{Text: "no such approval " + id + " (it may have expired)", IsError: true}
	}
	if snap.Status == StatusPending {
		// Give the user a moment if they are approving right now.
		ap := h.appr.get(id)
		h.waitDecision(ctx, ap, 20*time.Second)
		snap, _ = h.appr.snapshot(id)
	}
	switch snap.Status {
	case StatusPending:
		return Outcome{Text: fmt.Sprintf("Still waiting for the user to approve: %s\nApprove at: %s", snap.Summary, snap.URL)}
	case StatusDenied:
		return Outcome{Text: "The user denied: " + snap.Summary, IsError: true}
	case StatusExpired:
		return Outcome{Text: "This approval expired. Run the action again to request a new one.", IsError: true}
	case StatusExecuting:
		return Outcome{Text: "Approved; the action is running now. Call check_approval again shortly."}
	}
	res, st, err := h.executeApproved(ctx, id, caller.maxTier())
	if err != nil {
		h.audit.Write(audit.Entry{Event: "approved_run_failed", Server: snap.Server, ApprovalID: id, Error: err.Error()})
		return Outcome{Text: "Approved, but running it failed: " + err.Error(), IsError: true}
	}
	if res == nil {
		return Outcome{Text: "approval status: " + st}
	}
	sr := fromResult(ServerResult{Server: snap.Server}, res)
	prefix := "(approved by user"
	if snap.RuleID != "" {
		prefix += "; rule added so this runs without approval next time"
	}
	prefix += ")\n"
	if st == StatusExecuted && snap.Status == StatusExecuted {
		prefix = "(already ran; showing the stored result)\n"
	}
	sr.Text = prefix + sr.Text
	return combine([]ServerResult{sr}, false)
}

func (h *Hub) requestRule(caller Caller, args json.RawMessage) Outcome {
	if caller.ReadOnly {
		return Outcome{Text: "this connection is read-only", IsError: true}
	}
	var a struct {
		Kind    string `json:"kind"`
		Value   string `json:"value"`
		AnyArgs bool   `json:"any_args"`
		Servers string `json:"servers"`
		As      string `json:"as"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Outcome{Text: "invalid arguments: " + err.Error(), IsError: true}
	}
	if a.Kind == "" {
		a.Kind = "command"
	}
	scope := strings.TrimSpace(a.Servers)
	switch {
	case scope == "" || scope == "all":
		scope = "all"
	case strings.HasPrefix(scope, "tag:"):
	default:
		if _, err := h.resolveTargets(scope, caller.Servers); err != nil {
			return Outcome{Text: err.Error(), IsError: true}
		}
		scope = "server:" + scope
	}
	rule, err := ruleFromForm(a.Kind, a.Value, a.AnyArgs, a.As)
	if err != nil {
		return Outcome{Text: err.Error(), IsError: true}
	}
	ap := h.proposeRule(caller, rule, scope, a.Reason)
	return Outcome{Text: fmt.Sprintf("Rule requested (not active yet): %s on %s.\nAsk the user to approve it at %s/approvals (or %s), then call check_approval with approval_id=%q.", rule.String(), scopeLabel(scope), h.cfg.PublicURL, ap.URL, ap.ID)}
}
