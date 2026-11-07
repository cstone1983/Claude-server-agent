package hub

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/policy"
)

// Browser dashboard: log in once (password + authenticator code), then review
// and bulk-approve pending actions and manage "always allow" rules.

const sessionCookie = "csa_session"

// sessions are dashboard logins. They are stored (hashed) in the hub state so
// they survive hub restarts and updates.
type sessions struct {
	store *Store
	ttl   time.Duration
}

func (s *sessions) create() string {
	tok := randomToken("", 32)
	s.store.Update(func(d *stateData) error {
		now := time.Now()
		for k, exp := range d.Sessions {
			if now.After(exp) {
				delete(d.Sessions, k)
			}
		}
		d.Sessions[hashSecret(tok)] = now.Add(s.ttl)
		return nil
	})
	return tok
}

func (s *sessions) valid(tok string) bool {
	if tok == "" {
		return false
	}
	ok := false
	s.store.View(func(d *stateData) {
		exp, found := d.Sessions[hashSecret(tok)]
		ok = found && time.Now().Before(exp)
	})
	return ok
}

func (s *sessions) remove(tok string) {
	s.store.Update(func(d *stateData) error { delete(d.Sessions, hashSecret(tok)); return nil })
}

func (h *Hub) loggedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && h.sess.valid(c.Value)
}

// csrf is Go's standard cross-site request protection: it trusts the
// browser's Sec-Fetch-Site header and falls back to comparing Origin with
// Host. (A plain Origin comparison breaks with privacy settings that make
// browsers send "Origin: null" on same-site form posts.)
var csrf = http.NewCrossOriginProtection()

// sameOrigin rejects cross-site form posts (CSRF). The session cookie is also
// SameSite=Strict.
func (h *Hub) sameOrigin(r *http.Request) bool {
	return csrf.Check(r) == nil
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
	return "/"
}

func (h *Hub) handleLogin(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.FormValue("next"))
	data := map[string]any{"Next": next, "Hours": h.cfg.DashboardSessionHours, "MCP": h.cfg.PublicURL + "/mcp"}
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
			Name: sessionCookie, Value: tok, Path: "/", MaxAge: int(h.sess.ttl.Seconds()),
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
	h.renderPanel(w, http.StatusOK, dashApprovalsTmpl, data, 30)
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
		case "toggle":
			id := r.PostFormValue("rule_id")
			var now bool
			found := false
			h.store.Update(func(d *stateData) error {
				for _, x := range d.Rules {
					if x.ID == id {
						x.Disabled = !x.Disabled
						now, found = x.Disabled, true
					}
				}
				return nil
			})
			if found {
				h.audit.Write(audit.Entry{Event: "rule_toggled", Detail: map[string]any{"id": id, "disabled": now, "via": "dashboard"}})
				go h.pushRulesAll()
				if now {
					data["Done"] = "Rule disabled: that action needs approval again."
				} else {
					data["Done"] = "Rule enabled."
				}
			}
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
				data["FormOpen"] = true
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
	if r.URL.Query().Get("saved") == "1" && data["Done"] == nil {
		data["Done"] = "Rule saved."
	}
	views, total := filterSortRules(rules, r.URL.Query())
	q := r.URL.Query()
	data["Rules"], data["Total"], data["Servers"], data["Tags"] = views, total, servers, tagList
	data["Q"], data["Scope"], data["Sort"], data["Dir"] = q.Get("q"), q.Get("scope"), q.Get("sort"), q.Get("dir")
	data["SortLinks"] = sortLinks(q)
	data["BuiltinRead"], data["BuiltinAllow"] = policy.BuiltinSummary()
	h.renderPanel(w, http.StatusOK, dashRulesTmpl, data, 0)
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

type ruleView struct {
	HubRule
	Text, ScopeText string
}

// filterSortRules applies the rules page's search (?q=), scope filter
// (?scope=) and sort (?sort=rule|scope|added|note&dir=asc|desc).
func filterSortRules(rules []HubRule, q url.Values) ([]ruleView, int) {
	needle := strings.ToLower(strings.TrimSpace(q.Get("q")))
	scope := q.Get("scope")
	var out []ruleView
	for _, x := range rules {
		v := ruleView{x, x.Rule.Describe(), scopeLabel(x.Scope)}
		if scope != "" && x.Scope != scope {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(v.Text+" "+v.ScopeText+" "+x.Rule.Comment+" "+x.ID), needle) {
			continue
		}
		out = append(out, v)
	}
	desc := q.Get("dir") == "desc"
	key := q.Get("sort")
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if desc {
			a, b = b, a
		}
		var less bool
		switch key {
		case "rule":
			less = strings.ToLower(a.Text) < strings.ToLower(b.Text)
		case "scope":
			less = strings.ToLower(a.ScopeText) < strings.ToLower(b.ScopeText)
		case "note":
			less = strings.ToLower(a.Rule.Comment) < strings.ToLower(b.Rule.Comment)
		default: // added
			less = a.CreatedAt.Before(b.CreatedAt)
		}
		return less
	})
	return out, len(rules)
}

// sortLinks builds header links that sort by a column, toggling direction
// when it is already the active sort, and keep the current search/filter.
func sortLinks(q url.Values) map[string]string {
	links := map[string]string{}
	cur, dir := q.Get("sort"), q.Get("dir")
	if cur == "" {
		cur = "added"
	}
	for _, col := range []string{"rule", "scope", "added", "note"} {
		v := url.Values{}
		if s := q.Get("q"); s != "" {
			v.Set("q", s)
		}
		if s := q.Get("scope"); s != "" {
			v.Set("scope", s)
		}
		v.Set("sort", col)
		if col == cur && dir != "desc" {
			v.Set("dir", "desc")
		}
		links[col] = "/rules?" + v.Encode()
	}
	return links
}
