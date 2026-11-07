package hub

import (
	"html/template"
	"net/http"
)

const pageCSS = `
body{font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;background:#f5f5f4;color:#1c1917;margin:0;padding:24px}
main{max-width:560px;margin:0 auto;background:#fff;border-radius:12px;padding:24px;box-shadow:0 1px 3px rgba(0,0,0,.08)}
h1{font-size:1.3rem;margin:0 0 12px}
p,li{line-height:1.5}
label{display:block;margin:14px 0 4px;font-weight:600}
input[type=password],input[type=text],select{width:100%;box-sizing:border-box;padding:10px;font-size:1rem;border:1px solid #d6d3d1;border-radius:8px}
pre{background:#1c1917;color:#fafaf9;padding:12px;border-radius:8px;white-space:pre-wrap;word-break:break-all;font-size:.85rem}
.row{display:flex;gap:8px;flex-wrap:wrap;margin-top:18px}
button{flex:1;padding:12px;font-size:1rem;border:0;border-radius:8px;cursor:pointer}
.primary{background:#c2410c;color:#fff}.secondary{background:#e7e5e4}.danger{background:#fee2e2;color:#991b1b}
.err{background:#fee2e2;color:#991b1b;padding:10px;border-radius:8px}
.ok{background:#dcfce7;color:#166534;padding:10px;border-radius:8px}
.meta{color:#57534e;font-size:.9rem}
.card{border:1px solid #e7e5e4;border-radius:10px;padding:12px;margin:12px 0}
main{max-width:960px}
.tablewrap{overflow-x:auto}
table{width:100%;border-collapse:collapse;font-size:.9rem}
th,td{text-align:left;padding:8px 6px;border-bottom:1px solid #e7e5e4;vertical-align:top}
th{color:#57534e;font-weight:600}
td code{word-break:break-all}
.nowrap{white-space:nowrap}
button.small{flex:none;padding:6px 10px;font-size:.85rem}
.filter input[type=text]{flex:2}.filter select{flex:1;width:auto}
th a{color:inherit;text-decoration:none}
.check{display:flex;gap:8px;align-items:center;margin-top:14px;font-weight:400}
`

var base = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="same-origin"><title>{{block "title" .}}Claude Server Agent{{end}}</title><style>` + pageCSS + `</style></head><body><main>{{block "body" .}}{{end}}</main></body></html>`

var loginTmpl = template.Must(template.Must(template.New("login").Parse(base)).Parse(`
{{define "title"}}Connect to Claude Server Agent{{end}}
{{define "body"}}
<h1>Connect “{{.Client}}”</h1>
<p>This gives <b>{{.Client}}</b> (returning to <b>{{.RedirectHost}}</b>) access to all servers on this hub. Actions that aren't read-only or allowlisted still need your approval.</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" autocomplete="off">
{{range $k, $v := .Params}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
<label for="password">Admin password</label>
<input id="password" name="password" type="password" autocomplete="current-password" required autofocus>
<label for="code">Authenticator code</label>
<input id="code" name="code" type="text" inputmode="numeric" pattern="[0-9 ]*" autocomplete="one-time-code" required>
<div class="row"><button class="primary" name="action" value="allow">Allow access</button><button class="secondary" name="action" value="deny" formnovalidate>Cancel</button></div>
</form>
{{end}}`))

var approveTmpl = template.Must(template.Must(template.New("approve").Parse(base)).Parse(`
{{define "title"}}Approve action on {{.A.Server}}{{end}}
{{define "body"}}
<p class="meta"><a href="/approvals">Review all pending approvals →</a></p>
<h1>Approve action on <b>{{.A.Server}}</b>?</h1>
<p class="meta">Requested by {{.A.Caller}} · {{.A.CreatedAt.Format "2006-01-02 15:04:05 MST"}} · status: <b>{{.A.Status}}</b></p>
<pre>{{.A.Summary}}</pre>
{{if .A.Reason}}<p><b>Reason given:</b> {{.A.Reason}}</p>{{end}}
<p class="meta">Needs approval because: {{.A.Why}}</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
{{if .Done}}<p class="ok">{{.Done}}</p>
{{else if eq .A.Status "pending"}}
<form method="post" autocomplete="off">
<label for="code">Authenticator code</label>
<input id="code" name="code" type="text" inputmode="numeric" pattern="[0-9 ]*" autocomplete="one-time-code" required autofocus>
<label class="check"><input type="checkbox" name="always" value="1"> Always allow this exact action from now on</label>
<label for="scope">…on</label>
<select id="scope" name="scope">
<option value="server">this server ({{.A.Server}})</option>
{{range .A.ServerTags}}<option value="tag:{{.}}">servers tagged “{{.}}”</option>{{end}}
<option value="all">all servers</option>
</select>
<p class="meta">Rule that would be added: <code>{{.Rule}}</code></p>
<div class="row"><button class="primary" name="decision" value="approve">Approve</button><button class="danger" name="decision" value="deny">Deny</button></div>
</form>
{{end}}
{{end}}`))

var msgTmpl = template.Must(template.Must(template.New("msg").Parse(base)).Parse(`
{{define "body"}}<h1>{{.Title}}</h1><p>{{.Msg}}</p>{{end}}`))

func renderTemplate(w http.ResponseWriter, status int, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' https: http://localhost:* http://127.0.0.1:*; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	t.Execute(w, data)
}

func renderPage(w http.ResponseWriter, status int, title, msg string, _ any) {
	renderTemplate(w, status, msgTmpl, map[string]string{"Title": title, "Msg": msg})
}

const dashNav = `<p class="meta"><a href="/approvals">Approvals</a> · <a href="/rules">Allow rules</a> · <a href="/logout">Log out</a></p>`

var dashLoginTmpl = template.Must(template.Must(template.New("dlogin").Parse(base)).Parse(`
{{define "title"}}Claude Server Agent — log in{{end}}
{{define "body"}}
<h1>Log in</h1>
<p>Review approvals and manage allow rules for all servers. You stay logged in for 24 hours on this browser.</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" autocomplete="off">
<input type="hidden" name="next" value="{{.Next}}">
<label for="password">Admin password</label>
<input id="password" name="password" type="password" autocomplete="current-password" required autofocus>
<label for="code">Authenticator code</label>
<input id="code" name="code" type="text" inputmode="numeric" pattern="[0-9 ]*" autocomplete="one-time-code" required>
<div class="row"><button class="primary">Log in</button></div>
</form>
{{end}}`))

var dashApprovalsTmpl = template.Must(template.Must(template.New("dappr").Funcs(template.FuncMap{"first": func(a []Approval) Approval { return a[0] }}).Parse(base)).Parse(`
{{define "title"}}Approvals — Claude Server Agent{{end}}
{{define "body"}}
` + dashNav + `
<h1>Pending approvals</h1>
{{if .Done}}<p class="ok">{{.Done}}</p>{{end}}
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
{{if .Groups}}
<form method="post">
{{range .Groups}}
<div class="card">
{{if eq .Tool "request_rule"}}<p class="meta"><b>Rule request</b> — Claude asks to always allow:</p>{{end}}
<pre>{{.Summary}}</pre>
{{if .Reason}}<p class="meta">Reason: {{.Reason}}</p>{{end}}
{{if ne .Tool "request_rule"}}<p class="meta">{{.Why}} · rule if always allowed: <code>{{.Rule}}</code></p>{{end}}
{{range .Approvals}}<label class="check"><input type="checkbox" name="id" value="{{.ID}}" checked> {{.Server}} <span class="meta">· {{.Caller}} · {{.CreatedAt.Format "15:04"}}</span></label>{{end}}
</div>
{{end}}
<label for="scope">"Approve + always allow" applies to</label>
<select id="scope" name="scope">
<option value="all">all servers</option>
<option value="server">only the server(s) each action came from</option>
</select>
<div class="row">
<button class="primary" name="action" value="always">Approve + always allow</button>
<button class="secondary" name="action" value="approve">Approve once</button>
<button class="danger" name="action" value="deny">Deny</button>
</div>
<p class="meta">Rule requests from Claude are added with the servers they name when approved (either button).</p>
</form>
{{else}}
<p>Nothing is waiting for approval.</p>
{{end}}
{{if .Recent}}<h1 style="margin-top:28px">Recent</h1>
{{range .Recent}}<p class="meta"><b>{{.Status}}</b> · {{.Server}} · {{.Summary}}</p>{{end}}{{end}}
{{end}}`))

var dashRulesTmpl = template.Must(template.Must(template.New("drules").Parse(base)).Parse(`
{{define "title"}}Allow rules — Claude Server Agent{{end}}
{{define "body"}}
` + dashNav + `
<h1>Always-allowed actions</h1>
{{if .Done}}<p class="ok">{{.Done}}</p>{{end}}
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" autocomplete="off">
<input type="hidden" name="op" value="add">
<label for="kind">Allow</label>
<select id="kind" name="kind">
<option value="command">a command</option>
<option value="script">an exact shell script</option>
<option value="write">writing files under a path</option>
<option value="read">reading a sensitive file/path</option>
</select>
<label for="value">Command, script or path</label>
<input id="value" name="value" type="text" placeholder="systemctl restart nginx   ·   /srv/app/config/**" required>
<label class="check"><input type="checkbox" name="any_args" value="1"> also allow any extra arguments after the command</label>
<label for="scope">On</label>
<select id="scope" name="scope">
<option value="all">all servers</option>
{{range .Servers}}<option value="server:{{.}}">{{.}}</option>{{end}}
{{range .Tags}}<option value="tag:{{.}}">servers tagged “{{.}}”</option>{{end}}
</select>
<label for="as">Run as user (optional, default root)</label>
<input id="as" name="as" type="text" placeholder="root">
<label for="comment">Note (optional)</label>
<input id="comment" name="comment" type="text">
<div class="row"><button class="primary">Add rule</button></div>
</form>
<h1 style="margin-top:28px">Built in on every server (no rule needed)</h1>
<div class="card"><p><b>Always allowed:</b></p>{{range .BuiltinAllow}}<p class="meta">{{.}}</p>{{end}}</div>
<div class="card"><p><b>Read-only commands</b> (run freely; state-changing flags excluded):</p><p class="meta">{{range $i, $c := .BuiltinRead}}{{if $i}}, {{end}}{{$c}}{{end}}</p></div>
<p class="meta">Each server's own /etc/csa/policy.yaml can add more rules, and its deny rules always win.</p>
<h1 style="margin-top:28px">Current rules</h1>
<form method="get" class="row filter">
<input type="text" name="q" value="{{.Q}}" placeholder="Search rules…">
<select name="scope">
<option value="">All targets</option>
<option value="all" {{if eq .Scope "all"}}selected{{end}}>all servers</option>
{{range .Servers}}<option value="server:{{.}}" {{if eq $.Scope (printf "server:%s" .)}}selected{{end}}>{{.}}</option>{{end}}
{{range .Tags}}<option value="tag:{{.}}" {{if eq $.Scope (printf "tag:%s" .)}}selected{{end}}>tag {{.}}</option>{{end}}
</select>
<input type="hidden" name="sort" value="{{.Sort}}"><input type="hidden" name="dir" value="{{.Dir}}">
<button class="secondary small">Filter</button>{{if or .Q .Scope}} <a class="meta" href="/rules">clear</a>{{end}}
</form>
{{if .Rules}}
<div class="tablewrap"><table>
<thead><tr>
<th><a href="{{index .SortLinks "rule"}}">Rule{{if eq .Sort "rule"}}{{if eq .Dir "desc"}} ▼{{else}} ▲{{end}}{{end}}</a></th>
<th><a href="{{index .SortLinks "scope"}}">Applies to{{if eq .Sort "scope"}}{{if eq .Dir "desc"}} ▼{{else}} ▲{{end}}{{end}}</a></th>
<th><a href="{{index .SortLinks "added"}}">Added{{if or (eq .Sort "added") (eq .Sort "")}}{{if eq .Dir "desc"}} ▼{{else}} ▲{{end}}{{end}}</a></th>
<th><a href="{{index .SortLinks "note"}}">Note{{if eq .Sort "note"}}{{if eq .Dir "desc"}} ▼{{else}} ▲{{end}}{{end}}</a></th>
<th></th></tr></thead>
<tbody>
{{range .Rules}}<tr>
<td><code>{{.Text}}</code></td>
<td>{{.ScopeText}}</td>
<td class="nowrap">{{.CreatedAt.Format "2006-01-02"}}</td>
<td class="meta">{{.Rule.Comment}}</td>
<td><form method="post"><input type="hidden" name="op" value="delete"><input type="hidden" name="rule_id" value="{{.ID}}"><button class="danger small" title="Remove this rule">Remove</button></form></td>
</tr>{{end}}
</tbody></table></div>
<p class="meta">Showing {{len .Rules}} of {{.Total}} rule(s)</p>
{{else}}<p>{{if .Total}}No rules match.{{else}}No rules yet.{{end}}</p>{{end}}
{{end}}`))
