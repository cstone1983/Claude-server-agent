package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/policy"
	"github.com/cstone1983/Claude-server-agent/internal/proto"
	"github.com/cstone1983/Claude-server-agent/internal/tools"
)

// Interactive sessions: a command running in a pseudo-terminal that Claude
// can type into across several tool calls (installers that ask questions,
// menus, REPLs). Output is kept both as a raw stream (for "what's new") and
// in a terminal emulator (for a rendered screen of full-screen programs).

const (
	maxSessions      = 5
	sessionIdle      = 30 * time.Minute
	sessionBufBytes  = 1 << 20
	sessionMaxReturn = 48 * 1024
)

type session struct {
	ID         string
	Summary    string
	Started    time.Time
	cmd        *exec.Cmd
	ptmx       *os.File
	mu         sync.Mutex
	buf        []byte // raw output not yet returned
	dropped    int64
	term       vt10x.Terminal
	lastActive time.Time
	exited     bool
	exitCode   int
	changed    chan struct{} // closed and replaced whenever output arrives
	done       chan struct{}
	cancel     context.CancelFunc
}

type sessionManager struct {
	mu sync.Mutex
	m  map[string]*session
}

func newSessionManager() *sessionManager {
	sm := &sessionManager{m: map[string]*session{}}
	go sm.reap()
	return sm
}

func (sm *sessionManager) reap() {
	for range time.Tick(time.Minute) {
		sm.mu.Lock()
		for id, s := range sm.m {
			s.mu.Lock()
			idle := time.Since(s.lastActive)
			ex := s.exited
			s.mu.Unlock()
			if idle > sessionIdle || (ex && idle > 10*time.Minute) {
				s.kill()
				delete(sm.m, id)
			}
		}
		sm.mu.Unlock()
	}
}

func (sm *sessionManager) get(id string) *session {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.m[id]
}

func (sm *sessionManager) ids() []string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	var out []string
	for id, s := range sm.m {
		out = append(out, id+" ("+s.Summary+")")
	}
	return out
}

func (sm *sessionManager) remove(id string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if s := sm.m[id]; s != nil {
		s.kill()
		delete(sm.m, id)
	}
}

func (sm *sessionManager) start(spec procSpec, summary string, cols, rows int) (*session, error) {
	sm.mu.Lock()
	n := len(sm.m)
	sm.mu.Unlock()
	if n >= maxSessions {
		return nil, fmt.Errorf("too many open sessions (%d); close one with session_close", n)
	}
	if cols <= 0 || cols > 300 {
		cols = 120
	}
	if rows <= 0 || rows > 100 {
		rows = 40
	}
	ctx, cancel := context.WithTimeout(context.Background(), spec.Timeout)
	cmd, err := buildCmd(ctx, spec)
	if err != nil {
		cancel()
		return nil, err
	}
	// A terminal session needs its own session/controlling TTY, not just a
	// process group, and a terminal type that full-screen programs understand.
	cmd.SysProcAttr.Setpgid = false
	cmd.SysProcAttr.Setsid = true  // new session...
	cmd.SysProcAttr.Setctty = true // ...with the pty (stdin, fd 0) as controlling terminal, so Ctrl-C etc. work
	cmd.SysProcAttr.Ctty = 0
	for i, e := range cmd.Env {
		if strings.HasPrefix(e, "TERM=") {
			cmd.Env[i] = "TERM=xterm"
		}
	}
	cmd.Env = append(cmd.Env, fmt.Sprintf("COLUMNS=%d", cols), fmt.Sprintf("LINES=%d", rows))
	ptmx, err := pty.StartWithAttrs(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}, cmd.SysProcAttr)
	if err != nil {
		cancel()
		return nil, err
	}
	b := make([]byte, 6)
	rand.Read(b)
	s := &session{
		ID: "sess_" + hex.EncodeToString(b), Summary: summary, Started: time.Now(), cmd: cmd, ptmx: ptmx,
		term: vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithWriter(ptmx)), lastActive: time.Now(),
		changed: make(chan struct{}), done: make(chan struct{}), cancel: cancel,
	}
	go s.readLoop()
	sm.mu.Lock()
	sm.m[s.ID] = s
	sm.mu.Unlock()
	return s, nil
}

func (s *session) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.buf = append(s.buf, buf[:n]...)
			if over := len(s.buf) - sessionBufBytes; over > 0 {
				s.buf = append([]byte{}, s.buf[over:]...)
				s.dropped += int64(over)
			}
			s.term.Write(buf[:n])
			close(s.changed)
			s.changed = make(chan struct{})
			s.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	err := s.cmd.Wait()
	s.mu.Lock()
	s.exited = true
	s.exitCode = -1
	if s.cmd.ProcessState != nil {
		s.exitCode = s.cmd.ProcessState.ExitCode()
	}
	_ = err
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
	close(s.done)
	s.cancel()
}

func (s *session) kill() {
	if s.cmd.Process != nil {
		syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	}
	s.ptmx.Close()
	s.cancel()
}

var sessionKeys = map[string]string{
	"enter": "\r", "tab": "\t", "esc": "\x1b", "escape": "\x1b", "space": " ", "backspace": "\x7f",
	"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
	"home": "\x1b[H", "end": "\x1b[F", "pageup": "\x1b[5~", "pagedown": "\x1b[6~", "delete": "\x1b[3~",
	"ctrl-c": "\x03", "ctrl-d": "\x04", "ctrl-z": "\x1a", "ctrl-l": "\x0c", "ctrl-u": "\x15",
	"y": "y", "n": "n", "q": "q",
}

func (s *session) send(input string, enter bool, keys []string) error {
	s.mu.Lock()
	ex := s.exited
	s.lastActive = time.Now()
	s.mu.Unlock()
	if ex {
		return errors.New("the session's program has exited")
	}
	var out strings.Builder
	out.WriteString(input)
	if enter {
		out.WriteString("\r")
	}
	for _, k := range keys {
		seq, ok := sessionKeys[strings.ToLower(strings.TrimSpace(k))]
		if !ok {
			return fmt.Errorf("unknown key %q (known: enter, tab, esc, space, backspace, up, down, left, right, home, end, pageup, pagedown, delete, ctrl-c, ctrl-d, ctrl-z, ctrl-l, ctrl-u)", k)
		}
		out.WriteString(seq)
	}
	_, err := io.WriteString(s.ptmx, out.String())
	return err
}

// settle waits until output has been quiet for `quiet`, the program exits,
// or `max` elapses, then returns the output gathered since the last call.
func (s *session) settle(max, quiet time.Duration) (string, bool, int) {
	deadline := time.Now().Add(max)
	for {
		s.mu.Lock()
		ch, ex := s.changed, s.exited
		s.mu.Unlock()
		if ex {
			break
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		wait := min(quiet, remaining)
		select {
		case <-ch:
			continue // new output: keep waiting for quiet
		case <-time.After(wait):
		}
		s.mu.Lock()
		hasOutput := len(s.buf) > 0
		s.mu.Unlock()
		if hasOutput || time.Until(deadline) <= 0 {
			break
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw := s.buf
	s.buf = nil
	s.lastActive = time.Now()
	text := cleanTerminal(raw)
	if len(text) > sessionMaxReturn {
		text = "[... earlier output omitted; use screen=true to see the current screen ...]\n" + text[len(text)-sessionMaxReturn:]
	}
	return text, s.exited, s.exitCode
}

func (s *session) screen() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	lines := strings.Split(s.term.String(), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// cleanTerminal turns raw terminal output into readable text: escape
// sequences removed, carriage-return overwrites (progress bars) collapsed.
func cleanTerminal(raw []byte) string {
	s := cleanOutput(string(raw))
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		if j := strings.LastIndex(strings.TrimRight(line, "\r"), "\r"); j >= 0 {
			line = line[j+1:]
		}
		b.WriteString(strings.TrimRight(line, "\r"))
	}
	return b.String()
}

func (e *Executor) sessionStart(call *proto.Call, a tools.SessionStartArgs, hash string) *proto.Result {
	if call.MaxTier == "read" {
		return errResult("this caller is read-only; interactive sessions are not available")
	}
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
		timeout = 2 * time.Hour
	}
	timeout = min(timeout, 24*time.Hour)
	s, err := e.sess.start(procSpec{Argv: argv, As: a.As, Cwd: a.Cwd, Timeout: timeout}, act.Summary(), a.Cols, a.Rows)
	if err != nil {
		return errResult("%v", err)
	}
	entry := audit.Entry{Event: "session_started", Caller: call.Caller, Tool: call.Tool, Summary: act.Summary(), Tier: d.Tier, RuleID: d.RuleID, Detail: map[string]any{"session_id": s.ID, "reason": a.Reason}}
	if d.Tier == "approved" {
		entry.ApprovalID, entry.RuleID = d.RuleID, ""
	}
	e.audit.Write(entry)
	out, exited, code := s.settle(waitDur(a.WaitSec, 10, 45), 1500*time.Millisecond)
	return sessionResult(s, out, exited, code, false, fmt.Sprintf("session %s started (tier: %s)", s.ID, d.Tier))
}

func (e *Executor) sessionSend(call *proto.Call, a tools.SessionSendArgs) *proto.Result {
	if call.MaxTier == "read" {
		return errResult("this caller is read-only")
	}
	s := e.sess.get(a.SessionID)
	if s == nil {
		open := e.sess.ids()
		if len(open) == 0 {
			return errResult("no such session %s (no sessions are open; they end after 30 min idle or if the agent restarts)", a.SessionID)
		}
		return errResult("no such session %s; open sessions: %s", a.SessionID, strings.Join(open, ", "))
	}
	enter := a.Input != ""
	if a.Enter != nil {
		enter = *a.Enter
	}
	if a.Input != "" || enter || len(a.Keys) > 0 {
		logged := a.Input
		if a.Secret {
			logged = "[secret]"
		}
		e.audit.Write(audit.Entry{Event: "session_input", Caller: call.Caller, Tool: call.Tool, Summary: s.ID + ": " + logged, Detail: map[string]any{"enter": enter, "keys": a.Keys}})
		if err := s.send(a.Input, enter, a.Keys); err != nil {
			out, exited, code := s.settle(0, 0)
			r := sessionResult(s, out, exited, code, a.Screen, "")
			r.Error = err.Error()
			return r
		}
	}
	out, exited, code := s.settle(waitDur(a.WaitSec, 10, 45), 1500*time.Millisecond)
	return sessionResult(s, out, exited, code, a.Screen, "")
}

func waitDur(sec, def, max int) time.Duration {
	if sec <= 0 {
		sec = def
	}
	return time.Duration(min(sec, max)) * time.Second
}

func sessionResult(s *session, out string, exited bool, code int, withScreen bool, header string) *proto.Result {
	var b strings.Builder
	if header != "" {
		b.WriteString(header + "\n")
	}
	if strings.TrimSpace(out) != "" {
		b.WriteString("--- new output ---\n" + out + "\n")
	} else {
		b.WriteString("(no new output yet; call session_send without input to wait longer)\n")
	}
	if withScreen {
		b.WriteString("--- screen ---\n" + s.screen() + "\n")
	}
	if exited {
		fmt.Fprintf(&b, "--- program exited (code %d); close the session with session_close ---\n", code)
	} else {
		fmt.Fprintf(&b, "[session %s is waiting for input]\n", s.ID)
	}
	return &proto.Result{OK: !exited || code == 0, Text: b.String()}
}
