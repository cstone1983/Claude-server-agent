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
.check{display:flex;gap:8px;align-items:center;margin-top:14px;font-weight:400}
`

var base = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="no-referrer"><title>{{block "title" .}}Claude Server Agent{{end}}</title><style>` + pageCSS + `</style></head><body><main>{{block "body" .}}{{end}}</main></body></html>`

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
