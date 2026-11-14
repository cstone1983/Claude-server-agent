package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/audit"
	"github.com/cstone1983/Claude-server-agent/internal/proto"
)

// Built-in watching: every minute the hub looks at what it already knows
// (connections and the health samples) and sends ONE Telegram message when a
// problem starts and one when it clears: a server offline, a disk nearly
// full, a failed systemd unit. Optional scheduled checks run a command on a
// server and alert when it fails or prints ALERT lines. Checks only run
// commands that are read-only or allowlisted on that server: anything that
// would need approval is reported as a check error, never sent for approval.

type AlertsConfig struct {
	// Enabled turns the watcher on (needs notify.telegram).
	Enabled bool `yaml:"enabled,omitempty"`
	// OfflineAfterMin: alert when a server has been offline this long (default 5).
	OfflineAfterMin int `yaml:"offline_after_min,omitempty"`
	// DiskPct: alert at this disk usage (default 90; clears 5 points lower).
	DiskPct float64 `yaml:"disk_pct,omitempty"`
	// IgnoreServers are never alerted on (e.g. desktops that sleep).
	IgnoreServers []string `yaml:"ignore_servers,omitempty"`
	// IgnoreUnits are failed units that are expected.
	IgnoreUnits []string `yaml:"ignore_units,omitempty"`
	Checks      []Check  `yaml:"checks,omitempty"`
}

// Check runs argv on a server every Every.
type Check struct {
	Name       string   `yaml:"name"`
	Server     string   `yaml:"server"`
	Argv       []string `yaml:"argv"`
	Every      string   `yaml:"every"` // Go duration, e.g. 15m, 6h
	TimeoutSec int      `yaml:"timeout_sec,omitempty"`
}

type watcher struct {
	h     *Hub
	cfg   AlertsConfig
	start time.Time

	mu       sync.Mutex
	active   map[string]string // alert key -> message while active
	lastRun  map[string]time.Time
	interval map[string]time.Duration
}

func newWatcher(h *Hub, cfg AlertsConfig) (*watcher, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.OfflineAfterMin <= 0 {
		cfg.OfflineAfterMin = 5
	}
	if cfg.DiskPct <= 0 {
		cfg.DiskPct = 90
	}
	w := &watcher{h: h, cfg: cfg, start: time.Now(), active: map[string]string{}, lastRun: map[string]time.Time{}, interval: map[string]time.Duration{}}
	for _, c := range cfg.Checks {
		d, err := time.ParseDuration(c.Every)
		if err != nil || d < time.Minute {
			return nil, fmt.Errorf("alerts check %q: every must be a duration of at least 1m", c.Name)
		}
		if c.Name == "" || c.Server == "" || len(c.Argv) == 0 {
			return nil, fmt.Errorf("alerts check needs name, server and argv")
		}
		w.interval[c.Name] = d
	}
	return w, nil
}

func (w *watcher) run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !w.h.replica.serving() {
			continue // a forwarding standby leaves alerts to the primary
		}
		w.evaluate(time.Now())
		w.runChecks(ctx, time.Now())
	}
}

func (w *watcher) ignored(name string) bool {
	for _, s := range w.cfg.IgnoreServers {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}

// evaluate compares what is wrong now with what was wrong before.
func (w *watcher) evaluate(now time.Time) {
	problems := map[string]string{}
	var agents []AgentRec
	w.h.store.View(func(d *stateData) {
		for _, a := range d.Agents {
			agents = append(agents, *a)
		}
	})
	offlineAfter := time.Duration(w.cfg.OfflineAfterMin) * time.Minute
	for _, a := range agents {
		if w.ignored(a.Name) {
			continue
		}
		if !w.h.reg.online(a.ID) {
			// After a hub start, give agents time to reconnect.
			since := a.LastSeen
			if since.Before(w.start) {
				since = w.start
			}
			if now.Sub(since) >= offlineAfter {
				problems["offline:"+a.ID] = fmt.Sprintf("%s is offline (last seen %s)", a.Name, a.LastSeen.Local().Format("Jan 2 15:04"))
			}
			continue
		}
		hl := w.h.healthOf(a.ID)
		if hl == nil {
			continue
		}
		m, _ := w.h.metrics.get(a.ID)
		for _, d := range m.Latest.Disks {
			key := "disk:" + a.ID + ":" + d.Mount
			limit := w.cfg.DiskPct
			if w.isActive(key) {
				limit -= 5 // clear only well below the alert level
			}
			if d.UsedPct >= limit {
				problems[key] = fmt.Sprintf("%s: disk %s is %.0f%% full", a.Name, d.Mount, d.UsedPct)
			}
		}
		for _, u := range hl.FailedUnits {
			if !w.unitIgnored(u) {
				problems["unit:"+a.ID+":"+u] = fmt.Sprintf("%s: %s failed", a.Name, u)
			}
		}
	}
	// Check results are kept active by runChecks; carry them over.
	w.mu.Lock()
	for k, v := range w.active {
		if strings.HasPrefix(k, "check:") {
			problems[k] = v
		}
	}
	w.mu.Unlock()
	w.apply(problems, "offline:", "disk:", "unit:")
}

func (w *watcher) unitIgnored(u string) bool {
	for _, x := range w.cfg.IgnoreUnits {
		if x == u {
			return true
		}
	}
	return false
}

func (w *watcher) isActive(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.active[key]
	return ok
}

// apply raises new problems and clears ones (with one of the prefixes) that
// are gone, sending one message for all changes.
func (w *watcher) apply(now map[string]string, prefixes ...string) {
	var raised, cleared []string
	w.mu.Lock()
	for k, msg := range now {
		if _, ok := w.active[k]; !ok {
			raised = append(raised, msg)
		}
		w.active[k] = msg
	}
	for k, msg := range w.active {
		owned := false
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				owned = true
			}
		}
		if _, still := now[k]; owned && !still {
			cleared = append(cleared, msg)
			delete(w.active, k)
		}
	}
	w.mu.Unlock()
	w.send(raised, cleared)
}

func (w *watcher) send(raised, cleared []string) {
	if len(raised) == 0 && len(cleared) == 0 {
		return
	}
	sort.Strings(raised)
	sort.Strings(cleared)
	var b strings.Builder
	if len(raised) > 0 {
		b.WriteString("⚠️ Server alert\n")
		for _, r := range raised {
			b.WriteString("• " + r + "\n")
		}
	}
	if len(cleared) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("✅ Resolved\n")
		for _, c := range cleared {
			b.WriteString("• " + c + "\n")
		}
	}
	w.h.audit.Write(audit.Entry{Event: "alert", Detail: map[string]any{"raised": raised, "cleared": cleared}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := w.h.tg.Send(ctx, b.String()); err != nil {
		slog.Warn("alert not sent", "err", err)
	}
}

func (w *watcher) runChecks(ctx context.Context, now time.Time) {
	for _, c := range w.cfg.Checks {
		w.mu.Lock()
		due := now.Sub(w.lastRun[c.Name]) >= w.interval[c.Name]
		if due {
			w.lastRun[c.Name] = now
		}
		w.mu.Unlock()
		if due {
			go w.runCheck(ctx, c)
		}
	}
}

func (w *watcher) runCheck(ctx context.Context, c Check) {
	key := "check:" + c.Name
	problem := w.checkProblem(ctx, c)
	now := map[string]string{}
	if problem != "" {
		now[key] = problem
	}
	// Only this check's key is owned here.
	var raised, cleared []string
	w.mu.Lock()
	prev, was := w.active[key]
	switch {
	case problem != "" && !was:
		raised = append(raised, problem)
		w.active[key] = problem
	case problem != "" && was:
		w.active[key] = problem
	case problem == "" && was:
		cleared = append(cleared, prev)
		delete(w.active, key)
	}
	w.mu.Unlock()
	w.send(raised, cleared)
}

// checkProblem runs a check and returns "" when it passed.
func (w *watcher) checkProblem(ctx context.Context, c Check) string {
	var id string
	w.h.store.View(func(d *stateData) {
		for _, a := range d.Agents {
			if a.Name == c.Server {
				id = a.ID
			}
		}
	})
	if id == "" {
		return fmt.Sprintf("check %s: unknown server %s", c.Name, c.Server)
	}
	if !w.h.reg.online(id) {
		return "" // the offline alert covers it
	}
	timeout := c.TimeoutSec
	if timeout <= 0 {
		timeout = 120
	}
	args, _ := json.Marshal(map[string]any{"argv": c.Argv, "timeout_sec": timeout, "reason": "scheduled check " + c.Name})
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout+30)*time.Second)
	defer cancel()
	res, err := w.h.callAgent(cctx, id, &proto.Call{Tool: "run_command", Args: args, Caller: "hub:check:" + c.Name})
	switch {
	case err != nil:
		return fmt.Sprintf("check %s on %s could not run: %v", c.Name, c.Server, err)
	case res.NeedsApproval != nil:
		return fmt.Sprintf("check %s on %s is not allowlisted (add a rule for it)", c.Name, c.Server)
	case res.Error != "":
		return fmt.Sprintf("check %s on %s: %s", c.Name, c.Server, truncate(res.Error, 200))
	}
	var alerts []string
	for _, l := range strings.Split(res.Text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "ALERT") {
			alerts = append(alerts, strings.TrimSpace(l))
		}
	}
	if len(alerts) > 0 {
		if len(alerts) > 8 {
			alerts = append(alerts[:8], fmt.Sprintf("(+%d more)", len(alerts)-8))
		}
		return fmt.Sprintf("check %s on %s:\n  %s", c.Name, c.Server, strings.Join(alerts, "\n  "))
	}
	if !res.OK {
		return fmt.Sprintf("check %s on %s failed:\n  %s", c.Name, c.Server, truncate(strings.TrimSpace(res.Text), 400))
	}
	return ""
}
