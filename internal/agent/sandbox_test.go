package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func runSandboxed(t *testing.T, script string) procResult {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("sandbox needs root")
	}
	argv, env := shellSpec(script)
	r := runProcess(context.Background(), procSpec{Argv: argv, Env: env, Sandbox: true, Timeout: 10 * time.Second}, 8192)
	if r.ExitCode == -1 && strings.Contains(r.Stderr, "operation not permitted") {
		t.Skip("network namespaces not available here: " + r.Stderr)
	}
	return r
}

func TestSandboxRunsAsNobodyWithoutNetwork(t *testing.T) {
	r := runSandboxed(t, `id -u; echo cwd=$PWD; head -1 /proc/self/status; cat /proc/net/dev | tail -n +3 | cut -d: -f1 | tr -d ' '`)
	if r.ExitCode != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", r.ExitCode, r.Stdout, r.Stderr)
	}
	lines := strings.Fields(r.Stdout)
	if len(lines) == 0 || lines[0] != "65534" {
		t.Fatalf("not running as nobody: %q", r.Stdout)
	}
	if !strings.Contains(r.Stdout, "cwd=/") {
		t.Fatalf("cwd not /: %q", r.Stdout)
	}
	for _, l := range strings.Split(r.Stdout, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "eth") || strings.HasPrefix(l, "en") || strings.HasPrefix(l, "wl") {
			t.Fatalf("sandbox can see a real network interface %q: %q", l, r.Stdout)
		}
	}
}

func TestSandboxCannotWriteSystemFiles(t *testing.T) {
	f, err := os.CreateTemp("", "csa-sandbox-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())
	os.Chmod(f.Name(), 0o644)
	r := runSandboxed(t, "echo pwned > "+f.Name()+" && echo WROTE; cat /etc/shadow >/dev/null 2>&1 && echo READ-SHADOW; echo done")
	if strings.Contains(r.Stdout, "WROTE") || strings.Contains(r.Stdout, "READ-SHADOW") {
		t.Fatalf("sandbox escaped: %q", r.Stdout)
	}
	if b, _ := os.ReadFile(f.Name()); len(b) != 0 {
		t.Fatalf("root-owned file modified: %q", b)
	}
	if !strings.Contains(r.Stdout, "done") {
		t.Fatalf("script did not finish: %q %q", r.Stdout, r.Stderr)
	}
}

func TestSandboxCannotConnect(t *testing.T) {
	r := runSandboxed(t, `(exec 3<>/dev/tcp/1.1.1.1/443) 2>/dev/null && echo CONNECTED; echo done`)
	if strings.Contains(r.Stdout, "CONNECTED") {
		t.Fatalf("sandbox has network: %q", r.Stdout)
	}
}
