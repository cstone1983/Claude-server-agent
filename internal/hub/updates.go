package hub

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ReleaseRepo is where releases are published.
const ReleaseRepo = "cstone1983/Claude-server-agent"

// releaseWatcher periodically looks up the latest GitHub release so the hub
// can tell Claude when servers are behind. It never installs anything.
type releaseWatcher struct {
	mu      sync.RWMutex
	latest  string
	checked time.Time
	url     string
	client  *http.Client
}

func newReleaseWatcher() *releaseWatcher {
	return &releaseWatcher{
		url: "https://github.com/" + ReleaseRepo + "/releases/latest",
		client: &http.Client{
			Timeout: 20 * time.Second,
			// /releases/latest redirects to /releases/tag/<tag>; read the tag
			// from the redirect instead of using the rate-limited API.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (w *releaseWatcher) check(ctx context.Context) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodHead, w.url, nil)
	resp, err := w.client.Do(req)
	if err != nil {
		slog.Debug("release check failed", "err", err)
		return
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	tag := ""
	if i := strings.Index(loc, "/releases/tag/"); i >= 0 {
		tag = loc[i+len("/releases/tag/"):]
	}
	w.mu.Lock()
	w.latest, w.checked = tag, time.Now()
	w.mu.Unlock()
}

func (w *releaseWatcher) run(ctx context.Context) {
	w.check(ctx)
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.check(ctx)
		}
	}
}

func (w *releaseWatcher) Latest() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.latest
}

// versionOf extracts the version from "v0.1.5 (abc1234)".
func versionOf(agentVersion string) string {
	v, _, _ := strings.Cut(agentVersion, " ")
	return v
}

// updateAvailable reports whether a server's agent is behind the latest release.
func (h *Hub) updateAvailable(agentVersion string) (string, bool) {
	latest := h.releases.Latest()
	if latest == "" || agentVersion == "" {
		return latest, false
	}
	return latest, newerVersion(latest, versionOf(agentVersion))
}

// newerVersion reports whether release tag a is newer than installed version
// b. Tags look like v0.1.12. A non-release build (e.g. a commit hash from a
// source install) counts as older than any release.
func newerVersion(a, b string) bool {
	pa, okA := parseVersion(a)
	pb, okB := parseVersion(b)
	if !okA {
		return false
	}
	if !okB {
		return true
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}
