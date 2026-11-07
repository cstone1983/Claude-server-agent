package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type procSpec struct {
	Argv    []string
	As      string
	Cwd     string
	Stdin   string
	Timeout time.Duration
}

type procResult struct {
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

const basePath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/snap/bin"

// buildCmd prepares a command running as the requested user in its own
// process group, with a clean, non-interactive environment.
func buildCmd(ctx context.Context, s procSpec) (*exec.Cmd, error) {
	if len(s.Argv) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	username := s.As
	var u *user.User
	var err error
	if username == "" && os.Geteuid() != 0 {
		// Not running as root (development/testing): run as ourselves.
		u, err = user.Current()
	} else {
		if username == "" {
			username = "root"
		}
		u, err = user.Lookup(username)
	}
	if err != nil {
		return nil, fmt.Errorf("unknown user %q", username)
	}
	path, err := lookPath(s.Argv[0])
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, s.Argv[1:]...)
	cmd.Env = []string{
		"PATH=" + basePath, "HOME=" + u.HomeDir, "USER=" + u.Username, "LOGNAME=" + u.Username,
		"LANG=C.UTF-8", "TERM=dumb", "PAGER=cat", "SYSTEMD_PAGER=", "SYSTEMD_COLORS=0",
		"DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=l", "NO_COLOR=1",
	}
	cmd.Dir = s.Cwd
	if cmd.Dir == "" {
		cmd.Dir = u.HomeDir
		if st, err := os.Stat(cmd.Dir); err != nil || !st.IsDir() {
			cmd.Dir = "/"
		}
	}
	attr := &syscall.SysProcAttr{Setpgid: true}
	if os.Geteuid() == 0 && u.Uid != "0" {
		uid, _ := strconv.ParseUint(u.Uid, 10, 32)
		gid, _ := strconv.ParseUint(u.Gid, 10, 32)
		var groups []uint32
		if gids, err := u.GroupIds(); err == nil {
			for _, g := range gids {
				if n, err := strconv.ParseUint(g, 10, 32); err == nil {
					groups = append(groups, uint32(n))
				}
			}
		}
		attr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}
	} else if os.Geteuid() != 0 && u.Uid != strconv.Itoa(os.Geteuid()) {
		return nil, fmt.Errorf("agent is not running as root; cannot run as %q", username)
	}
	cmd.SysProcAttr = attr
	cmd.Cancel = func() error {
		// Kill the whole process group.
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
	if s.Stdin != "" {
		cmd.Stdin = strings.NewReader(s.Stdin)
	}
	return cmd, nil
}

func lookPath(name string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	for _, dir := range strings.Split(basePath, ":") {
		p := dir + "/" + name
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("command not found: %s", name)
}

func runProcess(ctx context.Context, s procSpec, maxOut int) procResult {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	start := time.Now()
	cmd, err := buildCmd(ctx, s)
	if err != nil {
		return procResult{ExitCode: -1, Stderr: err.Error()}
	}
	stdout := newCappedBuffer(maxOut)
	stderr := newCappedBuffer(maxOut)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	res := procResult{
		Stdout:     cleanOutput(stdout.String()),
		Stderr:     cleanOutput(stderr.String()),
		DurationMS: time.Since(start).Milliseconds(),
		Truncated:  stdout.truncated() || stderr.truncated(),
	}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	} else {
		res.ExitCode = -1
	}
	if err != nil && cmd.ProcessState == nil {
		res.Stderr = strings.TrimSpace(res.Stderr + "\n" + err.Error())
	}
	return res
}

// cappedBuffer keeps the first and last half of the output when it exceeds max.
type cappedBuffer struct {
	max   int
	head  bytes.Buffer
	tail  []byte
	total int64
}

func newCappedBuffer(max int) *cappedBuffer { return &cappedBuffer{max: max} }

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.total += int64(len(p))
	half := c.max / 2
	n := len(p)
	if c.head.Len() < half {
		take := min(half-c.head.Len(), len(p))
		c.head.Write(p[:take])
		p = p[take:]
	}
	if len(p) > 0 {
		c.tail = append(c.tail, p...)
		if len(c.tail) > half {
			c.tail = append([]byte{}, c.tail[len(c.tail)-half:]...)
		}
	}
	return n, nil
}

func (c *cappedBuffer) truncated() bool { return c.total > int64(c.head.Len()+len(c.tail)) }

func (c *cappedBuffer) String() string {
	if !c.truncated() {
		return c.head.String() + string(c.tail)
	}
	omitted := c.total - int64(c.head.Len()+len(c.tail))
	return c.head.String() + fmt.Sprintf("\n\n[... %d bytes omitted; use start_job for full output ...]\n\n", omitted) + string(c.tail)
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`)

func cleanOutput(s string) string {
	s = ansiRE.ReplaceAllString(s, "")
	return strings.ToValidUTF8(s, "�")
}
