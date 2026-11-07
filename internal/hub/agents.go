package hub

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/policy"
	"github.com/cstone1983/Claude-server-agent/internal/proto"
)

const (
	pingInterval = 30 * time.Second
	writeWait    = 15 * time.Second
	readWait     = 90 * time.Second
)

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

type agentConn struct {
	id      string
	ws      *websocket.Conn
	wmu     sync.Mutex
	mu      sync.Mutex
	pending map[string]chan *proto.Result
	closed  chan struct{}
}

func (c *agentConn) send(env proto.Envelope) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.ws.SetWriteDeadline(time.Now().Add(writeWait))
	return c.ws.WriteJSON(env)
}

type registry struct {
	mu    sync.Mutex
	conns map[string]*agentConn // by agent ID
}

func (r *registry) get(id string) *agentConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conns[id]
}

func (r *registry) online(id string) bool { return r.get(id) != nil }

var upgrader = websocket.Upgrader{
	ReadBufferSize: 16 << 10, WriteBufferSize: 16 << 10,
	// Agents are not browsers; there is no Origin to check.
	CheckOrigin: func(*http.Request) bool { return true },
}

// handleEnroll exchanges a join token for an agent identity.
func (h *Hub) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip := clientIP(r)
	if !h.limiter.allow("enroll:" + ip) {
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}
	var req struct {
		JoinToken string   `json:"join_token"`
		Name      string   `json:"name"`
		Tags      []string `json:"tags"`
		PublicKey string   `json:"public_key"`
		Hostname  string   `json:"hostname"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pk, err := base64.StdEncoding.DecodeString(req.PublicKey)
	if err != nil || len(pk) != ed25519.PublicKeySize {
		http.Error(w, "invalid public key", http.StatusBadRequest)
		return
	}
	if !nameRE.MatchString(req.Name) {
		http.Error(w, "invalid name (letters, digits, . _ -; max 63)", http.StatusBadRequest)
		return
	}
	var tags []string
	for _, t := range req.Tags {
		t = strings.TrimSpace(t)
		if t != "" {
			if !nameRE.MatchString(t) {
				http.Error(w, "invalid tag "+t, http.StatusBadRequest)
				return
			}
			tags = append(tags, t)
		}
	}
	var rec *AgentRec
	var replaced string
	err = h.store.Update(func(d *stateData) error {
		jt := d.JoinTokens[hashSecret(req.JoinToken)]
		if jt == nil || time.Now().After(jt.ExpiresAt) || jt.UsesLeft <= 0 {
			return errors.New("invalid or expired join token")
		}
		jt.UsesLeft--
		tags = append(tags, jt.Tags...)
		for id, a := range d.Agents {
			if a.Name == req.Name {
				// Re-enrolling a server under the same name replaces it.
				replaced = id
				delete(d.Agents, id)
			}
		}
		rec = &AgentRec{ID: randomID("agt_"), Name: req.Name, Tags: dedupe(tags), PublicKey: req.PublicKey, Hostname: req.Hostname, EnrolledAt: time.Now().UTC()}
		d.Agents[rec.ID] = rec
		return nil
	})
	if err != nil {
		h.limiter.fail("enroll:" + ip)
		h.audit.Write(audit.Entry{Event: "enroll_failed", Server: req.Name, Error: err.Error(), Detail: map[string]string{"ip": ip}})
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if replaced != "" {
		if c := h.reg.get(replaced); c != nil {
			c.ws.Close()
		}
	}
	h.audit.Write(audit.Entry{Event: "agent_enrolled", Server: rec.Name, Detail: map[string]any{"id": rec.ID, "tags": rec.Tags, "ip": ip, "replaced": replaced}})
	writeJSON(w, http.StatusOK, map[string]string{"agent_id": rec.ID, "name": rec.Name})
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// handleAgentConnect authenticates an agent by Ed25519 signature and keeps
// its WebSocket open.
func (h *Hub) handleAgentConnect(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get(proto.HeaderAgentID)
	ts := r.Header.Get(proto.HeaderTimestamp)
	sig, _ := base64.StdEncoding.DecodeString(r.Header.Get(proto.HeaderSignature))
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(sec, 0)).Abs() > 2*time.Minute {
		http.Error(w, "stale or missing timestamp (check the server clock)", http.StatusUnauthorized)
		return
	}
	var rec AgentRec
	var ok bool
	h.store.View(func(d *stateData) {
		if a := d.Agents[id]; a != nil {
			rec, ok = *a, true
		}
	})
	if !ok {
		http.Error(w, "unknown agent (re-enroll with a join token)", http.StatusUnauthorized)
		return
	}
	pk, _ := base64.StdEncoding.DecodeString(rec.PublicKey)
	if len(pk) != ed25519.PublicKeySize || !ed25519.Verify(pk, proto.SigningPayload(id, ts), sig) {
		h.audit.Write(audit.Entry{Event: "agent_auth_failed", Server: rec.Name, Detail: map[string]string{"ip": clientIP(r)}})
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(16 << 20)
	c := &agentConn{id: id, ws: ws, pending: map[string]chan *proto.Result{}, closed: make(chan struct{})}

	h.reg.mu.Lock()
	if old := h.reg.conns[id]; old != nil {
		old.ws.Close()
	}
	h.reg.conns[id] = c
	h.reg.mu.Unlock()
	slog.Info("agent connected", "name", rec.Name, "ip", clientIP(r))
	h.audit.Write(audit.Entry{Event: "agent_connected", Server: rec.Name, Detail: map[string]string{"ip": clientIP(r)}})

	defer func() {
		h.reg.mu.Lock()
		if h.reg.conns[id] == c {
			delete(h.reg.conns, id)
		}
		h.reg.mu.Unlock()
		close(c.closed)
		ws.Close()
		h.touch(id, nil)
		slog.Info("agent disconnected", "name", rec.Name)
		h.audit.Write(audit.Entry{Event: "agent_disconnected", Server: rec.Name})
	}()

	h.pushRules(id)

	go func() {
		t := time.NewTicker(pingInterval)
		defer t.Stop()
		for {
			select {
			case <-c.closed:
				return
			case <-t.C:
				c.wmu.Lock()
				err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait))
				c.wmu.Unlock()
				if err != nil {
					ws.Close()
					return
				}
				h.touch(id, nil)
			}
		}
	}()

	ws.SetReadDeadline(time.Now().Add(readWait))
	ws.SetPongHandler(func(string) error { ws.SetReadDeadline(time.Now().Add(readWait)); return nil })
	for {
		var env proto.Envelope
		if err := ws.ReadJSON(&env); err != nil {
			return
		}
		ws.SetReadDeadline(time.Now().Add(readWait))
		switch env.Type {
		case proto.TypeHello:
			h.touch(id, env.Hello)
		case proto.TypeResult:
			c.mu.Lock()
			ch := c.pending[env.ID]
			delete(c.pending, env.ID)
			c.mu.Unlock()
			if ch != nil && env.Result != nil {
				ch <- env.Result
			}
		}
	}
}

func (h *Hub) touch(id string, hello *proto.Hello) {
	h.store.Update(func(d *stateData) error {
		a := d.Agents[id]
		if a == nil {
			return nil
		}
		a.LastSeen = time.Now().UTC()
		if hello != nil {
			a.Hostname, a.OS, a.Kernel, a.Arch, a.AgentVersion = hello.Hostname, hello.OS, hello.Kernel, hello.Arch, hello.AgentVersion
		}
		return nil
	})
}

var errOffline = errors.New("server is offline (its agent is not connected to the hub)")

// callAgent sends one call and waits for the result.
func (h *Hub) callAgent(ctx context.Context, agentID string, call *proto.Call) (*proto.Result, error) {
	c := h.reg.get(agentID)
	if c == nil {
		return nil, errOffline
	}
	id := randomID("c_")
	ch := make(chan *proto.Result, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.send(proto.Envelope{Type: proto.TypeCall, ID: id, Call: call}); err != nil {
		return nil, fmt.Errorf("sending to agent: %w", err)
	}
	timeout := time.Duration(h.cfg.CallTimeoutSec) * time.Second
	select {
	case res := <-ch:
		return res, nil
	case <-c.closed:
		return nil, errors.New("agent disconnected while running the command (it may still have run)")
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, errors.New("timed out waiting for the agent")
	}
}

// rulesFor returns the hub rules that apply to an agent.
func (h *Hub) rulesFor(a *AgentRec) []policy.Rule {
	var out []policy.Rule
	h.store.View(func(d *stateData) {
		for _, r := range d.Rules {
			if r.appliesTo(a) {
				rule := r.Rule
				rule.ID = r.ID
				out = append(out, rule)
			}
		}
	})
	return out
}

func (h *Hub) pushRules(agentID string) {
	var rec *AgentRec
	h.store.View(func(d *stateData) {
		if a := d.Agents[agentID]; a != nil {
			cp := *a
			rec = &cp
		}
	})
	c := h.reg.get(agentID)
	if rec == nil || c == nil {
		return
	}
	rules := h.rulesFor(rec)
	if rules == nil {
		rules = []policy.Rule{}
	}
	if err := c.send(proto.Envelope{Type: proto.TypeSetRules, Rules: rules}); err != nil {
		slog.Warn("pushing rules", "agent", rec.Name, "err", err)
	}
}

func (h *Hub) pushRulesAll() {
	var ids []string
	h.store.View(func(d *stateData) {
		for id := range d.Agents {
			ids = append(ids, id)
		}
	})
	for _, id := range ids {
		h.pushRules(id)
	}
}

// resolveTargets turns a server selector into agent records.
func (h *Hub) resolveTargets(sel string, allowed []string) ([]*AgentRec, error) {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return nil, errors.New(`"server" is required: a server name, "tag:<tag>" or "all" (see list_servers)`)
	}
	var out []*AgentRec
	h.store.View(func(d *stateData) {
		for _, a := range d.Agents {
			match := false
			switch {
			case sel == "all":
				match = true
			case strings.HasPrefix(sel, "tag:"):
				match = a.HasTag(sel[4:])
			default:
				match = a.Name == sel
			}
			if match && serverAllowed(a, allowed) {
				cp := *a
				out = append(out, &cp)
			}
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) == 0 {
		return nil, fmt.Errorf("no server matches %q (see list_servers)", sel)
	}
	return out, nil
}

// serverAllowed applies an API token's server restriction (names or tag:x).
func serverAllowed(a *AgentRec, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, s := range allowed {
		if s == a.Name || s == "all" || (strings.HasPrefix(s, "tag:") && a.HasTag(s[4:])) {
			return true
		}
	}
	return false
}
