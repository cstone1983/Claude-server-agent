package hub

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/version"
)

// The control panel's templates and static assets are embedded in the binary.
//
//go:embed web
var webFS embed.FS

var (
	staticFS, _  = fs.Sub(webFS, "web/static")
	assetVersion = func() string {
		h := sha256.New()
		for _, f := range []string{"web/static/app.css", "web/static/app.js"} {
			b, _ := webFS.ReadFile(f)
			h.Write(b)
		}
		return hex.EncodeToString(h.Sum(nil))[:10]
	}()
)

var tmplFuncs = template.FuncMap{
	"iso":  func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
	"join": strings.Join,
	"hasTag": func(tags []string, t string) bool {
		for _, x := range tags {
			if strings.EqualFold(x, t) {
				return true
			}
		}
		return false
	},
	"sortmark": func(d map[string]any, col string) string {
		cur, _ := d["Sort"].(string)
		dir, _ := d["Dir"].(string)
		if cur == "" {
			cur = "added"
		}
		if cur != col {
			return ""
		}
		if dir == "desc" {
			return " ▼"
		}
		return " ▲"
	},
}

// pageT names a page template and the layout it renders in.
type pageT struct {
	file, layout, title, nav string
	t                        *template.Template
}

func newPage(file, layout, title, nav string) pageT {
	t := template.Must(template.New(file).Funcs(tmplFuncs).ParseFS(webFS, "web/templates/layout.html", "web/templates/"+file))
	return pageT{file: file, layout: layout, title: title, nav: nav, t: t}
}

var (
	loginTmpl         = newPage("oauth.html", "auth", "Connect", "")
	approveTmpl       = newPage("approve.html", "auth", "Approve", "")
	msgTmpl           = newPage("message.html", "auth", "Server Agent", "")
	dashLoginTmpl     = newPage("login.html", "auth", "Log in", "")
	dashApprovalsTmpl = newPage("approvals.html", "layout", "Approvals", "approvals")
	dashRulesTmpl     = newPage("rules.html", "layout", "Rules", "rules")
	ruleEditTmpl      = newPage("rule_edit.html", "layout", "Edit rule", "rules")
	overviewTmpl      = newPage("overview.html", "layout", "Overview", "overview")
	serversTmpl       = newPage("servers.html", "layout", "Servers", "servers")
	activityTmpl      = newPage("activity.html", "layout", "Activity", "activity")
)

// view is what every template receives; page-specific data is in D.
type view struct {
	Title, Nav   string
	Pending      int
	Done, Error  string
	Refresh      int
	Version      string
	AssetVersion string
	D            map[string]any
}

const csp = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; " +
	"form-action 'self' https: http://localhost:* http://127.0.0.1:*; frame-ancestors 'none'; base-uri 'none'"

func setPageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Cache-Control", "no-store")
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func execPage(w http.ResponseWriter, status int, p pageT, v view) {
	setPageHeaders(w)
	w.WriteHeader(status)
	if v.Title == "" {
		v.Title = p.title
	}
	v.Nav, v.Version, v.AssetVersion = p.nav, version.Version, assetVersion
	if err := p.t.ExecuteTemplate(w, p.layout, v); err != nil {
		fmt.Fprintf(w, "<!-- template error: %s -->", template.HTMLEscapeString(err.Error()))
	}
}

// renderTemplate renders a standalone (auth-layout) page. data["Error"] and
// data["Done"] become notices.
func renderTemplate(w http.ResponseWriter, status int, p pageT, data map[string]any) {
	execPage(w, status, p, view{D: data, Done: str(data["Done"]), Error: str(data["Error"])})
}

func renderPage(w http.ResponseWriter, status int, title, msg string, _ any) {
	renderTemplate(w, status, msgTmpl, map[string]any{"Title": title, "Msg": msg})
}

// renderPanel renders a page inside the logged-in control panel layout.
func (h *Hub) renderPanel(w http.ResponseWriter, status int, p pageT, data map[string]any, refresh int) {
	pending := 0
	for _, a := range h.appr.list() {
		if a.Status == StatusPending {
			pending++
		}
	}
	execPage(w, status, p, view{D: data, Done: str(data["Done"]), Error: str(data["Error"]), Pending: pending, Refresh: refresh})
}

func (h *Hub) handleStatic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))).ServeHTTP(w, r)
}
