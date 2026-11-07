// Package tools defines the tool catalogue shared by hub (MCP/REST schemas)
// and agent (argument parsing).
package tools

// Argument types. JSON field names are the public API.
type (
	SystemInfoArgs struct{}

	RunCommandArgs struct {
		Argv       []string `json:"argv"`
		As         string   `json:"as,omitempty"`
		Cwd        string   `json:"cwd,omitempty"`
		Stdin      string   `json:"stdin,omitempty"`
		TimeoutSec int      `json:"timeout_sec,omitempty"`
		Reason     string   `json:"reason,omitempty"`
	}

	RunShellArgs struct {
		Script     string `json:"script"`
		As         string `json:"as,omitempty"`
		Cwd        string `json:"cwd,omitempty"`
		TimeoutSec int    `json:"timeout_sec,omitempty"`
		Reason     string `json:"reason,omitempty"`
	}

	ReadFileArgs struct {
		Path   string `json:"path"`
		Offset int64  `json:"offset,omitempty"`
		Limit  int    `json:"limit,omitempty"`
		Reason string `json:"reason,omitempty"`
	}

	WriteFileArgs struct {
		Path       string `json:"path"`
		Content    string `json:"content"`
		Mode       string `json:"mode,omitempty"`
		Append     bool   `json:"append,omitempty"`
		CreateDirs bool   `json:"create_dirs,omitempty"`
		Reason     string `json:"reason,omitempty"`
	}

	EditFileArgs struct {
		Path       string `json:"path"`
		OldText    string `json:"old_text"`
		NewText    string `json:"new_text"`
		ReplaceAll bool   `json:"replace_all,omitempty"`
		Reason     string `json:"reason,omitempty"`
	}

	ListDirArgs struct {
		Path string `json:"path"`
	}

	ServiceArgs struct {
		Unit   string `json:"unit"`
		Action string `json:"action"`
		Reason string `json:"reason,omitempty"`
	}

	LogsArgs struct {
		Unit     string `json:"unit,omitempty"`
		File     string `json:"file,omitempty"`
		Lines    int    `json:"lines,omitempty"`
		Since    string `json:"since,omitempty"`
		Priority string `json:"priority,omitempty"`
		Grep     string `json:"grep,omitempty"`
	}

	StartJobArgs struct {
		Argv       []string `json:"argv,omitempty"`
		Script     string   `json:"script,omitempty"`
		As         string   `json:"as,omitempty"`
		Cwd        string   `json:"cwd,omitempty"`
		TimeoutSec int      `json:"timeout_sec,omitempty"`
		Reason     string   `json:"reason,omitempty"`
	}

	JobStatusArgs struct {
		JobID  string `json:"job_id,omitempty"`
		Offset int64  `json:"offset,omitempty"`
	}

	CancelJobArgs struct {
		JobID string `json:"job_id"`
	}

	UpdateServerArgs struct {
		CheckOnly bool `json:"check_only,omitempty"`
		Force     bool `json:"force,omitempty"`
	}

	AuditLogArgs struct {
		Lines int `json:"lines,omitempty"`
	}

	PolicyViewArgs struct{}
)

type Prop struct {
	Type  string
	Desc  string
	Items string
	Enum  []string
}

type Def struct {
	Name        string
	Title       string
	Description string
	Props       map[string]Prop
	Required    []string
	ReadOnly    bool // MCP readOnlyHint: never modifies the server
	Destructive bool
	// HubOnly tools are answered by the hub itself and take no server argument.
	HubOnly bool
}

// Schema returns the JSON Schema for the tool's input. If withServer, a
// required "server" property is added.
func (d Def) Schema(withServer bool) map[string]any {
	props := map[string]any{}
	required := append([]string{}, d.Required...)
	if withServer && !d.HubOnly {
		props["server"] = map[string]any{
			"type":        "string",
			"description": `Target server name (see list_servers), "tag:<tag>" for every server with that tag, or "all".`,
		}
		required = append([]string{"server"}, required...)
	}
	for name, p := range d.Props {
		s := map[string]any{"type": p.Type, "description": p.Desc}
		if p.Items != "" {
			s["items"] = map[string]any{"type": p.Items}
		}
		if len(p.Enum) > 0 {
			s["enum"] = p.Enum
		}
		props[name] = s
	}
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

var reason = Prop{Type: "string", Desc: "Why you are doing this. Shown to the user when approval is needed and recorded in the audit log."}
var as = Prop{Type: "string", Desc: `User to run as (default "root").`}
var timeout = Prop{Type: "integer", Desc: "Timeout in seconds (default 60). For anything longer than a few minutes use start_job."}

const approvalNote = " Read-only actions run immediately; actions matching the server's allowlist run immediately; anything else needs the user's approval: you'll get an approval link to show the user, then call check_approval."

// All is the tool catalogue.
var All = []Def{
	{
		Name: "list_servers", Title: "List servers", HubOnly: true, ReadOnly: true,
		Description: "List all servers connected to this hub, with online status, tags, OS and last-seen time. Call this first to learn server names.",
	},
	{
		Name: "check_approval", Title: "Check approval", HubOnly: true,
		Description: "Check a pending approval. If the user approved it, the action runs now (once) and its result is returned.",
		Props:       map[string]Prop{"approval_id": {Type: "string", Desc: "Approval ID returned when an action needed approval."}},
		Required:    []string{"approval_id"},
	},
	{
		Name: "request_rule", Title: "Request an allow rule", HubOnly: true,
		Description: "Ask the user to always allow a command (or exact script / writable path) so it runs without approval from now on. Nothing changes until the user approves it on the hub's approvals page (they can approve several requests at once). Use this when the user wants something whitelisted, or for routine actions they keep approving. Returns an approval_id; call check_approval to see the result.",
		Props: map[string]Prop{
			"kind":     {Type: "string", Desc: "What to allow (default command).", Enum: []string{"command", "script", "write", "read"}},
			"value":    {Type: "string", Desc: `The command line (e.g. "systemctl restart nginx"), exact bash script, or absolute path glob (e.g. "/srv/app/config/**").`},
			"any_args": {Type: "boolean", Desc: "For commands: also allow any extra arguments after the given ones."},
			"servers":  {Type: "string", Desc: `Where the rule applies: "all" (default), a server name, or "tag:<tag>".`},
			"as":       {Type: "string", Desc: `User the command may run as (default root).`},
			"reason":   {Type: "string", Desc: "Why this should be allowed; shown to the user."},
		},
		Required: []string{"value"},
	},
	{
		Name: "system_info", Title: "System info", ReadOnly: true,
		Description: "Overview of a server: OS, kernel, uptime, load, CPU, memory, disks, IPs, pending updates, reboot-required and failed systemd units.",
	},
	{
		Name: "run_command", Title: "Run command", Destructive: true,
		Description: "Run a command given as an argv array (no shell: pipes, globs and redirects are NOT interpreted; use run_shell for those). Returns exit code, stdout and stderr." + approvalNote,
		Props: map[string]Prop{
			"argv":        {Type: "array", Items: "string", Desc: `Command and arguments, e.g. ["systemctl","status","nginx"].`},
			"as":          as,
			"cwd":         {Type: "string", Desc: "Working directory (default: the user's home)."},
			"stdin":       {Type: "string", Desc: "Text to send on standard input."},
			"timeout_sec": timeout,
			"reason":      reason,
		},
		Required: []string{"argv"},
	},
	{
		Name: "run_shell", Title: "Run shell script", Destructive: true,
		Description: "Run a bash script (pipes, redirects, &&, loops). Always needs approval unless the exact script is allowlisted, so prefer run_command for single commands." + approvalNote,
		Props: map[string]Prop{
			"script":      {Type: "string", Desc: "Bash script to run with `bash -c`."},
			"as":          as,
			"cwd":         {Type: "string", Desc: "Working directory."},
			"timeout_sec": timeout,
			"reason":      reason,
		},
		Required: []string{"script"},
	},
	{
		Name: "read_file", Title: "Read file", ReadOnly: true,
		Description: "Read a text file. Sensitive files (keys, /etc/shadow, .env, ...) need approval.",
		Props: map[string]Prop{
			"path":   {Type: "string", Desc: "Absolute path."},
			"offset": {Type: "integer", Desc: "Byte offset to start at (default 0)."},
			"limit":  {Type: "integer", Desc: "Max bytes to return (default and max 512 KiB)."},
			"reason": reason,
		},
		Required: []string{"path"},
	},
	{
		Name: "write_file", Title: "Write file", Destructive: true,
		Description: "Create, overwrite or append to a file. The previous version is saved as <path>.csa-bak." + approvalNote,
		Props: map[string]Prop{
			"path":        {Type: "string", Desc: "Absolute path."},
			"content":     {Type: "string", Desc: "Full new content (or text to append)."},
			"mode":        {Type: "string", Desc: `Octal permissions for new files, e.g. "0644".`},
			"append":      {Type: "boolean", Desc: "Append instead of replacing."},
			"create_dirs": {Type: "boolean", Desc: "Create missing parent directories."},
			"reason":      reason,
		},
		Required: []string{"path", "content"},
	},
	{
		Name: "edit_file", Title: "Edit file", Destructive: true,
		Description: "Replace text in a file. old_text must occur exactly once unless replace_all is true. The previous version is saved as <path>.csa-bak." + approvalNote,
		Props: map[string]Prop{
			"path":        {Type: "string", Desc: "Absolute path."},
			"old_text":    {Type: "string", Desc: "Exact text to find."},
			"new_text":    {Type: "string", Desc: "Replacement text."},
			"replace_all": {Type: "boolean", Desc: "Replace every occurrence."},
			"reason":      reason,
		},
		Required: []string{"path", "old_text", "new_text"},
	},
	{
		Name: "list_dir", Title: "List directory", ReadOnly: true,
		Description: "List a directory: name, type, size, mode and modification time of each entry.",
		Props:       map[string]Prop{"path": {Type: "string", Desc: "Absolute path."}},
		Required:    []string{"path"},
	},
	{
		Name: "service", Title: "Manage systemd service", Destructive: true,
		Description: "Show or change a systemd unit. status is read-only; other actions follow the allowlist/approval rules.",
		Props: map[string]Prop{
			"unit":   {Type: "string", Desc: `Unit name, e.g. "nginx" or "docker.service".`},
			"action": {Type: "string", Desc: "What to do.", Enum: []string{"status", "start", "stop", "restart", "reload", "enable", "disable", "is-active", "is-enabled"}},
			"reason": reason,
		},
		Required: []string{"unit", "action"},
	},
	{
		Name: "logs", Title: "Read logs", ReadOnly: true,
		Description: "Read recent logs from the systemd journal (optionally for one unit) or the tail of a log file.",
		Props: map[string]Prop{
			"unit":     {Type: "string", Desc: "systemd unit to filter by."},
			"file":     {Type: "string", Desc: "Log file to tail instead of the journal, e.g. /var/log/nginx/error.log."},
			"lines":    {Type: "integer", Desc: "Number of lines (default 100, max 2000)."},
			"since":    {Type: "string", Desc: `journalctl --since value, e.g. "1 hour ago" or "2026-01-01 10:00".`},
			"priority": {Type: "string", Desc: `Max priority, e.g. "err" or "warning".`},
			"grep":     {Type: "string", Desc: "Only lines matching this pattern."},
		},
	},
	{
		Name: "start_job", Title: "Start background job", Destructive: true,
		Description: "Start a long-running command (argv) or script in the background, e.g. apt upgrades, builds or backups. Returns a job_id; poll with job_status." + approvalNote,
		Props: map[string]Prop{
			"argv":        {Type: "array", Items: "string", Desc: "Command and arguments (use this or script)."},
			"script":      {Type: "string", Desc: "Bash script (use this or argv)."},
			"as":          as,
			"cwd":         {Type: "string", Desc: "Working directory."},
			"timeout_sec": {Type: "integer", Desc: "Kill after this many seconds (default 3600)."},
			"reason":      reason,
		},
	},
	{
		Name: "job_status", Title: "Job status", ReadOnly: true,
		Description: "Get a background job's status and output from an offset. Without job_id, lists jobs.",
		Props: map[string]Prop{
			"job_id": {Type: "string", Desc: "Job ID from start_job."},
			"offset": {Type: "integer", Desc: "Output byte offset; pass next_offset from the previous call to stream."},
		},
	},
	{
		Name: "cancel_job", Title: "Cancel job",
		Description: "Kill a running background job.",
		Props:       map[string]Prop{"job_id": {Type: "string", Desc: "Job ID."}},
		Required:    []string{"job_id"},
	},
	{
		Name: "update_server", Title: "Update Claude Server Agent",
		Description: "Check for or install a newer Claude Server Agent release from GitHub on a server (or tag:<tag> / all). With check_only it only reports the installed and latest versions. Otherwise it starts the update in the background and returns immediately; the agent restarts and reconnects within about a minute (on the hub machine, the connector drops briefly). Then call list_servers to confirm the new version. Runs without an approval link because it only installs official releases, but ONLY call it (without check_only) after the user has explicitly said to update.",
		Props: map[string]Prop{
			"check_only": {Type: "boolean", Desc: "Only report whether an update is available."},
			"force":      {Type: "boolean", Desc: "Reinstall even if already up to date."},
		},
	},
	{
		Name: "policy_view", Title: "View policy", ReadOnly: true,
		Description: "Show the server's effective policy: built-in read-only commands, local allow/deny rules and hub-managed rules.",
	},
	{
		Name: "audit_log", Title: "Audit log", ReadOnly: true,
		Description: "Show the server's most recent audit log entries.",
		Props:       map[string]Prop{"lines": {Type: "integer", Desc: "Number of entries (default 50)."}},
	},
}

func Get(name string) (Def, bool) {
	for _, d := range All {
		if d.Name == name {
			return d, true
		}
	}
	return Def{}, false
}
