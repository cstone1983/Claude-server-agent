package hub

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/policy"
)

// Browser dashboard: log in once (password + authenticator code), then review
// and bulk-approve pending actions and manage "always allow" rules.

const (
	sessionCookie = "csa_session"
	sessionTTL    = 12 * time.Hour
)

type sessions struct {
	mu sync.Mutex
	m  map[string]time.Time // hashed token -> expiry
}

func (s *sessions) create() string {
	tok := randomToken("", 32)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, exp := range s.m {
		if now.After(exp) {
			delete(s.m, k)
		}
	}
	s.m[hashSecret(tok)] = now.Add(sessionTTL)
	return tok
}

func (s *sessions) valid(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[hashSecret(tok)]
	return ok && time.Now().Before(exp)
}

func (s *sessions) remove(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, hashSecret(tok))
}

func (h *Hub) loggedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && h.sess.valid(c.Value)
}

// sameOrigin rejects cross-site form posts (CSRF). Browsers send Origin on POST.
func (h *Hub) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true // non-browser clients; the session cookie is SameSite=Strict anyway
	}
	u, err := url.Parse(h.cfg.PublicURL)
	if err != nil {
		return false
	}
	return o == u.Scheme+"://"+u.Host || strings.HasPrefix(o, "http://localhost") || strings.HasPrefix(o, "http://127.0.0.1")
}

// requireLogin redirects to /login when there is no session.
func (h *Hub) requireLogin(w http.ResponseWriter, r *http.Request) bool {
	if h.loggedIn(r) {
		if r.Method == http.MethodPost && !h.sameOrigin(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return false
		}
		return true
	}
	http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	return false
}

func safeNext(next string) string {
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") {
		return next
	}
	return "/approvals"
}

func (h *Hub) handleLogin(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.FormValue("next"))
	data := map[string]any{"Next": next}
	if r.Method == http.MethodPost {
		if !h.sameOrigin(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		if err := h.checkLogin(r, r.PostFormValue("password"), r.PostFormValue("code")); err != nil {
			data["Error"] = err.Error()
			renderTemplate(w, http.StatusUnauthorized, dashLoginTmpl, data)
			return
		}
		tok := h.sess.create()
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookie, Value: tok, Path: "/", MaxAge: int(sessionTTL.Seconds()),
			HttpOnly: true, Secure: strings.HasPrefix(h.cfg.PublicURL, "https://"), SameSite: http.SameSiteStrictMode,
		})
		h.audit.Write(audit.Entry{Event: "dashboard_login", Detail: map[string]string{"ip": clientIP(r)}})
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	renderTemplate(w, http.StatusOK, dashLoginTmpl, data)
}

func (h *Hub) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		h.sess.remove(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// approvalGroup is one action requested on one or more servers.
type approvalGroup struct {
	Key       string
	Tool      string
	Summary   string
	Why       string
	Reason    string
	Rule      string
	Approvals []Approval
}

func (h *Hub) pendingGroups() []approvalGroup {
	byKey := map[string]*approvalGroup{}
	var order []string
	for _, a := range h.appr.list() {
		if a.Status != StatusPending {
			continue
		}
		key := a.Tool + "\x00" + a.Summary
		g := byKey[key]
		if g == nil {
			g = &approvalGroup{Key: key, Tool: a.Tool, Summary: a.Summary, Why: a.Why, Reason: a.Reason, Rule: a.Suggested.String()}
			byKey[key] = g
			order = append(order, key)
		}
		g.Approvals = append(g.Approvals, a)
	}
	out := make([]approvalGroup, 0, len(order))
	for _, k := range order {
		g := byKey[k]
		sort.Slice(g.Approvals, func(i, j int) bool { return g.Approvals[i].Server < g.Approvals[j].Server })
		out = append(out, *g)
	}
	return out
}

func (h *Hub) handleApprovals(w http.ResponseWriter, r *http.Request) {
	if !h.requireLogin(w, r) {
		return
	}
	data := map[string]any{}
	if r.Method == http.MethodPost {
		r.ParseForm()
		ids := r.PostForm["id"]
		action := r.PostFormValue("action")
		scope := r.PostFormValue("scope")
		if len(ids) == 0 {
			data["Error"] = "Select at least one action."
		} else {
			done, failed := 0, []string{}
			for _, id := range ids {
				var err error
				switch action {
				case "approve":
					_, err = h.Decide(id, true, false, "", "dashboard:"+clientIP(r))
				case "always":
					_, err = h.Decide(id, true, true, scope, "dashboard:"+clientIP(r))
				case "deny":
					_, err = h.Decide(id, false, false, "", "dashboard:"+clientIP(r))
				default:
					err = errors.New("unknown action")
				}
				if err != nil {
					failed = append(failed, err.Error())
				} else {
					done++
				}
			}
			verb := map[string]string{"approve": "Approved", "always": "Approved and allowed from now on:", "deny": "Denied"}[action]
			data["Done"] = verb + " " + itoa(done) + " action(s). Claude picks them up when it checks the approval (tell it to continue if it stopped waiting)."
			if len(failed) > 0 {
				data["Error"] = strings.Join(failed, "; ")
			}
		}
	}
	data["Groups"] = h.pendingGroups()
	var recent []Approval
	for _, a := range h.appr.list() {
		if a.Status != StatusPending && len(recent) < 15 {
			recent = append(recent, a)
		}
	}
	data["Recent"] = recent
	renderTemplate(w, http.StatusOK, dashApprovalsTmpl, data)
}

func itoa(n int) string { return strconv.Itoa(n) }

func (h *Hub) handleRules(w http.ResponseWriter, r *http.Request) {
	if !h.requireLogin(w, r) {
		return
	}
	data := map[string]any{}
	if r.Method == http.MethodPost {
		r.ParseForm()
		switch r.PostFormValue("op") {
		case "delete":
			id := r.PostFormValue("rule_id")
			found := false
			h.store.Update(func(d *stateData) error {
				for i, x := range d.Rules {
					if x.ID == id {
						d.Rules = append(d.Rules[:i], d.Rules[i+1:]...)
						found = true
						break
					}
				}
				return nil
			})
			if found {
				h.audit.Write(audit.Entry{Event: "rule_deleted", Detail: map[string]string{"id": id, "via": "dashboard"}})
				go h.pushRulesAll()
				data["Done"] = "Rule removed."
			}
		case "add":
			rule, err := ruleFromForm(r.PostFormValue("kind"), r.PostFormValue("value"), r.PostFormValue("any_args") == "1", strings.TrimSpace(r.PostFormValue("as")))
			scope := r.PostFormValue("scope")
			if err == nil && !validScope(scope) {
				err = errors.New("choose which servers the rule applies to")
			}
			if err != nil {
				data["Error"] = err.Error()
				data["Form"] = r.PostForm
				break
			}
			rule.Comment = strings.TrimSpace(r.PostFormValue("comment"))
			if _, err := h.addRule(scope, rule, ""); err != nil {
				data["Error"] = err.Error()
				break
			}
			data["Done"] = "Added: " + rule.String() + " on " + scopeLabel(scope)
		}
	}
	var rules []HubRule
	var servers []string
	tags := map[string]bool{}
	h.store.View(func(d *stateData) {
		for _, x := range d.Rules {
			rules = append(rules, *x)
		}
		for _, a := range d.Agents {
			servers = append(servers, a.Name)
			for _, t := range a.Tags {
				tags[t] = true
			}
		}
	})
	sort.Strings(servers)
	var tagList []string
	for t := range tags {
		tagList = append(tagList, t)
	}
	sort.Strings(tagList)
	type ruleView struct {
		HubRule
		Text, ScopeText string
	}
	var views []ruleView
	for _, x := range rules {
		views = append(views, ruleView{x, x.Rule.String(), scopeLabel(x.Scope)})
	}
	data["Rules"], data["Servers"], data["Tags"] = views, servers, tagList
	renderTemplate(w, http.StatusOK, dashRulesTmpl, data)
}

func validScope(s string) bool {
	return s == "all" || (strings.HasPrefix(s, "server:") && len(s) > 7) || (strings.HasPrefix(s, "tag:") && len(s) > 4)
}

func scopeLabel(s string) string {
	switch {
	case s == "all":
		return "all servers"
	case strings.HasPrefix(s, "server:"):
		return s[7:]
	case strings.HasPrefix(s, "tag:"):
		return "servers tagged " + s[4:]
	}
	return s
}

// ruleFromForm builds an allow rule from the simple "add rule" form.
func ruleFromForm(kind, value string, anyArgs bool, as string) (policy.Rule, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return policy.Rule{}, errors.New("enter a command or path")
	}
	r := policy.Rule{}
	if as != "" && as != "root" {
		r.As = as
	}
	switch kind {
	case "command":
		args, err := splitCommand(value)
		if err != nil {
			return r, err
		}
		for _, a := range args {
			r.Argv = append(r.Argv, policy.EscapeArg(a))
		}
		if anyArgs {
			r.Argv = append(r.Argv, "**")
		}
	case "script":
		r.Shell = value
	case "write":
		if !strings.HasPrefix(value, "/") {
			return r, errors.New("paths must be absolute, e.g. /srv/app/config/**")
		}
		r.WritePaths = []string{value}
	case "read":
		if !strings.HasPrefix(value, "/") {
			return r, errors.New("paths must be absolute")
		}
		r.ReadPaths = []string{value}
	default:
		return r, errors.New("unknown rule type")
	}
	return r, nil
}

// splitCommand splits a command line like a shell would for simple cases:
// whitespace separates words; single and double quotes group them. Shell
// operators are rejected because rules match a single command.
func splitCommand(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord := false
	var quote rune
	for _, c := range s {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote, inWord = c, true
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		case strings.ContainsRune(";&|<>`$", c):
			return nil, errors.New("rules match one plain command; for pipes or ; use the \"exact shell script\" type")
		default:
			cur.WriteRune(c)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unclosed quote")
	}
	if inWord {
		out = append(out, cur.String())
	}
	if len(out) > 0 && out[0] == "sudo" {
		out = out[1:] // commands already run as root
	}
	if len(out) == 0 {
		return nil, errors.New("enter a command")
	}
	return out, nil
}
