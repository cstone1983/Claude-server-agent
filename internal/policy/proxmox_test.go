package policy

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestProxmoxPolicy checks the careful-mode policy that install.sh writes on
// Proxmox VE hosts: reads and backup work run freely, other changes need
// approval unless a hub rule (scoped to this host) allows them.
func TestProxmoxPolicy(t *testing.T) {
	raw, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	const open = "cat > /etc/csa/policy.yaml <<'PVE'\n"
	i := strings.Index(s, open)
	if i < 0 {
		t.Fatal("Proxmox policy heredoc not found in install.sh")
	}
	body := s[i+len(open):]
	body = body[:strings.Index(body, "\nPVE\n")]

	var f File
	if err := yaml.Unmarshal([]byte(body), &f); err != nil {
		t.Fatalf("Proxmox policy is not valid YAML: %v", err)
	}
	hub := []Rule{{ID: "hub-proxmox", Argv: []string{"qm", "snapshot", "**"}}}
	p := New(f, hub)

	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"qm", "list"}, TierRead},
		{[]string{"qm", "config", "102"}, TierRead},
		{[]string{"pvesm", "status"}, TierRead},
		{[]string{"pvesh", "get", "/cluster/backup"}, TierRead},
		{[]string{"df", "-h"}, TierRead},
		{[]string{"qm", "destroy", "100"}, TierElevated},
		{[]string{"qm", "stop", "102"}, TierElevated},
		{[]string{"vzdump", "102", "--storage", "nas"}, TierAllowed},
		{[]string{"pvesh", "create", "/cluster/backup", "--schedule", "02:00"}, TierAllowed},
		{[]string{"pvesh", "set", "/cluster/backup/job-1", "--enabled", "0"}, TierAllowed},
		{[]string{"pvesh", "delete", "/cluster/backup/job-1"}, TierElevated},
		{[]string{"pvesm", "prune-backups", "local"}, TierElevated},
		{[]string{"qm", "snapshot", "102", "pre"}, TierAllowed}, // hub rule for this host applies
		{[]string{"apt-get", "upgrade", "-y"}, TierElevated},    // built-in allow is off
		{[]string{"systemctl", "restart", "x"}, TierElevated},
		{[]string{"reboot"}, TierElevated},
		{[]string{"pve-safe-reboot"}, TierElevated},
		{[]string{"pve-safe-reboot", "--dry-run"}, TierRead},
		{[]string{"shutdown", "-r", "+1", "kernel"}, TierElevated},
		{[]string{"shutdown", "-c"}, TierElevated},
		{[]string{"shutdown", "-h", "now"}, TierDenied},
		{[]string{"shutdown", "now"}, TierDenied},
		{[]string{"poweroff"}, TierDenied},
		{[]string{"systemctl", "poweroff"}, TierDenied},
	}
	for _, c := range cases {
		if got := p.Classify(Action{Kind: "exec", Argv: c.argv}).Tier; got != c.want {
			t.Errorf("%v: got %s, want %s", c.argv, got, c.want)
		}
	}
	// Power actions need their own OK even if an "always allow" rule exists.
	p2 := New(f, []Rule{{ID: "hub-reboot", Argv: []string{"shutdown", "**"}}, {ID: "hub-reboot2", Argv: []string{"reboot"}}})
	if d := p2.Classify(Action{Kind: "exec", Argv: []string{"shutdown", "-r", "+1"}}); d.Tier != TierElevated || !d.AskAlways {
		t.Errorf("shutdown -r with an allow rule: got %+v, want elevated + ask_always", d)
	}
	if d := New(f, []Rule{{ID: "x", Argv: []string{"pve-safe-reboot"}}}).Classify(Action{Kind: "exec", Argv: []string{"pve-safe-reboot"}}); !d.AskAlways {
		t.Errorf("pve-safe-reboot with an allow rule: got %+v, want ask_always", d)
	}
	if d := p2.Classify(Action{Kind: "exec", Argv: []string{"reboot"}}); d.Tier != TierElevated || !d.AskAlways {
		t.Errorf("reboot with an allow rule: got %+v, want elevated + ask_always", d)
	}
	if d := p2.Classify(Action{Kind: "exec", Argv: []string{"shutdown", "-h", "now"}}); d.Tier != TierDenied {
		t.Errorf("shutdown -h with an allow rule: got %s, want denied", d.Tier)
	}
	if d := p.Classify(Action{Kind: "shell", Shell: "apt-get -y full-upgrade && reboot"}); d.Tier != TierDenied {
		t.Errorf("script with reboot: got %s, want denied", d.Tier)
	}
	if got := p.Classify(Action{Kind: "read_file", Path: "/etc/pve/priv/authkey.key"}).Tier; got == TierRead {
		t.Errorf("reading /etc/pve/priv should need approval, got %s", got)
	}
}
