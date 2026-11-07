package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
)

// adminHandler serves the local admin API on a root-only unix socket. It is
// what the csa-hub CLI talks to.
func (h *Hub) adminHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /join-tokens", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			TTLMinutes int      `json:"ttl_minutes"`
			Uses       int      `json:"uses"`
			Tags       []string `json:"tags"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.TTLMinutes <= 0 {
			req.TTLMinutes = 60
		}
		if req.Uses <= 0 {
			req.Uses = 1
		}
		tok := randomToken("csa_join_", 24)
		exp := time.Now().Add(time.Duration(req.TTLMinutes) * time.Minute).UTC()
		h.store.Update(func(d *stateData) error {
			d.JoinTokens[hashSecret(tok)] = &JoinToken{ExpiresAt: exp, UsesLeft: req.Uses, Tags: req.Tags, CreatedAt: time.Now().UTC()}
			return nil
		})
		h.audit.Write(audit.Entry{Event: "join_token_created", Detail: map[string]any{"uses": req.Uses, "expires": exp}})
		writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires_at": exp, "uses": req.Uses, "hub_url": h.cfg.PublicURL})
	})

	mux.HandleFunc("GET /servers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, h.listServers(Caller{}).Data)
	})
	mux.HandleFunc("DELETE /servers/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		var removed string
		h.store.Update(func(d *stateData) error {
			for id, a := range d.Agents {
				if a.Name == name {
					removed = id
					delete(d.Agents, id)
				}
			}
			return nil
		})
		if removed == "" {
			http.Error(w, "no such server", http.StatusNotFound)
			return
		}
		if c := h.reg.get(removed); c != nil {
			c.ws.Close()
		}
		h.audit.Write(audit.Entry{Event: "server_removed", Server: name})
		writeJSON(w, http.StatusOK, map[string]string{"removed": name})
	})
	mux.HandleFunc("POST /servers/{name}/rename", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			NewName string `json:"new_name"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		oldName := r.PathValue("name")
		if !nameRE.MatchString(req.NewName) {
			http.Error(w, "invalid name (letters, digits, . _ -; max 63)", http.StatusBadRequest)
			return
		}
		rules := 0
		err := h.store.Update(func(d *stateData) error {
			var target *AgentRec
			for _, a := range d.Agents {
				if a.Name == req.NewName && req.NewName != oldName {
					return errors.New("another server is already called " + req.NewName)
				}
				if a.Name == oldName {
					target = a
				}
			}
			if target == nil {
				return errors.New("no such server " + oldName)
			}
			target.Name = req.NewName
			// Keep "always allow" rules that were scoped to this server.
			for _, rl := range d.Rules {
				if rl.Scope == "server:"+oldName {
					rl.Scope = "server:" + req.NewName
					rules++
				}
			}
			return nil
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.audit.Write(audit.Entry{Event: "server_renamed", Server: req.NewName, Detail: map[string]any{"old_name": oldName, "rules_moved": rules}})
		writeJSON(w, http.StatusOK, map[string]any{"old_name": oldName, "name": req.NewName, "rules_moved": rules})
	})
	mux.HandleFunc("POST /servers/{name}/tags", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tags []string `json:"tags"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		for _, t := range req.Tags {
			if !nameRE.MatchString(t) {
				http.Error(w, "invalid tag "+t, http.StatusBadRequest)
				return
			}
		}
		found := false
		h.store.Update(func(d *stateData) error {
			for _, a := range d.Agents {
				if a.Name == r.PathValue("name") {
					a.Tags = dedupe(req.Tags)
					found = true
				}
			}
			return nil
		})
		if !found {
			http.Error(w, "no such server", http.StatusNotFound)
			return
		}
		go h.pushRulesAll()
		writeJSON(w, http.StatusOK, map[string]any{"tags": req.Tags})
	})

	mux.HandleFunc("POST /api-tokens", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name    string   `json:"name"`
			Scope   string   `json:"scope"`
			Servers []string `json:"servers"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if !nameRE.MatchString(req.Name) {
			http.Error(w, "invalid name", http.StatusBadRequest)
			return
		}
		if req.Scope == "" {
			req.Scope = ScopeRead
		}
		if req.Scope != ScopeRead && req.Scope != ScopeFull {
			http.Error(w, "scope must be read or full", http.StatusBadRequest)
			return
		}
		tok := randomToken("csa_api_", 32)
		err := h.store.Update(func(d *stateData) error {
			for _, t := range d.Tokens {
				if t.Kind == TokenAPI && t.Name == req.Name {
					return errors.New("a token with that name exists; revoke it first")
				}
			}
			d.Tokens[hashSecret(tok)] = &Token{Kind: TokenAPI, Name: req.Name, Scope: req.Scope, Servers: req.Servers, CreatedAt: time.Now().UTC()}
			return nil
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		h.audit.Write(audit.Entry{Event: "api_token_created", Caller: "api:" + req.Name, Detail: map[string]any{"scope": req.Scope, "servers": req.Servers}})
		writeJSON(w, http.StatusOK, map[string]any{"token": tok, "name": req.Name, "scope": req.Scope, "servers": req.Servers})
	})
	mux.HandleFunc("GET /api-tokens", func(w http.ResponseWriter, r *http.Request) {
		var out []Token
		h.store.View(func(d *stateData) {
			for _, t := range d.Tokens {
				if t.Kind == TokenAPI {
					out = append(out, *t)
				}
			}
		})
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("DELETE /api-tokens/{name}", func(w http.ResponseWriter, r *http.Request) {
		n := 0
		h.store.Update(func(d *stateData) error {
			for k, t := range d.Tokens {
				if t.Kind == TokenAPI && t.Name == r.PathValue("name") {
					delete(d.Tokens, k)
					n++
				}
			}
			return nil
		})
		if n == 0 {
			http.Error(w, "no such token", http.StatusNotFound)
			return
		}
		h.audit.Write(audit.Entry{Event: "api_token_revoked", Caller: "api:" + r.PathValue("name")})
		writeJSON(w, http.StatusOK, map[string]int{"revoked": n})
	})

	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, r *http.Request) {
		type sess struct {
			Client    string    `json:"client"`
			Kind      string    `json:"kind"`
			ExpiresAt time.Time `json:"expires_at"`
			LastUsed  time.Time `json:"last_used"`
		}
		var out []sess
		h.store.View(func(d *stateData) {
			for _, t := range d.Tokens {
				if t.Kind != TokenAPI {
					out = append(out, sess{t.Name, t.Kind, t.ExpiresAt, t.LastUsed})
				}
			}
		})
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("DELETE /sessions", func(w http.ResponseWriter, r *http.Request) {
		n := 0
		h.store.Update(func(d *stateData) error {
			for k, t := range d.Tokens {
				if t.Kind != TokenAPI {
					delete(d.Tokens, k)
					n++
				}
			}
			return nil
		})
		h.audit.Write(audit.Entry{Event: "sessions_revoked", Detail: map[string]int{"count": n}})
		writeJSON(w, http.StatusOK, map[string]int{"revoked": n})
	})

	mux.HandleFunc("GET /approvals", func(w http.ResponseWriter, r *http.Request) {
		all := h.appr.list()
		if r.URL.Query().Get("all") == "" {
			var p []Approval
			for _, a := range all {
				if a.Status == StatusPending {
					p = append(p, a)
				}
			}
			all = p
		}
		writeJSON(w, http.StatusOK, all)
	})
	mux.HandleFunc("POST /approvals/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Decision string `json:"decision"`
			Always   bool   `json:"always"`
			Scope    string `json:"scope"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Decision != "approve" && req.Decision != "deny" {
			http.Error(w, "decision must be approve or deny", http.StatusBadRequest)
			return
		}
		id := r.PathValue("id")
		if !strings.HasPrefix(id, "apr_") {
			// Allow a unique prefix of the random part.
			for _, a := range h.appr.list() {
				if strings.HasPrefix(strings.TrimPrefix(a.ID, "apr_"), id) && a.Status == StatusPending {
					id = a.ID
					break
				}
			}
		}
		ap, err := h.Decide(id, req.Decision == "approve", req.Always, req.Scope, "cli")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, ap)
	})

	mux.HandleFunc("GET /rules", func(w http.ResponseWriter, r *http.Request) {
		var out []HubRule
		h.store.View(func(d *stateData) {
			for _, x := range d.Rules {
				out = append(out, *x)
			}
		})
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("DELETE /rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		found := false
		h.store.Update(func(d *stateData) error {
			for i, x := range d.Rules {
				if x.ID == r.PathValue("id") {
					d.Rules = append(d.Rules[:i], d.Rules[i+1:]...)
					found = true
					break
				}
			}
			return nil
		})
		if !found {
			http.Error(w, "no such rule", http.StatusNotFound)
			return
		}
		h.audit.Write(audit.Entry{Event: "rule_deleted", Detail: map[string]string{"id": r.PathValue("id")}})
		go h.pushRulesAll()
		writeJSON(w, http.StatusOK, map[string]string{"deleted": r.PathValue("id")})
	})

	mux.HandleFunc("POST /tools/{name}", func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, http.StatusOK, h.Invoke(r.Context(), Caller{Name: "admin-cli"}, r.PathValue("name"), body))
	})
	return mux
}
