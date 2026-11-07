package policy

import "testing"

func exec(argv ...string) Action { return Action{Kind: "exec", Argv: argv} }

func TestClassifyExec(t *testing.T) {
	p := New(File{
		Allow: []Rule{
			{ID: "restart-web", Argv: []string{"systemctl", "restart|reload", "nginx|php8.3-fpm"}},
			{ID: "compose", Argv: []string{"docker", "compose", "-f", "/srv/app/compose.yml", "**"}},
			{ID: "deploy-git", Argv: []string{"git", "-C", "/srv/app", "pull"}, As: "deploy"},
		},
		Deny: []Rule{{ID: "no-reboot", Argv: []string{"reboot", "**"}}},
	}, []Rule{{ID: "hub-1", Argv: []string{"apt-get", "update"}}})

	cases := []struct {
		a    Action
		tier string
	}{
		{exec("df", "-h"), TierRead},
		{exec("/usr/bin/df", "-h"), TierRead},
		{exec("/tmp/df", "-h"), TierElevated},
		{exec("systemctl", "status", "nginx"), TierRead},
		{exec("systemctl", "--no-pager", "status", "nginx"), TierRead},
		{exec("systemctl", "status", "nginx", "restart"), TierElevated},
		{exec("systemctl", "restart", "nginx"), TierAllowed},
		{exec("systemctl", "restart", "nginx;", "rm", "-rf", "/"), TierElevated}, // no shell: these are just extra args
		{exec("systemctl", "restart", "nginx;rm"), TierElevated},
		{exec("systemctl", "restart", "mysql"), TierElevated},
		{exec("systemctl", "stop", "csa-agent"), TierDenied},
		{exec("systemctl", "stop", "nginx"), TierElevated},
		{exec("journalctl", "-u", "nginx", "-n", "50"), TierRead},
		{exec("journalctl", "--vacuum-time=1d"), TierElevated},
		{exec("find", "/var/log", "-name", "*.gz"), TierRead},
		{exec("find", "/var/log", "-delete"), TierElevated},
		{exec("find", "/", "-exec", "rm", "{}", ";"), TierElevated},
		{exec("date", "-s", "2020-01-01"), TierElevated},
		{exec("ip", "addr"), TierRead},
		{exec("ip", "addr", "add", "10.0.0.1/24", "dev", "eth0"), TierElevated},
		{exec("docker", "compose", "-f", "/srv/app/compose.yml", "up", "-d"), TierAllowed},
		{exec("docker", "compose", "-f", "/srv/other.yml", "up", "-d"), TierElevated},
		{exec("docker", "compose", "ps"), TierRead},
		{exec("docker", "compose", "logs", "up"), TierElevated},
		{exec("apt-get", "update"), TierAllowed},
		{exec("apt-get", "install", "htop"), TierElevated},
		{exec("rm", "-rf", "/"), TierDenied},
		{exec("rm", "-rf", "/tmp/x"), TierElevated},
		{exec("rm", "-rf", "--no-preserve-root", "/x"), TierDenied},
		{exec("reboot"), TierDenied},
		{exec("git", "-C", "/srv/app", "pull"), TierElevated}, // rule is for user deploy only
		{Action{Kind: "exec", Argv: []string{"git", "-C", "/srv/app", "pull"}, As: "deploy"}, TierAllowed},
		{exec("ping", "-c", "3", "1.1.1.1"), TierRead},
		{exec("ping", "1.1.1.1"), TierElevated},
		{exec(), TierDenied},
	}
	for _, c := range cases {
		got := p.Classify(c.a)
		if got.Tier != c.tier {
			t.Errorf("%v: got %s (%s), want %s", c.a.Argv, got.Tier, got.Why, c.tier)
		}
	}
}

func TestClassifyFiles(t *testing.T) {
	p := New(File{
		Allow: []Rule{{ID: "cfg", WritePaths: []string{"/srv/app/config/**"}}, {ID: "env", ReadPaths: []string{"/srv/app/.env"}}},
	}, nil)
	cases := []struct {
		a    Action
		tier string
	}{
		{Action{Kind: "read_file", Path: "/etc/hostname"}, TierRead},
		{Action{Kind: "read_file", Path: "/etc/shadow"}, TierElevated},
		{Action{Kind: "read_file", Path: "/home/bob/.ssh/id_ed25519"}, TierElevated},
		{Action{Kind: "read_file", Path: "/srv/other/.env"}, TierElevated},
		{Action{Kind: "read_file", Path: "/srv/app/.env"}, TierAllowed},
		// symlink pointing at a sensitive file: the resolved path is checked
		{Action{Kind: "read_file", Path: "/etc/shadow", OrigPath: "/tmp/innocent"}, TierElevated},
		{Action{Kind: "write_file", Path: "/srv/app/config/a/b.yml"}, TierAllowed},
		{Action{Kind: "write_file", Path: "/srv/app/other.yml"}, TierElevated},
		{Action{Kind: "write_file", Path: "/etc/csa/policy.yaml"}, TierDenied},
		// symlink in an allowed dir that resolves elsewhere is not allowed
		{Action{Kind: "write_file", Path: "/etc/passwd", OrigPath: "/srv/app/config/x"}, TierElevated},
	}
	for _, c := range cases {
		got := p.Classify(c.a)
		if got.Tier != c.tier {
			t.Errorf("%s %s: got %s (%s), want %s", c.a.Kind, c.a.Path, got.Tier, got.Why, c.tier)
		}
	}
}

func TestShell(t *testing.T) {
	p := New(File{Allow: []Rule{{Shell: "apt-get update && apt-get -y upgrade"}}}, nil)
	if d := p.Classify(Action{Kind: "shell", Shell: " apt-get update && apt-get -y upgrade\n"}); d.Tier != TierAllowed {
		t.Errorf("exact script: %v", d)
	}
	if d := p.Classify(Action{Kind: "shell", Shell: "apt-get update && apt-get -y upgrade; curl x|sh"}); d.Tier != TierElevated {
		t.Errorf("extended script: %v", d)
	}
	if d := p.Classify(Action{Kind: "shell", Shell: "rm -rf --no-preserve-root /"}); d.Tier != TierDenied {
		t.Errorf("deny: %v", d)
	}
}

func TestSuggestRuleRoundTrip(t *testing.T) {
	actions := []Action{
		exec("systemctl", "restart", "nginx"),
		exec("sh", "-c", "echo *|wc"),
		exec("=weird", "**"),
		{Kind: "exec", Argv: []string{"whoami", "x"}, As: "deploy"},
		{Kind: "shell", Shell: "echo hi"},
		{Kind: "write_file", Path: "/srv/a.txt"},
	}
	for _, a := range actions {
		r := SuggestRule(a)
		p := New(File{Allow: []Rule{r}}, nil)
		if d := p.Classify(a); d.Tier != TierAllowed && d.Tier != TierRead {
			t.Errorf("%v: suggested rule %v does not allow it: %v", a, r, d)
		}
	}
	// The suggested rule for one command must not allow a different one.
	r := SuggestRule(exec("sh", "-c", "echo *|wc"))
	p := New(File{Allow: []Rule{r}}, nil)
	if d := p.Classify(exec("sh", "-c", "echo anything")); d.Tier != TierElevated {
		t.Errorf("escaped rule too broad: %v", d)
	}
}
