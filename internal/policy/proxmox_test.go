package policy

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestProxmoxPolicy checks the protective policy that install.sh writes on
// Proxmox VE hosts: reads run freely, every change needs approval, and hub
// "always allow" rules never apply.
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
	hub := []Rule{{ID: "hub-all", Argv: []string{"systemctl", "restart", "**"}}}
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
		{[]string{"vzdump", "102"}, TierElevated},
		{[]string{"pvesh", "create", "/cluster/backup"}, TierElevated},
		{[]string{"apt-get", "upgrade", "-y"}, TierElevated},   // built-in allow is off
		{[]string{"systemctl", "restart", "x"}, TierElevated}, // hub rule ignored
	}
	for _, c := range cases {
		if got := p.Classify(Action{Kind: "exec", Argv: c.argv}).Tier; got != c.want {
			t.Errorf("%v: got %s, want %s", c.argv, got, c.want)
		}
	}
	if got := p.Classify(Action{Kind: "read_file", Path: "/etc/pve/priv/authkey.key"}).Tier; got == TierRead {
		t.Errorf("reading /etc/pve/priv should need approval, got %s", got)
	}
}
