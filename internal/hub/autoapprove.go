package hub

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
)

// Hands-off mode: for a limited time, actions that would need approval are
// approved automatically, so the user can step away while Claude works on a
// project. It never covers:
//   - requests that need their own OK (AskAlways: e.g. rebooting a Proxmox
//     host),
//   - rule requests ("always allow" rules still need a real decision),
//   - anything the server's policy denies (denials never reach approval).
// Proxmox hosts are excluded unless the user includes them explicitly.

// AutoApprove is the hands-off mode setting, stored in the hub state.
type AutoApprove struct {
	Until time.Time `json:"until"`
	// Scope: "all" or "server:<name>".
	Scope string `json:"scope"`
	// IncludeCareful also covers servers tagged "proxmox" (still never their
	// reboots, which need their own OK).
	IncludeCareful bool      `json:"include_careful,omitempty"`
	SetAt          time.Time `json:"set_at"`
	SetBy          string    `json:"set_by,omitempty"`
}

// maxAutoApprove caps how long hands-off mode can be on at once.
const maxAutoApprove = 12 * time.Hour

func (a *AutoApprove) active(now time.Time) bool {
	return a != nil && now.Before(a.Until)
}

// covers reports whether hands-off mode approves an action on agent.
func (a *AutoApprove) covers(agent *AgentRec, now time.Time) bool {
	if !a.active(now) || agent == nil {
		return false
	}
	if agent.HasTag(carefulTag) && !a.IncludeCareful {
		return false
	}
	switch {
	case a.Scope == "all":
		return true
	case strings.HasPrefix(a.Scope, "server:"):
		return a.Scope[7:] == agent.Name
	}
	return false
}

func (a *AutoApprove) label() string {
	s := scopeLabel(a.Scope)
	if a.Scope == "all" {
		if a.IncludeCareful {
			s = "all servers, including Proxmox hosts"
		} else {
			s = "all servers except Proxmox hosts"
		}
	}
	return s
}

// autoApprove returns the current hands-off setting (nil when off/expired).
func (h *Hub) autoApprove() *AutoApprove {
	var out *AutoApprove
	h.store.View(func(d *stateData) {
		if d.AutoApprove.active(time.Now()) {
			c := *d.AutoApprove
			out = &c
		}
	})
	return out
}

// autoApproveCovers reports whether a new action approval should be granted
// automatically.
func (h *Hub) autoApproveCovers(agent *AgentRec, askAlways bool) bool {
	if askAlways {
		return false
	}
	a := h.autoApprove()
	return a != nil && a.covers(agent, time.Now())
}

// setAutoApprove turns hands-off mode on for d (capped) and approves pending
// requests it covers. It returns how many pending requests were approved.
func (h *Hub) setAutoApprove(d time.Duration, scope string, includeCareful bool, via string) (int, error) {
	if d <= 0 || d > maxAutoApprove {
		return 0, fmt.Errorf("choose a duration up to %s", maxAutoApprove)
	}
	if scope != "all" && !(strings.HasPrefix(scope, "server:") && len(scope) > 7) {
		return 0, errors.New("choose which servers it covers")
	}
	if strings.HasPrefix(scope, "server:") {
		found := false
		h.store.View(func(sd *stateData) {
			for _, a := range sd.Agents {
				if a.Name == scope[7:] {
					found = true
				}
			}
		})
		if !found {
			return 0, errors.New("unknown server " + scope[7:])
		}
	}
	now := time.Now().UTC()
	aa := &AutoApprove{Until: now.Add(d), Scope: scope, IncludeCareful: includeCareful, SetAt: now, SetBy: via}
	if err := h.store.Update(func(sd *stateData) error { sd.AutoApprove = aa; return nil }); err != nil {
		return 0, err
	}
	h.audit.Write(audit.Entry{Event: "auto_approve_on", Summary: aa.label(), Detail: map[string]any{"until": aa.Until, "scope": scope, "include_proxmox": includeCareful, "via": via}})

	// Approve what is already waiting and covered.
	n := 0
	for _, ap := range h.appr.list() {
		if ap.Status != StatusPending || ap.Kind != "action" || ap.AskAlways {
			continue
		}
		agent := h.agentByID(ap.AgentID)
		if !aa.covers(agent, time.Now()) {
			continue
		}
		if _, err := h.Decide(ap.ID, true, false, "", "auto-approve"); err == nil {
			n++
		}
	}
	return n, nil
}

func (h *Hub) clearAutoApprove(via string) {
	was := false
	h.store.Update(func(sd *stateData) error {
		was = sd.AutoApprove.active(time.Now())
		sd.AutoApprove = nil
		return nil
	})
	if was {
		h.audit.Write(audit.Entry{Event: "auto_approve_off", Detail: map[string]string{"via": via}})
	}
}

// agentByID returns a copy of the agent record, or nil.
func (h *Hub) agentByID(id string) *AgentRec {
	var out *AgentRec
	h.store.View(func(d *stateData) {
		if a := d.Agents[id]; a != nil {
			c := *a
			out = &c
		}
	})
	return out
}
