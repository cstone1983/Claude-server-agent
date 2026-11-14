package hub

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newReplicaHub(t *testing.T, role, peer, secretFile string) *Hub {
	t.Helper()
	dir := t.TempDir()
	cfg := &Config{StateDir: filepath.Join(dir, "s"), AuditLog: filepath.Join(dir, "a.jsonl"), PublicURL: "https://agents.example.com",
		Replica: ReplicaConfig{Role: role, PeerURL: peer, SecretFile: secretFile, TakeoverAfterSec: 1}}
	cfg.applyDefaults()
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func get(t *testing.T, url, host string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestStandbyCopiesForwardsTakesOverAndMergesBack(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "replica.secret")
	os.WriteFile(secret, []byte("0123456789abcdef0123456789abcdef\n"), 0o600)

	primary := newReplicaHub(t, "primary", "", secret)
	psrv := httptest.NewServer(primary.Handler())
	primary.store.Update(func(d *stateData) error {
		d.Agents["a1"] = &AgentRec{ID: "a1", Name: "web", EnrolledAt: time.Now()}
		return nil
	})

	standby := newReplicaHub(t, "standby", psrv.URL, secret)
	ssrv := httptest.NewServer(standby.Handler())
	defer ssrv.Close()
	ctx := context.Background()

	// 1. Copies the primary's state.
	standby.replica.tick(ctx)
	if standby.agentByID("a1") == nil {
		t.Fatal("standby did not copy the primary's state")
	}
	// 2. Forwards requests (public host) and refuses the UI on its LAN address.
	if _, body := get(t, ssrv.URL+"/healthz", ""); !strings.Contains(body, "forwarding") {
		t.Fatalf("healthz: %q", body)
	}
	if code, _ := get(t, ssrv.URL+"/.well-known/oauth-authorization-server", "agents.example.com"); code != 200 {
		t.Fatalf("forwarded request failed: %d", code)
	}
	if code, _ := get(t, ssrv.URL+"/", "10.10.11.50:7420"); code != http.StatusMisdirectedRequest {
		t.Fatalf("UI on a LAN host name should be refused, got %d", code)
	}
	// Replication refuses a wrong secret and anything from the tunnel.
	req, _ := http.NewRequest(http.MethodGet, psrv.URL+"/replica/state", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong secret: %d", resp.StatusCode)
	}

	// 3. Primary goes down: forwarding fails, then the standby takes over.
	psrv.Close()
	if code, _ := get(t, ssrv.URL+"/.well-known/oauth-authorization-server", "agents.example.com"); code != http.StatusBadGateway {
		t.Fatalf("expected 502 while primary is down and before takeover, got %d", code)
	}
	time.Sleep(1100 * time.Millisecond)
	standby.replica.tick(ctx)
	if !standby.replica.active.Load() {
		t.Fatal("standby did not take over")
	}
	if code, _ := get(t, ssrv.URL+"/.well-known/oauth-authorization-server", "agents.example.com"); code != 200 {
		t.Fatalf("standby not serving: %d", code)
	}
	// Things happen while the standby serves: a new login and hands-off mode.
	standby.store.Update(func(d *stateData) error {
		d.Tokens["h1"] = &Token{Kind: TokenRefresh, Scope: ScopeFull, CreatedAt: time.Now()}
		return nil
	})
	if _, err := standby.setAutoApprove(time.Hour, "all", false, "test"); err != nil {
		t.Fatal(err)
	}

	// 4. Primary comes back (new address in this test): changes are merged
	// into it and the standby forwards again.
	psrv2 := httptest.NewServer(primary.Handler())
	defer psrv2.Close()
	standby.replica.peer, _ = standby.replica.peer.Parse(psrv2.URL)
	standby.replica.proxy = standby.replica.newProxy()
	standby.replica.tick(ctx)
	if standby.replica.active.Load() {
		t.Fatal("standby still serving after the primary came back")
	}
	var hasToken bool
	primary.store.View(func(d *stateData) { hasToken = d.Tokens["h1"] != nil })
	if !hasToken {
		t.Fatal("login made on the standby was not merged into the primary")
	}
	if aa := primary.autoApprove(); aa == nil || aa.Scope != "all" {
		t.Fatalf("hands-off mode set on the standby was not carried over: %+v", aa)
	}
	// Turning hands-off off on the primary later wins over the old standby copy.
	primary.clearAutoApprove("test")
	standby.replica.tick(ctx)
	if standby.autoApprove() != nil {
		t.Fatal("standby should copy hands-off off from the primary")
	}
}
