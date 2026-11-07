package hub

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// limiter blocks a key after too many failures in a window.
type limiter struct {
	mu     sync.Mutex
	fails  map[string][]time.Time
	max    int
	window time.Duration
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{fails: map[string][]time.Time{}, max: max, window: window}
}

func (l *limiter) prune(key string) []time.Time {
	cut := time.Now().Add(-l.window)
	f := l.fails[key]
	i := 0
	for i < len(f) && f[i].Before(cut) {
		i++
	}
	f = f[i:]
	if len(f) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = f
	}
	return f
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key)) < l.max
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key] = append(l.prune(key), time.Now())
}

// clientIP returns the real client address. Behind cloudflared every request
// comes from loopback, so CF-Connecting-IP is trusted only in that case.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
			return strings.TrimSpace(cf)
		}
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			return strings.TrimSpace(strings.Split(xf, ",")[0])
		}
	}
	return host
}
