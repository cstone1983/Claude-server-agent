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
	"github.com/cstone1983/Claude-server-agent/internal/tools"
	"github.com/cstone1983/Claude-server-agent/internal/version"
)

type Hub struct {
	cfg     *Config
	store   *Store
	audit   *audit.Log
	reg     *registry
	appr    *approvals
	codes   *codeStore
	limiter *limiter
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
		reg:     &registry{conns: map[string]*agentConn{}},
		appr:    &approvals{m: map[string]*Approval{}},
		codes:   &codeStore{m: map[string]*authCode{}},
		limiter: newLimiter(10, 15*time.Minute),
	}, nil
}

const instructions = `This connector controls the user's Linux servers through Claude Server Agent.
Start with list_servers to learn server names; every other tool takes a "server" argument (a name, "tag:<tag>" or "all").
Prefer run_command (argv, no shell) over run_shell. Use start_job for anything that may take more than a minute.
Read-only actions run immediately. Other actions run immediately only if the server's allowlist permits them; otherwise the result says NEEDS APPROVAL with a link: show the link to the user, wait for them to approve, then call check_approval.
Always pass a short "reason" for actions that change things. Never try to work around a denial.`

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
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		renderPage(w, http.StatusOK, "Claude Server Agent hub", "Add "+h.cfg.PublicURL+"/mcp as a custom connector in Claude (Settings → Connectors).", nil)
	})
	return h.hostGuard(securityHeaders(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
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
	data := map[string]any{"A": snap, "Rule": snap.Suggested.String()}
	if r.Method == http.MethodPost {
		r.ParseForm()
		if err := h.checkCode(r, r.PostForm.Get("code")); err != nil {
			data["Error"] = err.Error()
			renderTemplate(w, http.StatusUnauthorized, approveTmpl, data)
			return
		}
		approve := r.PostForm.Get("decision") == "approve"
		ap, err := h.Decide(id, approve, r.PostForm.Get("always") == "1", r.PostForm.Get("scope"), "web:"+clientIP(r))
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
