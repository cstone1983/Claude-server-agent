package policy

import "testing"

func TestShellContainsWholeWords(t *testing.T) {
	cases := []struct {
		script, pat string
		want        bool
	}{
		{"[ -f /var/run/reboot-required ] && echo reboot needed", "reboot", true}, // "echo reboot" is the word reboot
		{"[ -f /var/run/reboot-required ] && cat /var/run/reboot-required.pkgs", "reboot", false},
		{"systemctl reboot", "reboot", true},
		{"/sbin/reboot now", "reboot", true},
		{"sleep 1; reboot", "reboot", true},
		{"(reboot)", "reboot", true},
		{"echo $(shutdown -h now)", "shutdown", true},
		{"grep -c shutdown-hooks /etc/x", "shutdown", false},
		{"qm destroy 101", "qm destroy", true},
		{"qm list; echo destroy", "qm destroy", false},
		{"rm -rf /", "rm -rf /", true},
	}
	for _, c := range cases {
		if got := shellContains(c.script, c.pat); got != c.want {
			t.Errorf("shellContains(%q, %q) = %v, want %v", c.script, c.pat, got, c.want)
		}
	}
}

func TestShellDenyNoFalseAlarmOnPaths(t *testing.T) {
	p := New(File{Deny: []Rule{{ID: "no-reboot", Shell: "reboot"}}}, nil)
	ok := p.Classify(Action{Kind: "shell", Shell: `[ -f /var/run/reboot-required ] && echo yes || echo no`})
	if ok.Tier == TierDenied {
		t.Fatalf("a script that only reads /var/run/reboot-required was denied: %+v", ok)
	}
	bad := p.Classify(Action{Kind: "shell", Shell: "apt-get -y upgrade && systemctl reboot"})
	if bad.Tier != TierDenied {
		t.Fatalf("a script that reboots was not denied: %+v", bad)
	}
}

func TestInspectIsReadOnlyButHonoursDenyRules(t *testing.T) {
	p := New(File{Deny: []Rule{{ID: "d1", Shell: "secret-tool"}}}, nil)
	if d := p.Classify(Action{Kind: "inspect", Shell: "journalctl -n 50 | grep reboot"}); d.Tier != TierRead {
		t.Fatalf("inspect should be read-only, got %+v", d)
	}
	if d := p.Classify(Action{Kind: "inspect", Shell: "secret-tool lookup x"}); d.Tier != TierDenied {
		t.Fatalf("deny rule should apply to inspect, got %+v", d)
	}
}
