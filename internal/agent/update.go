package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/proto"
	"github.com/cstone1983/Claude-server-agent/internal/tools"
	"github.com/cstone1983/Claude-server-agent/internal/version"
)

const updateScript = "/usr/local/sbin/csa-update"

// updateServer checks for, or starts, an update via the installer's
// csa-update script. The update runs in its own transient systemd unit so it
// survives this agent being restarted by the update itself.
func (e *Executor) updateServer(ctx context.Context, call *proto.Call, a tools.UpdateServerArgs) *proto.Result {
	if _, err := os.Stat(updateScript); err != nil {
		return errResult("this server's installation predates update support: re-run the installer once (sudo ./install.sh agent, or hub on the hub machine) to enable it")
	}
	if a.CheckOnly {
		r := runProcess(ctx, procSpec{Argv: []string{updateScript, "--check"}, Timeout: 60 * time.Second}, 16*1024)
		out := strings.TrimSpace(r.Stdout + "\n" + r.Stderr)
		e.audit.Write(audit.Entry{Event: "update_checked", Caller: call.Caller, Tool: call.Tool, Summary: out})
		res := &proto.Result{OK: r.ExitCode == 0, Text: fmt.Sprintf("installed: %s\n%s", version.Version, out)}
		if r.ExitCode != 0 {
			res.Error = "update check failed"
		}
		return res
	}
	if call.MaxTier == "read" {
		return errResult("this caller is read-only; it can check for updates (check_only) but not install them")
	}
	if !e.policy.RemoteUpdateAllowed() {
		return errResult("remote updates are disabled on this server (disable_remote_update in /etc/csa/policy.yaml); run sudo csa-update there instead")
	}
	argv := []string{"systemd-run", "--unit", fmt.Sprintf("csa-update-manual-%d", time.Now().Unix()), "--collect", "--quiet", updateScript}
	if a.Force {
		argv = append(argv, "--force")
	}
	r := runProcess(ctx, procSpec{Argv: argv, Timeout: 30 * time.Second}, 16*1024)
	ec := r.ExitCode
	e.audit.Write(audit.Entry{Event: "update_started", Caller: call.Caller, Tool: call.Tool, Summary: strings.Join(argv, " "), ExitCode: &ec, Detail: map[string]any{"from_version": version.Version, "force": a.Force}})
	if ec != 0 {
		return errResult("could not start the update: %s", strings.TrimSpace(r.Stderr+r.Stdout))
	}
	return &proto.Result{OK: true, Text: fmt.Sprintf("Update started (installed version %s). If a newer release exists, the agent restarts within about a minute; call list_servers to confirm the new version. Log on the server: journalctl -u '%s'", version.Version, argv[2])}
}
