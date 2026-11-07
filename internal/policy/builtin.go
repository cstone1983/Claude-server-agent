package policy

import "strings"

// builtinRead are commands that only inspect the system. They run freely as
// any user. Flags that would make them modify state are excluded via NotArgs.
var builtinRead = []Rule{
	{ID: "builtin:uptime", Argv: []string{"uptime", "**"}},
	{ID: "builtin:df", Argv: []string{"df", "**"}},
	{ID: "builtin:du", Argv: []string{"du", "**"}},
	{ID: "builtin:free", Argv: []string{"free", "**"}},
	{ID: "builtin:uname", Argv: []string{"uname", "**"}},
	{ID: "builtin:hostname", Argv: []string{"hostname"}},
	{ID: "builtin:hostnamectl", Argv: []string{"hostnamectl", "status|--static"}},
	{ID: "builtin:hostnamectl0", Argv: []string{"hostnamectl"}},
	{ID: "builtin:timedatectl", Argv: []string{"timedatectl", "status|show|timesync-status"}},
	{ID: "builtin:timedatectl0", Argv: []string{"timedatectl"}},
	{ID: "builtin:whoami", Argv: []string{"whoami"}},
	{ID: "builtin:id", Argv: []string{"id", "**"}},
	{ID: "builtin:w", Argv: []string{"w", "**"}},
	{ID: "builtin:who", Argv: []string{"who", "**"}},
	{ID: "builtin:last", Argv: []string{"last", "**"}},
	{ID: "builtin:date", Argv: []string{"date", "**"}, NotArgs: []string{"-s*", "--set*"}},
	{ID: "builtin:nproc", Argv: []string{"nproc", "**"}},
	{ID: "builtin:lscpu", Argv: []string{"lscpu", "**"}},
	{ID: "builtin:lsblk", Argv: []string{"lsblk", "**"}},
	{ID: "builtin:lsusb", Argv: []string{"lsusb", "**"}},
	{ID: "builtin:lspci", Argv: []string{"lspci", "**"}},
	{ID: "builtin:lsof", Argv: []string{"lsof", "**"}},
	{ID: "builtin:lsb_release", Argv: []string{"lsb_release", "**"}},
	{ID: "builtin:sensors", Argv: []string{"sensors", "**"}},
	{ID: "builtin:vmstat", Argv: []string{"vmstat", "**"}},
	{ID: "builtin:iostat", Argv: []string{"iostat", "**"}},
	{ID: "builtin:ps", Argv: []string{"ps", "**"}},
	{ID: "builtin:pgrep", Argv: []string{"pgrep", "**"}},
	{ID: "builtin:top", Argv: []string{"top", "-b", "**"}},
	{ID: "builtin:ls", Argv: []string{"ls", "**"}},
	{ID: "builtin:stat", Argv: []string{"stat", "**"}},
	{ID: "builtin:file", Argv: []string{"file", "**"}},
	{ID: "builtin:which", Argv: []string{"which", "**"}},
	{ID: "builtin:find", Argv: []string{"find", "**"},
		NotArgs: []string{"-delete", "-exec*", "-ok*", "-fprint*", "-fls", "-execdir"}},
	{ID: "builtin:ip", Argv: []string{"ip", "-br|-brief|-c|-color|-4|-6|-s|-d|-j", "addr|a|address|link|l|route|r|neigh|n", "**"},
		NotArgs: []string{"add", "del", "delete", "change", "replace", "set", "flush", "append"}},
	{ID: "builtin:ip2", Argv: []string{"ip", "addr|a|address|link|l|route|r|neigh|n", "**"},
		NotArgs: []string{"add", "del", "delete", "change", "replace", "set", "flush", "append"}},
	{ID: "builtin:ss", Argv: []string{"ss", "**"}, NotArgs: []string{"-K", "--kill"}},
	{ID: "builtin:netstat", Argv: []string{"netstat", "**"}},
	{ID: "builtin:ping", Argv: []string{"ping", "-c", "?|??", "**"}},
	{ID: "builtin:dig", Argv: []string{"dig", "**"}},
	{ID: "builtin:host", Argv: []string{"host", "**"}},
	{ID: "builtin:resolvectl", Argv: []string{"resolvectl", "status|query|dns|domain|statistics", "**"}},
	{ID: "builtin:systemctl", Argv: []string{"systemctl", "**"},
		AnyArg:  []string{"status", "is-active", "is-enabled", "is-failed", "list-units", "list-unit-files", "list-timers", "list-sockets", "list-dependencies", "show", "cat", "--failed"},
		NotArgs: []string{"start", "stop", "restart", "reload", "try-restart", "reload-or-restart", "enable", "disable", "mask", "unmask", "kill", "edit", "set-property", "daemon-reload", "reboot", "poweroff", "halt", "isolate", "reset-failed", "preset", "link", "revert", "set-default", "suspend", "hibernate"}},
	{ID: "builtin:systemctl0", Argv: []string{"systemctl"}},
	{ID: "builtin:systemd-analyze", Argv: []string{"systemd-analyze", "blame|critical-chain|time|verify", "**"}},
	{ID: "builtin:journalctl", Argv: []string{"journalctl", "**"},
		NotArgs: []string{"--vacuum*", "--rotate", "--flush", "--relinquish-var", "--sync", "--setup-keys", "--update-catalog"}},
	{ID: "builtin:dmesg", Argv: []string{"dmesg", "**"}, NotArgs: []string{"-c", "-C", "--clear", "--read-clear", "-n", "--console-level", "-D", "-E", "--console-off", "--console-on"}},
	{ID: "builtin:apt-list", Argv: []string{"apt", "list|show|search|policy", "**"}},
	{ID: "builtin:apt-cache", Argv: []string{"apt-cache", "**"}},
	{ID: "builtin:dpkg", Argv: []string{"dpkg", "-l|-L|-s|-S|--list|--listfiles|--status|--search|--get-selections|--print-architecture", "**"}},
	{ID: "builtin:dpkg-query", Argv: []string{"dpkg-query", "**"}},
	{ID: "builtin:snap", Argv: []string{"snap", "list|info|services|version|changes", "**"}},
	{ID: "builtin:ufw", Argv: []string{"ufw", "status", "**"}},
	{ID: "builtin:docker", Argv: []string{"docker", "ps|images|version|info|inspect|logs|top|port|diff|history", "**"}},
	{ID: "builtin:docker-stats", Argv: []string{"docker", "stats", "--no-stream", "**"}},
	{ID: "builtin:docker-ls", Argv: []string{"docker", "container|image|volume|network", "ls|list|inspect", "**"}},
	{ID: "builtin:docker-compose", Argv: []string{"docker", "compose", "**"},
		AnyArg:  []string{"ps", "ls", "logs", "config", "images", "top", "version"},
		NotArgs: []string{"up", "down", "start", "stop", "restart", "rm", "kill", "pull", "build", "create", "exec", "run", "pause", "unpause", "push", "cp", "scale", "watch"}},
	{ID: "builtin:needrestart", Argv: []string{"needrestart", "-b|-r|-l", "**"}, NotArgs: []string{"a"}},
	{ID: "builtin:smartctl", Argv: []string{"smartctl", "-a|-H|-i|-x|--all|--health|--info|--xall", "**"}},
	{ID: "builtin:zpool", Argv: []string{"zpool", "status|list|iostat", "**"}},
	{ID: "builtin:zfs", Argv: []string{"zfs", "list|get", "**"}},
	{ID: "builtin:lvm", Argv: []string{"lvs|vgs|pvs", "**"}},
	{ID: "builtin:smartctl-scan", Argv: []string{"smartctl", "--scan|--scan-open"}},
	{ID: "builtin:mount", Argv: []string{"mount"}},
	{ID: "builtin:findmnt", Argv: []string{"findmnt", "**"}},
	{ID: "builtin:crontab", Argv: []string{"crontab", "-l", "**"}},
	{ID: "builtin:getent", Argv: []string{"getent", "passwd|group|hosts", "**"}},
}

// builtinAllow are harmless maintenance commands that change state but run
// without approval (as root). Turn off with disable_builtin_allow: true.
var builtinAllow = []Rule{}

// aptMaintenanceID identifies the built-in apt rule in decisions and policy_view.
const aptMaintenanceID = "builtin-allow:apt-update-upgrade"

const aptMaintenanceDesc = "apt / apt-get update or upgrade, with any flags (-y, -q, --with-new-pkgs, -o Dpkg::Options::=..., ...), " +
	"but no package names and no options that can run programs or change apt's paths (*Invoke*, Pre-Install-Pkgs, Dir::, ::Path, Binary::, -c/--config-file, --allow-*)"

// BuiltinSummary describes the built-in tiers for display (e.g. the hub's
// rules page).
func BuiltinSummary() (read []string, allow []string) {
	seen := map[string]bool{}
	for _, r := range builtinRead {
		if len(r.Argv) > 0 && !seen[r.Argv[0]] {
			seen[r.Argv[0]] = true
			read = append(read, r.Argv[0])
		}
	}
	return read, []string{aptMaintenanceDesc}
}

// aptMaintenance reports whether argv is a routine package-list refresh or
// upgrade of installed packages: "apt[-get] [flags] update|upgrade [flags]".
// Flags are allowed freely except those that can execute arbitrary programs
// (apt hooks), point apt at other binaries/paths, load another config file,
// or weaken package verification. Package names are not allowed.
func aptMaintenance(argv []string) bool {
	if len(argv) < 2 || (argv[0] != "apt" && argv[0] != "apt-get") {
		return false
	}
	sub := ""
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "-o" || a == "--option":
			if i+1 >= len(argv) || !safeAptOption(argv[i+1]) {
				return false
			}
			i++
		case strings.HasPrefix(a, "--option="):
			if !safeAptOption(strings.TrimPrefix(a, "--option=")) {
				return false
			}
		case strings.HasPrefix(a, "-o") && !strings.HasPrefix(a, "--"):
			if !safeAptOption(a[2:]) {
				return false
			}
		case a == "-c" || strings.HasPrefix(a, "-c") && !strings.HasPrefix(a, "--"),
			strings.HasPrefix(a, "--config-file"),
			strings.HasPrefix(strings.ToLower(a), "--allow-"):
			return false
		case a == "-t" || a == "--target-release":
			i++ // takes a release name, harmless
		case strings.HasPrefix(a, "-"):
			// other flags (-y, -q, -s, --with-new-pkgs, --no-install-recommends, ...)
		default:
			if sub != "" || (a != "update" && a != "upgrade") {
				return false // second subcommand or a package name
			}
			sub = a
		}
	}
	return sub != ""
}

// safeAptOption rejects apt configuration overrides that can run programs or
// redirect apt/dpkg to other binaries or paths. apt treats option names
// case-insensitively, so the check does too.
func safeAptOption(opt string) bool {
	o := strings.ToLower(opt)
	for _, bad := range []string{"invoke", "pre-install-pkgs", "dir::", "dir=", "::path", "binary::", "dpkg::tools", "allowunauthenticated", "allow-unauthenticated", "allowinsecure", "allow-insecure", "allowdowngrade", "trusted"} {
		if strings.Contains(o, bad) {
			return false
		}
	}
	return true
}

// builtinDeny can never run, even with approval. Remove the agent's ability
// to lock itself out or wipe the root filesystem.
var builtinDeny = []Rule{
	{ID: "builtin-deny:no-preserve-root", AnyArg: []string{"--no-preserve-root"}},
	{ID: "builtin-deny:rm-root", Argv: []string{"rm", "**"}, AnyArg: []string{"=/", "=/*"}},
	{ID: "builtin-deny:stop-agent", Argv: []string{"systemctl", "stop|disable|mask|kill", "csa-agent|csa-agent.service|csa-hub|csa-hub.service|cloudflared|cloudflared.service"}},
	{ID: "builtin-deny:stop-agent-now", Argv: []string{"systemctl", "disable|mask", "--now", "csa-agent|csa-agent.service|csa-hub|csa-hub.service|cloudflared|cloudflared.service"}},
	{ID: "builtin-deny:policy-write", WritePaths: []string{"/etc/csa/**", "/var/lib/csa/**", "/etc/csa-hub/**", "/var/lib/csa-hub/**"}},
	// The hub's config holds the admin password hash and authenticator secret;
	// reading it would let a caller approve its own requests.
	{ID: "builtin-deny:hub-secrets", ReadPaths: []string{"/etc/csa-hub/**", "/var/lib/csa-hub/**", "/etc/csa/agent.key"}},
	{ID: "builtin-deny:shell-no-preserve-root", Shell: "--no-preserve-root"},
}

// builtinSensitive are read paths that need approval.
var builtinSensitive = []string{
	"/etc/shadow", "/etc/shadow-", "/etc/gshadow", "/etc/gshadow-",
	"/etc/sudoers", "/etc/sudoers.d/**",
	"/etc/ssh/ssh_host_*_key",
	"/root/.ssh/**", "/home/*/.ssh/**",
	"/etc/csa/agent.key", "/var/lib/csa/**",
	"/etc/csa-hub/**", "/var/lib/csa-hub/**",
	"/proc/*/environ", "/proc/kcore",
	"**/.env", "**/.env.*", "**/*.pem", "**/*.key", "**/id_rsa", "**/id_ed25519",
	"**/.aws/credentials", "**/.docker/config.json", "**/.netrc", "**/.pgpass",
	"/etc/letsencrypt/**",
}
