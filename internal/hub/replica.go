package hub

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
)

// A primary/standby pair. The standby copies the primary's state every few
// seconds and forwards every request to it, so there is only ever one hub
// doing the work. When the primary has not answered for takeover_after_sec,
// the standby serves by itself from its copy. When the primary answers
// again, the standby first merges what changed meanwhile (new logins, rules,
// enrolled servers, hands-off mode) into the primary, then goes back to
// forwarding.

const replicaPollEvery = 15 * time.Second

type replica struct {
	h      *Hub
	cfg    ReplicaConfig
	secret string
	peer   *url.URL
	proxy  *httputil.ReverseProxy
	client *http.Client

	active     atomic.Bool // standby: serving by itself
	mu         sync.Mutex
	lastPeerOK time.Time
	lastError  string
}

type replicaStatus struct {
	Role   string          `json:"role"`
	Active bool            `json:"active"`
	State  json.RawMessage `json:"state,omitempty"`
}

func newReplica(h *Hub, cfg ReplicaConfig) (*replica, error) {
	if cfg.Role == "" {
		return nil, nil
	}
	if cfg.Role != "primary" && cfg.Role != "standby" {
		return nil, fmt.Errorf("replica.role must be primary or standby, not %q", cfg.Role)
	}
	b, err := os.ReadFile(cfg.SecretFile)
	if err != nil {
		return nil, fmt.Errorf("replica.secret_file: %w", err)
	}
	secret := strings.TrimSpace(string(b))
	if len(secret) < 24 {
		return nil, errors.New("replica secret is too short (use at least 24 random characters)")
	}
	r := &replica{h: h, cfg: cfg, secret: secret, client: &http.Client{Timeout: 10 * time.Second}}
	if cfg.Role == "standby" {
		if cfg.PeerURL == "" {
			return nil, errors.New("replica.peer_url is required on a standby")
		}
		r.peer, err = url.Parse(strings.TrimRight(cfg.PeerURL, "/"))
		if err != nil || r.peer.Host == "" {
			return nil, fmt.Errorf("replica.peer_url: invalid %q", cfg.PeerURL)
		}
		r.proxy = r.newProxy()
	}
	return r, nil
}

func (r *replica) standby() bool { return r != nil && r.cfg.Role == "standby" }

// forwarding reports whether requests go to the primary.
func (r *replica) forwarding() bool { return r.standby() && !r.active.Load() }

func (r *replica) newProxy() *httputil.ReverseProxy {
	public := r.h.cfg.publicHost()
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(r.peer)
			pr.Out.Host = public
			// Pass on who the client really is; the primary trusts these
			// headers from this standby (replica.trusted_proxies).
			ip := clientIP(pr.In)
			pr.Out.Header.Set("X-Forwarded-For", ip)
			if pr.In.Header.Get("CF-Connecting-IP") != "" {
				pr.Out.Header.Set("CF-Connecting-IP", ip)
			}
			pr.Out.Header.Set("X-Forwarded-Proto", "https")
		},
		FlushInterval: -1, // stream MCP/SSE responses straight through
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			slog.Warn("standby: primary hub not reachable", "err", err)
			http.Error(w, "the primary hub is not reachable; the standby hub takes over if it stays down", http.StatusBadGateway)
		},
	}
}

func (r *replica) authorized(req *http.Request) bool {
	// Never over the public tunnel: replication is LAN only.
	if req.Header.Get("CF-Connecting-IP") != "" {
		return false
	}
	got := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(got), []byte(r.secret)) == 1
}

// handle serves /replica/state (GET) and /replica/merge (POST).
func (r *replica) handle(w http.ResponseWriter, req *http.Request) {
	if !r.authorized(req) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/replica/state":
		b, err := r.h.store.SnapshotJSON()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, replicaStatus{Role: r.cfg.Role, Active: r.serving(), State: b})
	case req.Method == http.MethodPost && req.URL.Path == "/replica/merge":
		var peer stateData
		if err := json.NewDecoder(io.LimitReader(req.Body, 64<<20)).Decode(&peer); err != nil {
			http.Error(w, "bad state: "+err.Error(), http.StatusBadRequest)
			return
		}
		n, err := r.h.mergeState(&peer)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		r.h.audit.Write(audit.Entry{Event: "replica_merged", Detail: map[string]any{"from": clientIP(req), "changes": n}})
		go r.h.pushRulesAll()
		writeJSON(w, http.StatusOK, map[string]int{"changes": n})
	default:
		http.NotFound(w, req)
	}
}

// serving reports whether this hub is the one doing the work right now.
func (r *replica) serving() bool { return r == nil || !r.standby() || r.active.Load() }

func (r *replica) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.peer.String()+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.secret)
	req.Header.Set("Content-Type", "application/json")
	req.Host = r.h.cfg.publicHost()
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("%s %s: HTTP %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

// run is the standby's loop.
func (r *replica) run(ctx context.Context) {
	r.mu.Lock()
	r.lastPeerOK = time.Now() // give the primary the full grace period at start
	r.mu.Unlock()
	t := time.NewTicker(replicaPollEvery)
	defer t.Stop()
	for {
		r.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *replica) tick(ctx context.Context) {
	st, err := r.fetch(ctx)
	if err == nil && st.Role == "primary" {
		r.mu.Lock()
		r.lastPeerOK, r.lastError = time.Now(), ""
		r.mu.Unlock()
		if r.active.Load() {
			// Primary is back: hand over what changed here first.
			b, err := r.h.store.SnapshotJSON()
			if err == nil {
				var resp *http.Response
				if resp, err = r.do(ctx, http.MethodPost, "/replica/merge", b); err == nil {
					resp.Body.Close()
				}
			}
			if err != nil {
				slog.Error("standby: merging into the primary failed; still serving", "err", err)
				r.setError(err)
				return
			}
			r.active.Store(false)
			slog.Info("standby: primary hub is back; forwarding to it again")
			r.h.audit.Write(audit.Entry{Event: "standby_forwarding", Detail: map[string]string{"peer": r.peer.String()}})
			r.h.dropAgents() // they reconnect and are forwarded to the primary
			// Take the merged state on the next poll.
			return
		}
		var d stateData
		if err := json.Unmarshal(st.State, &d); err != nil {
			r.setError(err)
			return
		}
		if err := r.h.store.Replace(d); err != nil {
			r.setError(err)
		}
		return
	}
	if err == nil {
		err = fmt.Errorf("peer reports role %q, expected primary", st.Role)
	}
	r.setError(err)
	r.mu.Lock()
	down := time.Since(r.lastPeerOK)
	r.mu.Unlock()
	if !r.active.Load() && down > time.Duration(r.cfg.TakeoverAfterSec)*time.Second {
		r.active.Store(true)
		slog.Warn("standby: primary hub down; this hub is serving now", "down_for", down.Round(time.Second), "err", err)
		r.h.audit.Write(audit.Entry{Event: "standby_active", Detail: map[string]string{"peer": r.peer.String(), "error": err.Error()}})
	}
}

func (r *replica) setError(err error) {
	r.mu.Lock()
	r.lastError = err.Error()
	r.mu.Unlock()
}

func (r *replica) fetch(ctx context.Context) (*replicaStatus, error) {
	resp, err := r.do(ctx, http.MethodGet, "/replica/state", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var st replicaStatus
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

// healthText is shown on /healthz.
func (r *replica) healthText() string {
	if r == nil {
		return "ok\n"
	}
	if !r.standby() {
		return "ok primary\n"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active.Load() {
		return "ok standby serving (primary down: " + r.lastError + ")\n"
	}
	return "ok standby forwarding to primary\n"
}

// gate forwards requests to the primary while this standby is passive.
// Replication and health requests are always answered locally.
func (h *Hub) replicaGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/replica/") {
			if h.replica == nil {
				http.NotFound(w, req)
				return
			}
			h.replica.handle(w, req)
			return
		}
		if req.URL.Path != "/healthz" && h.replica.forwarding() {
			h.replica.proxy.ServeHTTP(w, req)
			return
		}
		next.ServeHTTP(w, req)
	})
}

// dropAgents closes every agent connection.
func (h *Hub) dropAgents() {
	h.reg.mu.Lock()
	defer h.reg.mu.Unlock()
	for _, c := range h.reg.conns {
		if c.ws != nil {
			c.ws.Close()
		}
	}
}

// mergeState folds a standby's state into this one after a takeover. It
// only adds and updates (deletions made on the standby during the outage are
// not carried over). It returns how many things changed.
func (h *Hub) mergeState(p *stateData) (int, error) {
	n := 0
	err := h.store.Update(func(d *stateData) error {
		for id, a := range p.Agents {
			cur := d.Agents[id]
			switch {
			case cur == nil:
				d.Agents[id] = a
				n++
			case a.EnrolledAt.After(cur.EnrolledAt):
				d.Agents[id] = a
				n++
			case a.LastSeen.After(cur.LastSeen):
				cur.LastSeen = a.LastSeen
			}
		}
		for k, t := range p.JoinTokens {
			if cur := d.JoinTokens[k]; cur == nil {
				d.JoinTokens[k] = t
				n++
			} else if t.UsesLeft < cur.UsesLeft {
				cur.UsesLeft = t.UsesLeft
				n++
			}
		}
		for k, c := range p.Clients {
			if d.Clients[k] == nil {
				d.Clients[k] = c
				n++
			}
		}
		for k, t := range p.Tokens {
			if cur := d.Tokens[k]; cur == nil {
				d.Tokens[k] = t
				n++
			} else if t.LastUsed.After(cur.LastUsed) {
				cur.LastUsed = t.LastUsed
			}
		}
		for k, e := range p.Sessions {
			if cur, ok := d.Sessions[k]; !ok || e.After(cur) {
				d.Sessions[k] = e
				if !ok {
					n++
				}
			}
		}
		for k, dev := range p.Devices {
			if cur := d.Devices[k]; cur == nil {
				d.Devices[k] = dev
				n++
			} else if dev.LastUsed.After(cur.LastUsed) {
				cur.LastUsed = dev.LastUsed
			}
		}
		byID := map[string]int{}
		for i, r := range d.Rules {
			byID[r.ID] = i
		}
		for _, r := range p.Rules {
			i, ok := byID[r.ID]
			if !ok {
				d.Rules = append(d.Rules, r)
				n++
				continue
			}
			if ruleChanged(r).After(ruleChanged(d.Rules[i])) {
				d.Rules[i] = r
				n++
			}
		}
		if p.AutoApproveChanged.After(d.AutoApproveChanged) {
			d.AutoApprove, d.AutoApproveChanged = p.AutoApprove, p.AutoApproveChanged
			n++
		}
		return nil
	})
	return n, err
}

func ruleChanged(r *HubRule) time.Time {
	if r.UpdatedAt.After(r.CreatedAt) {
		return r.UpdatedAt
	}
	return r.CreatedAt
}

// lanHostAllowed reports whether a request that reached the hub by a LAN IP
// address (not the public host name; names other than that are always
// refused, against DNS rebinding) may be served: only agent connections,
// health checks and replication.
func lanHostAllowed(path string) bool {
	return path == "/healthz" || path == "/agent/connect" || strings.HasPrefix(path, "/replica/")
}
