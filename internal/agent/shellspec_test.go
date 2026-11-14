package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A script that pkills processes matching its own text must not kill itself
// (the script text is no longer part of the shell's command line).
func TestShellSpecSurvivesSelfPkill(t *testing.T) {
	script := "pkill -f csa-selfkill-marker-xyz; echo still-alive; echo ${CSA_SCRIPT:-unset}"
	argv, env := shellSpec(script)
	r := runProcess(context.Background(), procSpec{Argv: argv, Env: env, Timeout: 10 * time.Second}, 4096)
	if !strings.Contains(r.Stdout, "still-alive") {
		t.Fatalf("script killed itself: exit=%d stdout=%q stderr=%q", r.ExitCode, r.Stdout, r.Stderr)
	}
	if !strings.Contains(r.Stdout, "unset") {
		t.Fatalf("CSA_SCRIPT leaked into the script's environment: %q", r.Stdout)
	}
}

func TestShellSpecRunsMultilineScripts(t *testing.T) {
	script := "set -e\nx=1\nif [ $x = 1 ]; then\n  echo \"ok $x\"\nfi\ncat <<'EOT'\nheredoc $x\nEOT\nexit 3"
	argv, env := shellSpec(script)
	r := runProcess(context.Background(), procSpec{Argv: argv, Env: env, Timeout: 10 * time.Second}, 4096)
	if r.ExitCode != 3 || !strings.Contains(r.Stdout, "ok 1") || !strings.Contains(r.Stdout, "heredoc $x") {
		t.Fatalf("unexpected result: exit=%d stdout=%q stderr=%q", r.ExitCode, r.Stdout, r.Stderr)
	}
}
