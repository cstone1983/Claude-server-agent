package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.1.2", "v0.1.1", true},
		{"v0.1.1", "v0.1.2", false}, // never suggest a downgrade
		{"v0.1.2", "v0.1.2", false},
		{"v0.1.10", "v0.1.9", true},
		{"v0.2.0", "v0.1.99", true},
		{"v0.1.2", "a1b2c3d", true}, // source build
		{"v0.1.2", "dev", true},
		{"garbage", "v0.1.1", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestReleaseWatcher(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/cstone1983/Claude-server-agent/releases/tag/v0.1.7", http.StatusFound)
	}))
	defer srv.Close()
	w := newReleaseWatcher()
	w.url = srv.URL + "/latest"
	w.check(context.Background())
	if w.Latest() != "v0.1.7" {
		t.Fatalf("latest = %q", w.Latest())
	}

	h := &Hub{releases: w, store: &Store{d: stateData{Agents: map[string]*AgentRec{
		"a": {ID: "a", Name: "old", AgentVersion: "v0.1.5 (abc1234)"},
		"b": {ID: "b", Name: "new", AgentVersion: "v0.1.7 (def5678)"},
	}}}, reg: &registry{conns: map[string]*agentConn{}}}
	out := h.listServers(Caller{})
	if !strings.Contains(out.Text, "old — offline") || !strings.Contains(out.Text, "UPDATE AVAILABLE: v0.1.7") ||
		strings.Count(out.Text, "UPDATE AVAILABLE") != 1 || !strings.Contains(out.Text, "1 server(s) can be updated") {
		t.Fatalf("list_servers:\n%s", out.Text)
	}

	// No release published yet: GitHub redirects to /releases, no flag shown.
	none := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/cstone1983/Claude-server-agent/releases", http.StatusFound)
	}))
	defer none.Close()
	w.url = none.URL + "/latest"
	w.check(context.Background())
	if out := h.listServers(Caller{}); strings.Contains(out.Text, "UPDATE AVAILABLE") {
		t.Fatalf("flag without a release:\n%s", out.Text)
	}
}

func TestHealthLine(t *testing.T) {
	if a, s := parseUpdates("5 updates can be applied immediately. ; 2 of these updates are standard security updates. ; To see these additional updates run: apt list --upgradable"); a != 5 || s != 2 {
		t.Fatalf("parseUpdates = %d, %d", a, s)
	}
	hl := &health{UptimeHours: 100, Load: "0.10 0.20 0.30", MemUsedPct: 41, FullestDisk: "/", FullestDiskPct: 93, Updates: 5, SecurityUpd: 2, RebootRequired: true, FailedUnits: []string{"x.service"}}
	got := hl.line()
	for _, want := range []string{"up 4d", "disk / 93% (NEARLY FULL)", "5 updates (2 security)", "REBOOT PENDING", "FAILED: x.service"} {
		if !strings.Contains(got, want) {
			t.Errorf("line %q lacks %q", got, want)
		}
	}
}
