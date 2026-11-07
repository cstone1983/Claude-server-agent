package hub

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/crypto/bcrypt"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/totp"
)

// A minimal OAuth 2.1 authorization server for a single admin user:
// metadata discovery (RFC 8414/9728), dynamic client registration (RFC 7591),
// authorization code + PKCE S256, and rotating refresh tokens.

type authCode struct {
	clientID      string
	redirectURI   string
	challenge     string
	scope         string
	expires       time.Time
}

type codeStore struct {
	mu sync.Mutex
	m  map[string]*authCode
}

func (c *codeStore) put(code string, ac *authCode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.m {
		if time.Now().After(v.expires) {
			delete(c.m, k)
		}
	}
	c.m[code] = ac
}

func (c *codeStore) take(code string) *authCode {
	c.mu.Lock()
	defer c.mu.Unlock()
	ac := c.m[code]
	delete(c.m, code)
	if ac == nil || time.Now().After(ac.expires) {
		return nil
	}
	return ac
}

func cors(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Mcp-Protocol-Version")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
}

func (h *Hub) handleProtectedResource(w http.ResponseWriter, r *http.Request) {
	cors(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 h.cfg.PublicURL + "/mcp",
		"authorization_servers":    []string{h.cfg.PublicURL},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{"csa"},
		"resource_name":            "Claude Server Agent",
	})
}

func (h *Hub) handleASMetadata(w http.ResponseWriter, r *http.Request) {
	cors(w)
	p := h.cfg.PublicURL
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                p,
		"authorization_endpoint":                p + "/oauth/authorize",
		"token_endpoint":                        p + "/oauth/token",
		"registration_endpoint":                 p + "/oauth/register",
		"revocation_endpoint":                   p + "/oauth/revoke",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"csa"},
	})
}

func (h *Hub) redirectAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" {
		return false
	}
	host := u.Hostname()
	isLocal := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && isLocal) {
		return false
	}
	for _, a := range h.cfg.AllowedRedirectHosts {
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

func (h *Hub) handleRegister(w http.ResponseWriter, r *http.Request) {
	cors(w)
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.limiter.allow("register:" + clientIP(r)) {
		oauthError(w, http.StatusTooManyRequests, "slow_down", "too many registrations")
		return
	}
	h.limiter.fail("register:" + clientIP(r)) // counts registrations, not just failures
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "bad JSON")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris required")
		return
	}
	for _, u := range req.RedirectURIs {
		if !h.redirectAllowed(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect URI host not allowed by this hub: "+u+" (see allowed_redirect_hosts in hub.yaml)")
			return
		}
	}
	name := truncate(strings.TrimSpace(req.ClientName), 80)
	if name == "" {
		name = "MCP client"
	}
	c := &OAuthClient{ID: randomToken("cli_", 16), Name: name, RedirectURIs: req.RedirectURIs, CreatedAt: time.Now().UTC()}
	h.store.Update(func(d *stateData) error {
		// Keep the client list bounded: drop the oldest clients beyond 100.
		if len(d.Clients) >= 100 {
			var oldest *OAuthClient
			for _, x := range d.Clients {
				if oldest == nil || x.CreatedAt.Before(oldest.CreatedAt) {
					oldest = x
				}
			}
			delete(d.Clients, oldest.ID)
		}
		d.Clients[c.ID] = c
		return nil
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  c.ID,
		"client_id_issued_at":        c.CreatedAt.Unix(),
		"client_name":                c.Name,
		"redirect_uris":              c.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
}

func (h *Hub) lookupClient(id string) *OAuthClient {
	var c *OAuthClient
	h.store.View(func(d *stateData) {
		if x := d.Clients[id]; x != nil {
			cp := *x
			c = &cp
		}
	})
	return c
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// checkLogin verifies the admin password and TOTP code with rate limiting.
func (h *Hub) checkLogin(r *http.Request, password, code string) error {
	ip := clientIP(r)
	if !h.limiter.allow("login:"+ip) || !h.limiter.allow("login:global") {
		return errors.New("too many failed attempts; wait 15 minutes")
	}
	pwOK := password != "" && bcrypt.CompareHashAndPassword([]byte(h.cfg.AdminPasswordHash), []byte(password)) == nil
	codeOK := totp.Verify(h.cfg.TOTPSecret, code, time.Now())
	if !pwOK || !codeOK {
		h.limiter.fail("login:" + ip)
		h.limiter.fail("login:global")
		h.audit.Write(audit.Entry{Event: "login_failed", Detail: map[string]string{"ip": ip}})
		return errors.New("wrong password or code")
	}
	return nil
}

func (h *Hub) checkCode(r *http.Request, code string) error {
	ip := clientIP(r)
	if !h.limiter.allow("totp:"+ip) || !h.limiter.allow("totp:global") {
		return errors.New("too many failed attempts; wait 15 minutes")
	}
	if !totp.Verify(h.cfg.TOTPSecret, code, time.Now()) {
		h.limiter.fail("totp:" + ip)
		h.limiter.fail("totp:global")
		h.audit.Write(audit.Entry{Event: "totp_failed", Detail: map[string]string{"ip": ip}})
		return errors.New("wrong code")
	}
	return nil
}

func (h *Hub) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	q := r.Form
	client := h.lookupClient(q.Get("client_id"))
	redirect := q.Get("redirect_uri")
	if client == nil {
		renderPage(w, http.StatusBadRequest, "Unknown client", "This connector is not registered with the hub. Remove and re-add the connector in Claude.", nil)
		return
	}
	if redirect == "" && len(client.RedirectURIs) == 1 {
		redirect = client.RedirectURIs[0]
	}
	if !contains(client.RedirectURIs, redirect) {
		renderPage(w, http.StatusBadRequest, "Invalid redirect", "The redirect URI does not match the registered client.", nil)
		return
	}
	fail := func(code, desc string) {
		u, _ := url.Parse(redirect)
		v := u.Query()
		v.Set("error", code)
		v.Set("error_description", desc)
		if s := q.Get("state"); s != "" {
			v.Set("state", s)
		}
		u.RawQuery = v.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
	if q.Get("response_type") != "code" {
		fail("unsupported_response_type", "only code is supported")
		return
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		fail("invalid_request", "PKCE with S256 is required")
		return
	}
	data := map[string]any{
		"Client": client.Name, "RedirectHost": hostOf(redirect),
		"Params": map[string]string{
			"client_id": client.ID, "redirect_uri": redirect, "state": q.Get("state"),
			"code_challenge": q.Get("code_challenge"), "code_challenge_method": "S256",
			"response_type": "code", "scope": q.Get("scope"), "resource": q.Get("resource"),
		},
	}
	if r.Method == http.MethodPost {
		if q.Get("action") == "deny" {
			fail("access_denied", "the user denied access")
			return
		}
		if err := h.checkLogin(r, q.Get("password"), q.Get("code")); err != nil {
			data["Error"] = err.Error()
			renderTemplate(w, http.StatusUnauthorized, loginTmpl, data)
			return
		}
		code := randomToken("", 32)
		h.codes.put(code, &authCode{clientID: client.ID, redirectURI: redirect, challenge: q.Get("code_challenge"), scope: q.Get("scope"), expires: time.Now().Add(5 * time.Minute)})
		h.audit.Write(audit.Entry{Event: "oauth_authorized", Caller: client.Name, Detail: map[string]string{"ip": clientIP(r), "redirect": hostOf(redirect)}})
		u, _ := url.Parse(redirect)
		v := u.Query()
		v.Set("code", code)
		if s := q.Get("state"); s != "" {
			v.Set("state", s)
		}
		u.RawQuery = v.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
		return
	}
	renderTemplate(w, http.StatusOK, loginTmpl, data)
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Host
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func (h *Hub) issueTokens(clientID, clientName string) map[string]any {
	access := randomToken("csa_at_", 32)
	refresh := randomToken("csa_rt_", 32)
	now := time.Now().UTC()
	at := time.Duration(h.cfg.AccessTokenTTLMin) * time.Minute
	h.store.Update(func(d *stateData) error {
		d.Tokens[hashSecret(access)] = &Token{Kind: TokenAccess, ClientID: clientID, Name: clientName, Scope: ScopeFull, ExpiresAt: now.Add(at), CreatedAt: now}
		d.Tokens[hashSecret(refresh)] = &Token{Kind: TokenRefresh, ClientID: clientID, Name: clientName, Scope: ScopeFull, ExpiresAt: now.Add(time.Duration(h.cfg.RefreshTokenTTLDays) * 24 * time.Hour), CreatedAt: now}
		return nil
	})
	return map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(at.Seconds()),
		"refresh_token": refresh, "scope": "csa",
	}
}

func (h *Hub) handleToken(w http.ResponseWriter, r *http.Request) {
	cors(w)
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "bad form")
		return
	}
	f := r.PostForm
	switch f.Get("grant_type") {
	case "authorization_code":
		ac := h.codes.take(f.Get("code"))
		if ac == nil || ac.clientID != f.Get("client_id") || (f.Get("redirect_uri") != "" && ac.redirectURI != f.Get("redirect_uri")) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired code")
			return
		}
		sum := sha256.Sum256([]byte(f.Get("code_verifier")))
		if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(ac.challenge)) != 1 {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
			return
		}
		c := h.lookupClient(ac.clientID)
		if c == nil {
			oauthError(w, http.StatusBadRequest, "invalid_client", "client no longer registered")
			return
		}
		writeJSON(w, http.StatusOK, h.issueTokens(c.ID, c.Name))
	case "refresh_token":
		var tok *Token
		key := hashSecret(f.Get("refresh_token"))
		h.store.Update(func(d *stateData) error {
			t := d.Tokens[key]
			if t != nil && t.Kind == TokenRefresh && time.Now().Before(t.ExpiresAt) && (f.Get("client_id") == "" || f.Get("client_id") == t.ClientID) {
				cp := *t
				tok = &cp
				delete(d.Tokens, key) // rotate
			}
			return nil
		})
		if tok == nil {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired refresh token")
			return
		}
		writeJSON(w, http.StatusOK, h.issueTokens(tok.ClientID, tok.Name))
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

func (h *Hub) handleRevoke(w http.ResponseWriter, r *http.Request) {
	cors(w)
	if r.Method != http.MethodPost {
		return
	}
	r.ParseForm()
	key := hashSecret(r.PostForm.Get("token"))
	h.store.Update(func(d *stateData) error {
		if t := d.Tokens[key]; t != nil && t.Kind != TokenAPI {
			delete(d.Tokens, key)
		}
		return nil
	})
	w.WriteHeader(http.StatusOK)
}

type callerKey struct{}

// verifyToken is the bearer-token verifier for /mcp and /api.
func (h *Hub) verifyToken(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	var tok *Token
	key := hashSecret(token)
	h.store.View(func(d *stateData) {
		if t := d.Tokens[key]; t != nil && (t.Kind == TokenAccess || t.Kind == TokenAPI) {
			if t.ExpiresAt.IsZero() || time.Now().Before(t.ExpiresAt) {
				if time.Since(t.LastUsed) > time.Minute {
					t.LastUsed = time.Now().UTC() // persisted on next save
				}
				cp := *t
				tok = &cp
			}
		}
	})
	if tok == nil {
		return nil, fmt.Errorf("%w: unknown or expired token", auth.ErrInvalidToken)
	}
	c := Caller{Name: "claude:" + tok.Name, ReadOnly: tok.Scope == ScopeRead, Servers: tok.Servers}
	exp := tok.ExpiresAt
	if tok.Kind == TokenAPI {
		c.Name = "api:" + tok.Name
		exp = time.Now().Add(24 * time.Hour)
	}
	return &auth.TokenInfo{UserID: "admin", Expiration: exp, Scopes: []string{"csa"}, Extra: map[string]any{"caller": c}}, nil
}

func callerFrom(ti *auth.TokenInfo) Caller {
	if ti != nil {
		if c, ok := ti.Extra["caller"].(Caller); ok {
			return c
		}
	}
	return Caller{Name: "unknown", ReadOnly: true}
}
