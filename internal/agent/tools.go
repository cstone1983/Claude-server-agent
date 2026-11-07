package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/policy"
	"github.com/cstone1983/Claude-server-agent/internal/proto"
	"github.com/cstone1983/Claude-server-agent/internal/tools"
)

// Executor runs tools under the local policy.
type Executor struct {
	cfg    *Config
	policy *policy.Policy
	audit  *audit.Log
	jobs   *jobManager
}

func NewExecutor(cfg *Config, p *policy.Policy, a *audit.Log) *Executor {
	return &Executor{cfg: cfg, policy: p, audit: a, jobs: newJobManager()}
}

// gate decides whether an action may run now. It returns a non-nil Result
// when the action must not run (denied, read-only cap, or needs approval).
func (e *Executor) gate(call *proto.Call, hash string, a policy.Action, why string) (policy.Decision, *proto.Result) {
	d := e.policy.Classify(a)
	entry := audit.Entry{Caller: call.Caller, Tool: call.Tool, Summary: a.Summary(), Tier: d.Tier, RuleID: d.RuleID, Detail: map[string]string{"reason": why}}
	switch d.Tier {
	case policy.TierDenied:
		entry.Event = "denied"
		e.audit.Write(entry)
		return d, &proto.Result{Error: "denied by this server's policy: " + d.Why}
	case policy.TierElevated:
		if call.Approval != nil && call.Approval.ActionHash == hash {
			d.Tier = "approved"
			d.RuleID = call.Approval.ID
			break
		}
		if call.MaxTier == policy.TierRead {
			entry.Event = "refused_read_only"
			e.audit.Write(entry)
			return d, &proto.Result{Error: "this caller is read-only; the action needs approval: " + d.Why}
		}
		entry.Event = "approval_requested"
		e.audit.Write(entry)
		return d, &proto.Result{NeedsApproval: &proto.NeedsApproval{
			ActionHash: hash, Summary: a.Summary(), Why: d.Why, SuggestedRule: policy.SuggestRule(a),
		}}
	case policy.TierAllowed:
		if call.MaxTier == policy.TierRead {
			return d, &proto.Result{Error: "this caller is read-only; the action is allowlisted but not read-only"}
		}
	}
	return d, nil
}

func actionHash(tool string, args any) string {
	b, _ := json.Marshal(args)
	h := sha256.Sum256(append([]byte(tool+"\n"), b...))
	return hex.EncodeToString(h[:])
}

func errResult(format string, a ...any) *proto.Result {
	return &proto.Result{Error: fmt.Sprintf(format, a...)}
}

func dataResult(text string, data any) *proto.Result {
	b, _ := json.Marshal(data)
	return &proto.Result{OK: true, Text: text, Data: b}
}

// Handle runs one tool call.
func (e *Executor) Handle(ctx context.Context, call *proto.Call) *proto.Result {
	defer func() {
		if r := recover(); r != nil {
			e.audit.Write(audit.Entry{Event: "panic", Tool: call.Tool, Error: fmt.Sprint(r)})
		}
	}()
	parse := func(v any) error {
		if len(call.Args) == 0 || string(call.Args) == "null" {
			return nil
		}
		dec := json.NewDecoder(bytes.NewReader(call.Args))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			return fmt.Errorf("invalid arguments: %w", err)
		}
		return nil
	}
	switch call.Tool {
	case "system_info":
		si := collectSystemInfo(ctx)
		b, _ := json.MarshalIndent(si, "", "  ")
		return dataResult(string(b), si)

	case "run_command":
		var a tools.RunCommandArgs
		if err := parse(&a); err != nil {
			return errResult("%v", err)
		}
		return e.runExec(ctx, call, a.Argv, a.As, a.Cwd, a.Stdin, a.TimeoutSec, a.Reason, actionHash(call.Tool, a))

	case "run_shell":
		var a tools.RunShellArgs
		if err := parse(&a); err != nil {
			return errResult("%v", err)
		}
		if strings.TrimSpace(a.Script) == "" {
			return errResult("script is empty")
		}
		act := policy.Action{Kind: "shell", Shell: a.Script, As: a.As}
		d, stop := e.gate(call, actionHash(call.Tool, a), act, a.Reason)
		if stop != nil {
			return stop
		}
		return e.execute(ctx, call, act, d, procSpec{Argv: []string{"/bin/bash", "-c", a.Script}, As: a.As, Cwd: a.Cwd, Timeout: e.timeout(a.TimeoutSec)}, a.Reason)

	case "service":
		var a tools.ServiceArgs
		if err := parse(&a); err != nil {
			return errResult("%v", err)
		}
		if a.Unit == "" || strings.HasPrefix(a.Unit, "-") {
			return errResult("invalid unit")
		}
		argv := []string{"systemctl", a.Action, a.Unit}
		switch a.Action {
		case "status":
			argv = []string{"systemctl", "status", "--no-pager", "-l", a.Unit}
		case "start", "stop", "restart", "reload", "enable", "disable", "is-active", "is-enabled":
		default:
			return errResult("unsupported action %q", a.Action)
		}
		return e.runExec(ctx, call, argv, "", "", "", 120, a.Reason, actionHash(call.Tool, a))

	case "logs":
		var a tools.LogsArgs
		if err := parse(&a); err != nil {
			return errResult("%v", err)
		}
		return e.logs(ctx, call, a)

	case "read_file":
		var a tools.ReadFileArgs
		if err := parse(&a); err != nil {
			return errResult("%v", err)
		}
		return e.readFile(call, a, actionHash(call.Tool, a))

	case "write_file":
		var a tools.WriteFileArgs
		if err := parse(&a); err != nil {
			return errResult("%v", err)
		}
		return e.writeFile(call, a, actionHash(call.Tool, a))

	case "edit_file":
		var a tools.EditFileArgs
		if err := parse(&a); err != nil {
			return errResult("%v", err)
		}
		return e.editFile(call, a, actionHash(call.Tool, a))

	case "list_dir":
		var a tools.ListDirArgs
		if err := parse(&a); err != nil {
			return errResult("%v", err)
		}
		return listDir(a.Path)

	case "start_job":
		var a tools.StartJobArgs
		if err := parse(&a); err != nil {
			return errResult("%v", err)
		}
		return e.startJob(call, a, actionHash(call.Tool, a))

	case "job_status":
		var a tools.JobStatusArgs
		if err := parse(&a); err != nil {
			return errResult("%v", err)
		}
		if a.JobID == "" {
			l := e.jobs.list()
			b, _ := json.MarshalIndent(l, "", "  ")
			return dataResult(string(b), l)
		}
		j := e.jobs.get(a.JobID)
		if j == nil {
			return errResult("no such job %s (jobs are kept for 24h and lost if the agent restarts)", a.JobID)
		}
		v := j.view(a.Offset, e.cfg.MaxOutputBytes)
		text := fmt.Sprintf("job %s: %s", v.JobID, v.Status)
		if v.ExitCode != nil {
			text += fmt.Sprintf(" (exit code %d)", *v.ExitCode)
		}
		text += fmt.Sprintf("\noutput bytes %d-%d:\n%s", v.Offset, v.NextOffset, v.Output)
		if v.Note != "" {
			text += "\n[" + v.Note + "]"
		}
		return dataResult(text, v)

	case "cancel_job":
		var a tools.CancelJobArgs
		if err := parse(&a); err != nil {
			return errResult("%v", err)
		}
		if err := e.jobs.cancel(a.JobID); err != nil {
			return errResult("%v", err)
		}
		e.audit.Write(audit.Entry{Event: "job_cancelled", Caller: call.Caller, Tool: call.Tool, Summary: a.JobID})
		return &proto.Result{OK: true, Text: "cancelled " + a.JobID}

	case "update_server":
		var a tools.UpdateServerArgs
		if err := parse(&a); err != nil {
			return errResult("%v", err)
		}
		return e.updateServer(ctx, call, a)

	case "policy_view":
		s := e.policy.Snapshot()
		b, _ := json.MarshalIndent(s, "", "  ")
		return dataResult(string(b), s)

	case "audit_log":
		var a tools.AuditLogArgs
		if err := parse(&a); err != nil {
			return errResult("%v", err)
		}
		n := a.Lines
		if n <= 0 || n > 1000 {
			n = 50
		}
		lines, err := audit.Tail(e.audit.Path(), n)
		if err != nil {
			return errResult("%v", err)
		}
		var sb strings.Builder
		for _, l := range lines {
			sb.Write(l)
			sb.WriteByte('\n')
		}
		return &proto.Result{OK: true, Text: sb.String()}
	}
	return errResult("unknown tool %q", call.Tool)
}

func (e *Executor) timeout(sec int) time.Duration {
	if sec <= 0 {
		sec = e.cfg.DefaultTimeoutSec
	}
	if sec > e.cfg.MaxTimeoutSec {
		sec = e.cfg.MaxTimeoutSec
	}
	return time.Duration(sec) * time.Second
}

func (e *Executor) runExec(ctx context.Context, call *proto.Call, argv []string, as, cwd, stdin string, timeoutSec int, why, hash string) *proto.Result {
	if len(argv) == 0 {
		return errResult("argv is empty")
	}
	act := policy.Action{Kind: "exec", Argv: argv, As: as}
	d, stop := e.gate(call, hash, act, why)
	if stop != nil {
		return stop
	}
	return e.execute(ctx, call, act, d, procSpec{Argv: argv, As: as, Cwd: cwd, Stdin: stdin, Timeout: e.timeout(timeoutSec)}, why)
}

func (e *Executor) execute(ctx context.Context, call *proto.Call, act policy.Action, d policy.Decision, spec procSpec, why string) *proto.Result {
	r := runProcess(ctx, spec, e.cfg.MaxOutputBytes)
	ec := r.ExitCode
	entry := audit.Entry{Event: "executed", Caller: call.Caller, Tool: call.Tool, Summary: act.Summary(), Tier: d.Tier, RuleID: d.RuleID, ExitCode: &ec, DurationMS: r.DurationMS, Detail: map[string]any{"reason": why, "stdout_bytes": len(r.Stdout), "stderr_bytes": len(r.Stderr), "timed_out": r.TimedOut}}
	if d.Tier == "approved" {
		entry.ApprovalID = d.RuleID
		entry.RuleID = ""
	}
	e.audit.Write(entry)
	var sb strings.Builder
	fmt.Fprintf(&sb, "exit code: %d (%d ms, tier: %s)", r.ExitCode, r.DurationMS, d.Tier)
	if r.TimedOut {
		sb.WriteString(" — TIMED OUT; use start_job for long-running commands")
	}
	if r.Stdout != "" {
		sb.WriteString("\n--- stdout ---\n" + r.Stdout)
	}
	if r.Stderr != "" {
		sb.WriteString("\n--- stderr ---\n" + r.Stderr)
	}
	res := dataResult(sb.String(), map[string]any{"result": r, "tier": d.Tier})
	res.OK = r.ExitCode == 0 && !r.TimedOut
	return res
}

func (e *Executor) logs(ctx context.Context, call *proto.Call, a tools.LogsArgs) *proto.Result {
	lines := a.Lines
	if lines <= 0 {
		lines = 100
	}
	if lines > 2000 {
		lines = 2000
	}
	if a.File != "" {
		path, orig, err := resolvePath(a.File, false)
		if err != nil {
			return errResult("%v", err)
		}
		act := policy.Action{Kind: "read_file", Path: path, OrigPath: orig}
		if _, stop := e.gate(call, actionHash(call.Tool, a), act, "logs"); stop != nil {
			return stop
		}
		text, err := tailFile(path, lines, a.Grep)
		if err != nil {
			return errResult("%v", err)
		}
		return &proto.Result{OK: true, Text: text}
	}
	argv := []string{"journalctl", "--no-pager", "-o", "short-iso", "-n", strconv.Itoa(lines)}
	if a.Unit != "" {
		argv = append(argv, "-u", a.Unit)
	}
	if a.Since != "" {
		argv = append(argv, "--since", a.Since)
	}
	if a.Priority != "" {
		argv = append(argv, "-p", a.Priority)
	}
	if a.Grep != "" {
		argv = append(argv, "-g", a.Grep)
	}
	return e.runExec(ctx, call, argv, "", "", "", 60, "logs", actionHash(call.Tool, a))
}

func tailFile(path string, n int, grep string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, _ := f.Stat()
	const window = 4 << 20
	start := int64(0)
	if st.Size() > window {
		start = st.Size() - window
	}
	b, err := io.ReadAll(io.NewSectionReader(f, start, st.Size()-start))
	if err != nil {
		return "", err
	}
	all := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if grep != "" {
		var keep []string
		for _, l := range all {
			if strings.Contains(l, grep) {
				keep = append(keep, l)
			}
		}
		all = keep
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return cleanOutput(strings.Join(all, "\n")), nil
}

// resolvePath cleans an absolute path and resolves symlinks so policy checks
// apply to the real target. For writes to new files, the parent is resolved.
func resolvePath(p string, forWrite bool) (resolved, orig string, err error) {
	if !filepath.IsAbs(p) {
		return "", "", fmt.Errorf("path must be absolute: %q", p)
	}
	orig = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(orig); err == nil {
		return r, orig, nil
	} else if !forWrite || !errors.Is(err, fs.ErrNotExist) {
		if forWrite {
			return "", "", err
		}
		return "", "", err
	}
	// New file: resolve the nearest existing ancestor.
	dir, rest := filepath.Dir(orig), filepath.Base(orig)
	for {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(r, rest), orig, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return orig, orig, nil
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

func (e *Executor) readFile(call *proto.Call, a tools.ReadFileArgs, hash string) *proto.Result {
	path, orig, err := resolvePath(a.Path, false)
	if err != nil {
		return errResult("%v", err)
	}
	act := policy.Action{Kind: "read_file", Path: path, OrigPath: orig}
	d, stop := e.gate(call, hash, act, a.Reason)
	if stop != nil {
		return stop
	}
	f, err := os.Open(path)
	if err != nil {
		return errResult("%v", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return errResult("%v", err)
	}
	if st.IsDir() {
		return errResult("%s is a directory; use list_dir", path)
	}
	limit := a.Limit
	if limit <= 0 || limit > e.cfg.MaxReadBytes {
		limit = e.cfg.MaxReadBytes
	}
	buf := make([]byte, limit)
	n, err := f.ReadAt(buf, a.Offset)
	if err != nil && err != io.EOF {
		return errResult("%v", err)
	}
	buf = buf[:n]
	if d.Tier != policy.TierRead {
		e.audit.Write(audit.Entry{Event: "file_read", Caller: call.Caller, Tool: call.Tool, Summary: act.Summary(), Tier: d.Tier, RuleID: d.RuleID})
	}
	if !utf8.Valid(buf) && bytes.IndexByte(buf, 0) >= 0 {
		return errResult("%s looks like a binary file (%d bytes)", path, st.Size())
	}
	text := string(buf)
	end := a.Offset + int64(n)
	if end < st.Size() {
		text += fmt.Sprintf("\n\n[showing bytes %d-%d of %d; pass offset=%d to read more]", a.Offset, end, st.Size(), end)
	}
	return dataResult(text, map[string]any{"path": path, "size": st.Size(), "offset": a.Offset, "bytes": n, "mode": st.Mode().String(), "modified": st.ModTime().UTC()})
}

func (e *Executor) writeFile(call *proto.Call, a tools.WriteFileArgs, hash string) *proto.Result {
	path, orig, err := resolvePath(a.Path, true)
	if err != nil {
		return errResult("%v", err)
	}
	act := policy.Action{Kind: "write_file", Path: path, OrigPath: orig}
	d, stop := e.gate(call, hash, act, a.Reason)
	if stop != nil {
		return stop
	}
	mode := os.FileMode(0o644)
	if a.Mode != "" {
		m, err := strconv.ParseUint(a.Mode, 8, 32)
		if err != nil {
			return errResult("invalid mode %q", a.Mode)
		}
		mode = os.FileMode(m)
	}
	if a.CreateDirs {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return errResult("%v", err)
		}
	}
	var backup string
	if a.Append {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, mode)
		if err != nil {
			return errResult("%v", err)
		}
		_, err = f.WriteString(a.Content)
		f.Close()
		if err != nil {
			return errResult("%v", err)
		}
	} else {
		backup, err = atomicWrite(path, []byte(a.Content), mode)
		if err != nil {
			return errResult("%v", err)
		}
	}
	e.audit.Write(audit.Entry{Event: "file_written", Caller: call.Caller, Tool: call.Tool, Summary: act.Summary(), Tier: d.Tier, RuleID: d.RuleID, Detail: map[string]any{"bytes": len(a.Content), "append": a.Append, "backup": backup, "reason": a.Reason}})
	msg := fmt.Sprintf("wrote %d bytes to %s (tier: %s)", len(a.Content), path, d.Tier)
	if backup != "" {
		msg += "; previous version saved to " + backup
	}
	return &proto.Result{OK: true, Text: msg}
}

func (e *Executor) editFile(call *proto.Call, a tools.EditFileArgs, hash string) *proto.Result {
	path, orig, err := resolvePath(a.Path, false)
	if err != nil {
		return errResult("%v", err)
	}
	act := policy.Action{Kind: "write_file", Path: path, OrigPath: orig}
	d, stop := e.gate(call, hash, act, a.Reason)
	if stop != nil {
		return stop
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return errResult("%v", err)
	}
	s := string(b)
	n := strings.Count(s, a.OldText)
	if a.OldText == "" || n == 0 {
		return errResult("old_text not found in %s", path)
	}
	if n > 1 && !a.ReplaceAll {
		return errResult("old_text occurs %d times in %s; make it unique or set replace_all", n, path)
	}
	s = strings.Replace(s, a.OldText, a.NewText, -1)
	st, _ := os.Stat(path)
	backup, err := atomicWrite(path, []byte(s), st.Mode().Perm())
	if err != nil {
		return errResult("%v", err)
	}
	e.audit.Write(audit.Entry{Event: "file_edited", Caller: call.Caller, Tool: call.Tool, Summary: act.Summary(), Tier: d.Tier, RuleID: d.RuleID, Detail: map[string]any{"replacements": n, "backup": backup, "reason": a.Reason}})
	return &proto.Result{OK: true, Text: fmt.Sprintf("replaced %d occurrence(s) in %s (tier: %s); previous version saved to %s", n, path, d.Tier, backup)}
}

// atomicWrite replaces path, keeping owner and mode of an existing file and
// saving the old content to path.csa-bak.
func atomicWrite(path string, data []byte, mode os.FileMode) (backup string, err error) {
	var uid, gid = -1, -1
	if st, err := os.Stat(path); err == nil {
		if st.IsDir() {
			return "", fmt.Errorf("%s is a directory", path)
		}
		mode = st.Mode().Perm()
		if s, ok := statOwner(st); ok {
			uid, gid = s[0], s[1]
		}
		old, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		backup = path + ".csa-bak"
		if err := os.WriteFile(backup, old, mode); err != nil {
			return "", fmt.Errorf("saving backup: %w", err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".csa-tmp-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return "", err
	}
	if uid >= 0 {
		os.Chown(tmp.Name(), uid, gid)
	}
	return backup, os.Rename(tmp.Name(), path)
}

func listDir(p string) *proto.Result {
	if !filepath.IsAbs(p) {
		return errResult("path must be absolute")
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		return errResult("%v", err)
	}
	type ent struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Size     int64  `json:"size"`
		Mode     string `json:"mode"`
		Modified string `json:"modified"`
		Target   string `json:"target,omitempty"`
	}
	var out []ent
	var sb strings.Builder
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	for i, de := range ents {
		if i >= 2000 {
			fmt.Fprintf(&sb, "... %d more entries\n", len(ents)-i)
			break
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		t := "file"
		switch {
		case de.IsDir():
			t = "dir"
		case info.Mode()&os.ModeSymlink != 0:
			t = "symlink"
		case !info.Mode().IsRegular():
			t = "other"
		}
		e := ent{Name: de.Name(), Type: t, Size: info.Size(), Mode: info.Mode().String(), Modified: info.ModTime().UTC().Format(time.RFC3339)}
		if t == "symlink" {
			e.Target, _ = os.Readlink(filepath.Join(p, de.Name()))
		}
		out = append(out, e)
		line := fmt.Sprintf("%s %10d %s %s", e.Mode, e.Size, e.Modified, e.Name)
		if e.Target != "" {
			line += " -> " + e.Target
		}
		sb.WriteString(line + "\n")
	}
	return dataResult(sb.String(), out)
}

func (e *Executor) startJob(call *proto.Call, a tools.StartJobArgs, hash string) *proto.Result {
	var act policy.Action
	var argv []string
	switch {
	case len(a.Argv) > 0 && a.Script == "":
		act = policy.Action{Kind: "exec", Argv: a.Argv, As: a.As}
		argv = a.Argv
	case a.Script != "" && len(a.Argv) == 0:
		act = policy.Action{Kind: "shell", Shell: a.Script, As: a.As}
		argv = []string{"/bin/bash", "-c", a.Script}
	default:
		return errResult("give exactly one of argv or script")
	}
	d, stop := e.gate(call, hash, act, a.Reason)
	if stop != nil {
		return stop
	}
	timeout := time.Duration(a.TimeoutSec) * time.Second
	if a.TimeoutSec <= 0 {
		timeout = time.Hour
	}
	if timeout > 24*time.Hour {
		timeout = 24 * time.Hour
	}
	j, err := e.jobs.start(procSpec{Argv: argv, As: a.As, Cwd: a.Cwd, Timeout: timeout}, act.Summary())
	if err != nil {
		return errResult("%v", err)
	}
	e.audit.Write(audit.Entry{Event: "job_started", Caller: call.Caller, Tool: call.Tool, Summary: act.Summary(), Tier: d.Tier, RuleID: d.RuleID, Detail: map[string]any{"job_id": j.ID, "reason": a.Reason}})
	return dataResult(fmt.Sprintf("started %s (tier: %s). Poll with job_status job_id=%s.", j.ID, d.Tier, j.ID), map[string]string{"job_id": j.ID})
}
