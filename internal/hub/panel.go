package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/policy"
	"github.com/cstone1983/Claude-server-agent/internal/proto"
)

// ---------- shared ----------

type serverView struct {
	ID, Name, Hostname, OS, Arch, Version, Latest string
	Tags                                          []string
	Online, UpdateAvailable                       bool
	LastSeen                                      string
	// metrics
	HasMetrics          bool
	Uptime              string
	Load                string
	CPUs                int
	LoadPct             int
	LoadClass, MemClass string
	MemPct              int
	MemTotal            string
	Disks               []diskView
	RebootRequired      bool
	FailedUnits         []string
	PkgUpdates          string
	LoadSpark, MemSpark template.HTML
}

type diskView struct {
	Mount, Size, Class string
	UsedPct            int
}

func pctClass(p float64) string {
	switch {
	case p >= 90:
		return "crit"
	case p >= 80:
		return "warn"
	}
	return ""
}

func (h *Hub) serverViews() []serverView {
	var recs []AgentRec
	h.store.View(func(d *stateData) {
		for _, a := range d.Agents {
			recs = append(recs, *a)
		}
	})
	sort.Slice(recs, func(i, j int) bool { return strings.ToLower(recs[i].Name) < strings.ToLower(recs[j].Name) })
	var out []serverView
	for _, a := range recs {
		latest, upd := h.updateAvailable(a.AgentVersion)
		v := serverView{ID: a.ID, Name: a.Name, Hostname: a.Hostname, OS: a.OS, Arch: a.Arch, Version: versionOf(a.AgentVersion),
			Latest: latest, Tags: a.Tags, Online: h.reg.online(a.ID), UpdateAvailable: upd}
		if !a.LastSeen.IsZero() {
			v.LastSeen = a.LastSeen.UTC().Format(time.RFC3339)
		}
		if m, ok := h.metrics.get(a.ID); ok && time.Since(m.At) < 10*time.Minute {
			si := m.Latest
			v.HasMetrics = true
			v.Uptime = humanHours(si.UptimeHours)
			v.Load, v.CPUs = si.Load, si.CPUs
			var load1 float64
			fmt.Sscan(strings.Fields(si.Load + " 0")[0], &load1)
			lp := 0.0
			if si.CPUs > 0 {
				lp = 100 * load1 / float64(si.CPUs)
			}
			v.LoadPct, v.LoadClass = clamp(lp), pctClass(lp)
			if si.MemTotalMB > 0 {
				mp := 100 * float64(si.MemTotalMB-si.MemAvailMB) / float64(si.MemTotalMB)
				v.MemPct, v.MemClass = clamp(mp), pctClass(mp)
				v.MemTotal = fmt.Sprintf("%.1f GB", float64(si.MemTotalMB)/1024)
			}
			for _, d := range si.Disks {
				v.Disks = append(v.Disks, diskView{Mount: d.Mount, Size: humanGB(d.SizeGB), UsedPct: clamp(d.UsedPct), Class: pctClass(d.UsedPct)})
			}
			v.RebootRequired, v.FailedUnits = si.RebootRequired, si.FailedUnits
			v.PkgUpdates = pkgUpdatesSummary(si.Updates)
			var loads, mems []float64
			for _, s := range m.Samples {
				loads = append(loads, s.Load1)
				mems = append(mems, s.MemPct)
			}
			v.LoadSpark = sparkline(loads, float64(max(si.CPUs, 1)), "var(--brand-1)")
			v.MemSpark = sparkline(mems, 100, "var(--brand-2)")
		}
		out = append(out, v)
	}
	return out
}

func humanGB(gb float64) string {
	switch {
	case gb >= 1000:
		return fmt.Sprintf("%.1f TB", gb/1000)
	case gb >= 10:
		return fmt.Sprintf("%.0f GB", gb)
	case gb >= 1:
		return fmt.Sprintf("%.1f GB", gb)
	}
	return fmt.Sprintf("%.0f MB", gb*1000)
}

func clamp(p float64) int { return int(min(100, max(0, p))) }

func humanHours(hrs float64) string {
	switch {
	case hrs < 1:
		return fmt.Sprintf("%.0f min", hrs*60)
	case hrs < 48:
		return fmt.Sprintf("%.0f h", hrs)
	}
	return fmt.Sprintf("%.0f days", hrs/24)
}

// pkgUpdatesSummary turns Ubuntu's update-notifier text into "N updates".
func pkgUpdatesSummary(s string) string {
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "can be applied immediately") {
			n := strings.Fields(part)[0]
			if n != "0" {
				return n + " package updates"
			}
		}
	}
	return ""
}

func (h *Hub) serverNamesAndTags() ([]string, []string) {
	var servers []string
	tags := map[string]bool{}
	h.store.View(func(d *stateData) {
		for _, a := range d.Agents {
			servers = append(servers, a.Name)
			for _, t := range a.Tags {
				tags[t] = true
			}
		}
	})
	sort.Strings(servers)
	var tl []string
	for t := range tags {
		tl = append(tl, t)
	}
	sort.Strings(tl)
	return servers, tl
}

// ---------- overview ----------

func (h *Hub) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !h.loggedIn(r) {
		renderPage(w, http.StatusOK, "Claude Server Agent hub", "Add "+h.cfg.PublicURL+"/mcp as a custom connector in Claude (Settings → Connectors). To manage servers, log in at "+h.cfg.PublicURL+"/login.", nil)
		return
	}
	servers := h.serverViews()
	online, attention, updates := 0, 0, 0
	for _, s := range servers {
		if s.Online {
			online++
		}
		if !s.Online || s.RebootRequired || len(s.FailedUnits) > 0 || s.MemClass == "crit" {
			attention++
		} else {
			for _, d := range s.Disks {
				if d.Class != "" {
					attention++
					break
				}
			}
		}
		if s.UpdateAvailable {
			updates++
		}
	}
	h.renderPanel(w, http.StatusOK, overviewTmpl, map[string]any{"Servers": servers, "Online": online, "Attention": attention, "Updates": updates}, 60)
}

// ---------- servers ----------

type joinInfo struct {
	Token, Command string
	Expires        time.Time
	Uses           int
}

func (h *Hub) createJoinToken(ttlMinutes, uses int, tags []string) (string, time.Time) {
	if ttlMinutes <= 0 {
		ttlMinutes = 60
	}
	if uses <= 0 {
		uses = 1
	}
	tok := randomToken("csa_join_", 24)
	exp := time.Now().Add(time.Duration(ttlMinutes) * time.Minute).UTC()
	h.store.Update(func(d *stateData) error {
		d.JoinTokens[hashSecret(tok)] = &JoinToken{ExpiresAt: exp, UsesLeft: uses, Tags: tags, CreatedAt: time.Now().UTC()}
		return nil
	})
	h.audit.Write(audit.Entry{Event: "join_token_created", Detail: map[string]any{"uses": uses, "expires": exp}})
	return tok, exp
}

// InstallCommand is the one-line agent install command for a join token.
func InstallCommand(hubURL, token string) string {
	return "command -v curl >/dev/null || { sudo apt-get update -qq && sudo apt-get install -y -qq curl; }; curl -fsSL https://raw.githubusercontent.com/cstone1983/Claude-server-agent/main/install.sh | sudo bash -s -- agent --hub " + hubURL + " --join " + token
}

func (h *Hub) handleServers(w http.ResponseWriter, r *http.Request) {
	if !h.requireLogin(w, r) {
		return
	}
	data := map[string]any{}
	if r.Method == http.MethodPost {
		r.ParseForm()
		name := r.PostFormValue("name")
		via := "dashboard:" + clientIP(r)
		switch r.PostFormValue("op") {
		case "join":
			uses, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("uses")))
			ttl, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("ttl")))
			var tags []string
			for _, t := range strings.Split(r.PostFormValue("tags"), ",") {
				if t = strings.TrimSpace(t); t != "" {
					if !nameRE.MatchString(t) {
						data["Error"] = "invalid tag " + t
					}
					tags = append(tags, t)
				}
			}
			if data["Error"] == nil {
				tok, exp := h.createJoinToken(min(max(ttl, 1), 7*24*60), min(max(uses, 1), 100), tags)
				data["Join"] = joinInfo{Token: tok, Command: InstallCommand(h.cfg.PublicURL, tok), Expires: exp, Uses: max(uses, 1)}
			}
		case "tags":
			var tags []string
			for _, t := range strings.Split(r.PostFormValue("tags"), ",") {
				if t = strings.TrimSpace(t); t != "" {
					tags = append(tags, t)
				}
			}
			if err := h.setTags(name, tags); err != nil {
				data["Error"] = err.Error()
			} else {
				data["Done"] = "Tags updated for " + name + "."
			}
		case "rename":
			newName := strings.TrimSpace(r.PostFormValue("new_name"))
			if n, err := h.renameServer(name, newName); err != nil {
				data["Error"] = err.Error()
			} else {
				data["Done"] = fmt.Sprintf("Renamed %s to %s (%d rule(s) moved).", name, newName, n)
			}
		case "remove":
			if err := h.removeServer(name); err != nil {
				data["Error"] = err.Error()
			} else {
				data["Done"] = "Removed " + name + ". Uninstall the agent on that machine too: sudo bash install.sh uninstall"
			}
		case "update":
			if err := h.startAgentUpdate(r.Context(), name, via); err != nil {
				data["Error"] = err.Error()
			} else {
				data["Done"] = "Update started on " + name + ". It reconnects within about a minute."
			}
		}
	}
	data["Servers"] = h.serverViews()
	h.renderPanel(w, http.StatusOK, serversTmpl, data, 0)
}

func (h *Hub) agentByName(name string) (*AgentRec, bool) {
	var rec *AgentRec
	h.store.View(func(d *stateData) {
		for _, a := range d.Agents {
			if a.Name == name {
				cp := *a
				rec = &cp
			}
		}
	})
	return rec, rec != nil
}

func (h *Hub) setTags(name string, tags []string) error {
	for _, t := range tags {
		if !nameRE.MatchString(t) {
			return errors.New("invalid tag " + t)
		}
	}
	found := false
	h.store.Update(func(d *stateData) error {
		for _, a := range d.Agents {
			if a.Name == name {
				a.Tags = dedupe(tags)
				found = true
			}
		}
		return nil
	})
	if !found {
		return errors.New("no such server " + name)
	}
	h.audit.Write(audit.Entry{Event: "server_tagged", Server: name, Detail: map[string]any{"tags": tags}})
	go h.pushRulesAll()
	return nil
}

func (h *Hub) renameServer(oldName, newName string) (int, error) {
	if !nameRE.MatchString(newName) {
		return 0, errors.New("invalid name (letters, digits, . _ -; max 63)")
	}
	rules := 0
	err := h.store.Update(func(d *stateData) error {
		var target *AgentRec
		for _, a := range d.Agents {
			if a.Name == newName && newName != oldName {
				return errors.New("another server is already called " + newName)
			}
			if a.Name == oldName {
				target = a
			}
		}
		if target == nil {
			return errors.New("no such server " + oldName)
		}
		target.Name = newName
		for _, rl := range d.Rules {
			if rl.Scope == "server:"+oldName {
				rl.Scope = "server:" + newName
				rules++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	h.audit.Write(audit.Entry{Event: "server_renamed", Server: newName, Detail: map[string]any{"old_name": oldName, "rules_moved": rules}})
	return rules, nil
}

func (h *Hub) removeServer(name string) error {
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
		return errors.New("no such server " + name)
	}
	if c := h.reg.get(removed); c != nil {
		c.ws.Close()
	}
	h.audit.Write(audit.Entry{Event: "server_removed", Server: name})
	return nil
}

func (h *Hub) startAgentUpdate(ctx context.Context, name, via string) error {
	rec, ok := h.agentByName(name)
	if !ok {
		return errors.New("no such server " + name)
	}
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	res, err := h.callAgent(cctx, rec.ID, &proto.Call{Tool: "update_server", Args: json.RawMessage(`{}`), Caller: via})
	if err != nil {
		return err
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	return nil
}

// ---------- activity ----------

type activityEntry struct {
	Time, Event, Caller, Summary, Server, Tier, Rule, Exit, Error, Class string
}

func entryFromJSON(b []byte) (activityEntry, bool) {
	var e audit.Entry
	if json.Unmarshal(b, &e) != nil {
		return activityEntry{}, false
	}
	a := activityEntry{Time: e.Time.UTC().Format(time.RFC3339), Event: strings.ReplaceAll(e.Event, "_", " "), Caller: e.Caller,
		Summary: truncate(e.Summary, 300), Server: e.Server, Tier: e.Tier, Error: e.Error}
	switch {
	case e.RuleID != "":
		a.Rule = e.RuleID
	case e.ApprovalID != "":
		a.Rule = "approval " + truncate(e.ApprovalID, 16)
	}
	if e.ExitCode != nil {
		a.Exit = strconv.Itoa(*e.ExitCode)
	}
	switch {
	case strings.Contains(e.Event, "denied") || strings.Contains(e.Event, "failed") || e.Error != "" || (e.ExitCode != nil && *e.ExitCode != 0):
		a.Class = "crit"
	case strings.Contains(e.Event, "approval_requested") || strings.Contains(e.Event, "requested"):
		a.Class = "warn"
	case strings.Contains(e.Event, "granted") || strings.Contains(e.Event, "executed") || strings.Contains(e.Event, "rule_added"):
		a.Class = "good"
	}
	return a, true
}

func (h *Hub) handleActivity(w http.ResponseWriter, r *http.Request) {
	if !h.requireLogin(w, r) {
		return
	}
	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("n"))
	if n <= 0 || n > 1000 {
		n = 200
	}
	server := q.Get("server")
	data := map[string]any{"Server": server, "N": n, "Sizes": []int{100, 200, 500, 1000}}
	data["Servers"], _ = h.serverNamesAndTags()
	var lines [][]byte
	if server == "" {
		raw, err := audit.Tail(h.audit.Path(), n)
		if err != nil && !os.IsNotExist(err) {
			data["Err"] = err.Error()
		}
		for _, l := range raw {
			lines = append(lines, l)
		}
	} else if rec, ok := h.agentByName(server); !ok {
		data["Err"] = "no such server"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		args, _ := json.Marshal(map[string]int{"lines": n})
		res, err := h.callAgent(ctx, rec.ID, &proto.Call{Tool: "audit_log", Args: args, Caller: "dashboard", MaxTier: "read"})
		if err != nil {
			data["Err"] = server + ": " + err.Error()
		} else {
			sc := bufio.NewScanner(strings.NewReader(res.Text))
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for sc.Scan() {
				lines = append(lines, append([]byte(nil), sc.Bytes()...))
			}
		}
	}
	var entries []activityEntry
	for i := len(lines) - 1; i >= 0; i-- { // newest first
		if e, ok := entryFromJSON(lines[i]); ok {
			entries = append(entries, e)
		}
	}
	data["Entries"] = entries
	h.renderPanel(w, http.StatusOK, activityTmpl, data, 0)
}

// ---------- rule editor ----------

// ruleToForm turns a stored rule back into the simple form fields. complex
// is set when it uses pattern features the form can't express.
func ruleToForm(r policy.Rule) (kind, value string, anyArgs, complex bool) {
	switch {
	case r.Shell != "":
		return "script", r.Shell, false, false
	case len(r.WritePaths) > 0:
		return "write", strings.Join(r.WritePaths, " "), false, len(r.WritePaths) > 1
	case len(r.ReadPaths) > 0:
		return "read", strings.Join(r.ReadPaths, " "), false, len(r.ReadPaths) > 1
	}
	var parts []string
	for i, a := range r.Argv {
		if a == "**" && i == len(r.Argv)-1 {
			anyArgs = true
			continue
		}
		if strings.HasPrefix(a, "=") {
			a = a[1:]
		} else if strings.ContainsAny(a, "|*?") {
			complex = true
		}
		if a == "" || strings.ContainsAny(a, " \t'\"") {
			a = "'" + strings.ReplaceAll(a, "'", `'"'"'`) + "'"
		}
		parts = append(parts, a)
	}
	if len(r.AnyArg) > 0 || len(r.NotArgs) > 0 {
		complex = true
	}
	return "command", strings.Join(parts, " "), anyArgs, complex
}

func (h *Hub) handleRuleEdit(w http.ResponseWriter, r *http.Request) {
	if !h.requireLogin(w, r) {
		return
	}
	id := r.FormValue("rule_id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	var hr *HubRule
	h.store.View(func(d *stateData) {
		for _, x := range d.Rules {
			if x.ID == id {
				cp := *x
				hr = &cp
			}
		}
	})
	if hr == nil {
		http.Redirect(w, r, "/rules", http.StatusSeeOther)
		return
	}
	data := map[string]any{}
	if r.Method == http.MethodPost {
		r.ParseForm()
		rule, err := ruleFromForm(r.PostFormValue("kind"), r.PostFormValue("value"), r.PostFormValue("any_args") == "1", strings.TrimSpace(r.PostFormValue("as")))
		scope := r.PostFormValue("scope")
		if err == nil && !validScope(scope) {
			err = errors.New("choose which servers the rule applies to")
		}
		if err == nil {
			rule.Comment = strings.TrimSpace(r.PostFormValue("comment"))
			disabled := r.PostFormValue("disabled") == "1"
			h.store.Update(func(d *stateData) error {
				for _, x := range d.Rules {
					if x.ID == id {
						x.Rule, x.Scope, x.Disabled, x.UpdatedAt = rule, scope, disabled, time.Now().UTC()
					}
				}
				return nil
			})
			h.audit.Write(audit.Entry{Event: "rule_edited", Summary: rule.String(), Detail: map[string]any{"id": id, "scope": scope, "disabled": disabled}})
			go h.pushRulesAll()
			http.Redirect(w, r, "/rules?saved=1", http.StatusSeeOther)
			return
		}
		data["Error"] = err.Error()
	}
	kind, value, anyArgs, complex := ruleToForm(hr.Rule)
	servers, tags := h.serverNamesAndTags()
	data["ID"], data["Kind"], data["Value"], data["AnyArgs"], data["Complex"] = hr.ID, kind, value, anyArgs, complex
	data["Scope"], data["As"], data["Comment"], data["Disabled"] = hr.Scope, hr.Rule.As, hr.Rule.Comment, hr.Disabled
	data["Servers"], data["Tags"] = servers, tags
	h.renderPanel(w, http.StatusOK, ruleEditTmpl, data, 0)
}
