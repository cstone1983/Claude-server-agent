package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/proto"
	"github.com/cstone1983/Claude-server-agent/internal/tools"
	"github.com/cstone1983/Claude-server-agent/internal/version"
)

type Hub struct {
	cfg      *Config
	store    *Store
	audit    *audit.Log
	reg      *registry
	appr     *approvals
	codes    *codeStore
	limiter  *limiter
	releases *releaseWatcher
	sess     *sessions
	metrics  *metricsStore
	// testSend replaces sending to an agent in tests.
	testSend func(*agentConn, proto.Envelope) error
}

func New(cfg *Config) (*Hub, error) {
	st, err := OpenStore(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	al, err := audit.Open(cfg.AuditLog)
	if err != nil {
		return nil, err
	}
	return &Hub{
		cfg: cfg, store: st, audit: al,
		reg:      &registry{conns: map[string]*agentConn{}},
		appr:     &approvals{m: map[string]*Approval{}},
		codes:    &codeStore{m: map[string]*authCode{}},
		limiter:  newLimiter(10, 15*time.Minute),
		releases: newReleaseWatcher(),
		metrics:  &metricsStore{m: map[string]*serverMetrics{}},
		sess:     &sessions{store: st, ttl: time.Duration(cfg.DashboardSessionHours) * time.Hour},
	}, nil
}

const instructions = `This connector controls the user's Linux servers through Claude Server Agent.
Start with list_servers to learn server names; every other tool takes a "server" argument (a name, "tag:<tag>" or "all").
Prefer run_command (argv, no shell) over run_shell. Use start_job for anything that may take more than a minute.
Read-only actions run immediately. Other actions run immediately only if the server's allowlist permits them; otherwise the result says NEEDS APPROVAL with a link: show the link to the user, wait for them to approve, then call check_approval.
Always pass a short "reason" for actions that change things. Never try to work around a denial.
If list_servers shows UPDATE AVAILABLE, tell the user; run update_server only after the user explicitly says to update.
Verify, don't assume: before any command that changes something, use read-only commands to confirm every fact it depends on (the target exists and is the one the user means, current state, free space, versions, what else depends on it), and double-check the exact command and arguments. If something can't be verified or looks different than expected, stop and ask the user.
Servers tagged "proxmox" are hypervisor hosts: everything on the network runs on them, so be extra careful. Confirm VM IDs against VM names (qm list / qm config) right before using them, never from memory; say exactly what will change, which VMs it touches, any downtime and how to undo it before running it; make one change at a time and verify the result afterwards. Never shut a proxmox host down. Reboot one only after the user has said OK to that reboot in so many words, as its own request: never bundled with other commands or inside a script, and never "always allowed". Reboot it with pve-safe-reboot (it shuts the VMs down one by one, the hub's VM last, then reboots; first show the user "pve-safe-reboot --dry-run"), and before that make sure no VM is in the middle of an upgrade or a backup.
When a result says "auto-approved: hands-off mode is on", the user is away and approved nothing themselves: take the same care as always, keep to what they asked for, and save anything risky or irreversible (deleting data, wiping disks, changing access) for when they are back.`

func (h *Hub) mcpServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "claude-server-agent", Title: "Claude Server Agent", Version: version.Version}, &mcp.ServerOptions{Instructions: instructions})
	for _, def := range tools.All {
		def := def
		destructive := def.Destructive
		tool := &mcp.Tool{
			Name: def.Name, Title: def.Title, Description: def.Description,
			InputSchema: def.Schema(true),
			Annotations: &mcp.ToolAnnotations{Title: def.Title, ReadOnlyHint: def.ReadOnly, DestructiveHint: &destructive},
		}
		s.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var caller Caller
			if req.Extra != nil {
				caller = callerFrom(req.Extra.TokenInfo)
			} else {
				caller = callerFrom(nil)
			}
			out := h.Invoke(ctx, caller, def.Name, req.Params.Arguments)
			text := out.Text
			if text == "" {
				text = "(no output)"
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: out.IsError}, nil
		})
	}
	return s
}

// hostGuard rejects requests whose Host is neither the public host nor
// loopback (DNS-rebinding protection; the MCP SDK's own check is disabled
// because cloudflared always connects from loopback).
func (h *Hub) hostGuard(next http.Handler) http.Handler {
	public := strings.ToLower(h.cfg.publicHost())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		hn := host
		if x, _, err := net.SplitHostPort(host); err == nil {
			hn = x
		}
		if host != public && hn != "localhost" && hn != "127.0.0.1" && hn != "::1" && hn != "[::1]" {
			http.Error(w, "unknown host", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mcpSrv := h.mcpServer()
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpSrv }, &mcp.StreamableHTTPOptions{
		DisableLocalhostProtection: true,
		SessionTimeout:             2 * time.Hour,
	})
	bearer := auth.RequireBearerToken(h.verifyToken, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: h.cfg.PublicURL + "/.well-known/oauth-protected-resource/mcp",
	})
	mux.Handle("/mcp", bearer(mcpHandler))
	mux.Handle("/mcp/", bearer(mcpHandler))

	mux.HandleFunc("/.well-known/oauth-protected-resource", h.handleProtectedResource)
	mux.HandleFunc("/.well-known/oauth-protected-resource/", h.handleProtectedResource)
	mux.HandleFunc("/.well-known/oauth-authorization-server", h.handleASMetadata)
	mux.HandleFunc("/.well-known/oauth-authorization-server/", h.handleASMetadata)
	mux.HandleFunc("/.well-known/openid-configuration", h.handleASMetadata)
	mux.HandleFunc("/oauth/register", h.handleRegister)
	mux.HandleFunc("/oauth/authorize", h.handleAuthorize)
	mux.HandleFunc("/oauth/token", h.handleToken)
	mux.HandleFunc("/oauth/revoke", h.handleRevoke)

	mux.HandleFunc("/agent/enroll", h.handleEnroll)
	mux.HandleFunc("/agent/connect", h.handleAgentConnect)

	mux.HandleFunc("/approve/", h.handleApprovePage)
	mux.HandleFunc("/login", h.handleLogin)
	mux.HandleFunc("/logout", h.handleLogout)
	mux.HandleFunc("/approvals", h.handleApprovals)
	mux.HandleFunc("/rules", h.handleRules)
	mux.HandleFunc("/rules/edit", h.handleRuleEdit)
	mux.HandleFunc("/servers", h.handleServers)
	mux.HandleFunc("/activity", h.handleActivity)
	mux.HandleFunc("/static/", h.handleStatic)

	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/servers", func(w http.ResponseWriter, r *http.Request) {
		out := h.listServers(callerFrom(auth.TokenInfoFromContext(r.Context())))
		writeJSON(w, http.StatusOK, out)
	})
	api.HandleFunc("GET /api/v1/tools", func(w http.ResponseWriter, r *http.Request) {
		var list []map[string]any
		for _, d := range tools.All {
			list = append(list, map[string]any{"name": d.Name, "description": d.Description, "input_schema": d.Schema(true)})
		}
		writeJSON(w, http.StatusOK, list)
	})
	api.HandleFunc("POST /api/v1/tools/{name}", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			body = []byte("{}")
		}
		out := h.Invoke(r.Context(), callerFrom(auth.TokenInfoFromContext(r.Context())), r.PathValue("name"), body)
		writeJSON(w, http.StatusOK, out)
	})
	api.HandleFunc("GET /api/v1/approvals/{id}", func(w http.ResponseWriter, r *http.Request) {
		ap, ok := h.appr.snapshot(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, ap)
	})
	mux.Handle("/api/", bearer(api))

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("/", h.handleOverview)
	return h.hostGuard(securityHeaders(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func (h *Hub) handleApprovePage(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/approve/")
	snap, ok := h.appr.snapshot(id)
	if !ok {
		renderPage(w, http.StatusNotFound, "Not found", "This approval does not exist or has expired.", nil)
		return
	}
	dev, remembered := h.trustedDevice(r)
	// A remembered device approves without a code, except requests that need
	// their own OK every time.
	needCode := !remembered || snap.AskAlways
	data := map[string]any{"A": snap, "Rule": snap.Suggested.String(), "NeedCode": needCode, "Remembered": remembered, "Device": dev.DisplayName()}
	if r.Method == http.MethodPost {
		r.ParseForm()
		if !h.sameOrigin(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		via := "web:" + clientIP(r)
		if needCode {
			if err := h.checkCode(r, r.PostForm.Get("code")); err != nil {
				data["Error"] = err.Error()
				renderTemplate(w, http.StatusUnauthorized, approveTmpl, data)
				return
			}
			if !remembered && r.PostForm.Get("remember") == "1" {
				h.rememberDevice(w, r, "approval page")
			}
		} else {
			h.useDevice(w, r)
			via += " (remembered device " + dev.ID + ")"
		}
		approve := r.PostForm.Get("decision") == "approve"
		ap, err := h.Decide(id, approve, r.PostForm.Get("always") == "1", r.PostForm.Get("scope"), via)
		if err != nil {
			data["Error"] = err.Error()
		}
		if ap != nil {
			data["A"] = *ap
			if approve {
				data["Done"] = "Approved. Claude can continue now (if it stopped waiting, tell it to check the approval)."
			} else {
				data["Done"] = "Denied."
			}
		}
		renderTemplate(w, http.StatusOK, approveTmpl, data)
		return
	}
	if remembered {
		h.useDevice(w, r)
	}
	renderTemplate(w, http.StatusOK, approveTmpl, data)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// Serve runs the public listener and the admin socket until ctx ends.
func (h *Hub) Serve(ctx context.Context) error {
	srv := &http.Server{Addr: h.cfg.Listen, Handler: h.Handler(), ReadHeaderTimeout: 20 * time.Second}
	ln, err := net.Listen("tcp", h.cfg.Listen)
	if err != nil {
		return err
	}
	adminLn, err := listenUnix(h.cfg.AdminSocket)
	if err != nil {
		return err
	}
	admin := &http.Server{Handler: h.adminHandler(), ReadHeaderTimeout: 10 * time.Second}

	go h.releases.run(ctx)
	go h.collectMetrics(ctx)
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.expireApprovals()
				h.store.Update(func(*stateData) error { return nil }) // prune expired tokens, persist last_used
			}
		}
	}()
	errc := make(chan error, 2)
	go func() { errc <- srv.Serve(ln) }()
	go func() { errc <- admin.Serve(adminLn) }()
	slog.Info("hub listening", "addr", h.cfg.Listen, "public_url", h.cfg.PublicURL, "admin_socket", h.cfg.AdminSocket)
	h.audit.Write(audit.Entry{Event: "hub_started", Detail: map[string]string{"version": version.String()}})
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin.Shutdown(sctx)
	return srv.Shutdown(sctx)
}

func listenUnix(path string) (net.Listener, error) {
	if len(path) > 100 {
		return nil, errors.New("admin_socket path is too long for a unix socket (max ~100 characters): " + path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}
