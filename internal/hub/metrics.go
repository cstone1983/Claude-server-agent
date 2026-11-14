package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/proto"
)

// The hub samples each online server's health once a minute (via the
// read-only system_info tool) and keeps 24 hours in memory for the overview
// charts.

const (
	metricsEvery  = time.Minute
	metricsPoints = 24 * 60
)

type sysInfo struct {
	Hostname    string  `json:"hostname"`
	OS          string  `json:"os"`
	UptimeHours float64 `json:"uptime_hours"`
	Load        string  `json:"load"`
	CPUs        int     `json:"cpus"`
	MemTotalMB  int64   `json:"mem_total_mb"`
	MemAvailMB  int64   `json:"mem_available_mb"`
	Disks       []struct {
		Mount   string  `json:"mount"`
		SizeGB  float64 `json:"size_gb"`
		UsedPct float64 `json:"used_pct"`
	} `json:"disks"`
	RebootRequired bool     `json:"reboot_required"`
	Updates        string   `json:"updates"`
	FailedUnits    []string `json:"failed_units"`
}

type sample struct {
	T      time.Time
	Load1  float64
	MemPct float64
}

type serverMetrics struct {
	Latest  sysInfo
	At      time.Time
	Samples []sample
}

type metricsStore struct {
	mu sync.Mutex
	m  map[string]*serverMetrics // by agent ID
}

func (ms *metricsStore) add(id string, si sysInfo, at time.Time) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	s := ms.m[id]
	if s == nil {
		s = &serverMetrics{}
		ms.m[id] = s
	}
	s.Latest, s.At = si, at
	var load1 float64
	fmt.Sscan(strings.Fields(si.Load + " 0")[0], &load1)
	mem := 0.0
	if si.MemTotalMB > 0 {
		mem = 100 * float64(si.MemTotalMB-si.MemAvailMB) / float64(si.MemTotalMB)
	}
	s.Samples = append(s.Samples, sample{T: at, Load1: load1, MemPct: mem})
	if len(s.Samples) > metricsPoints {
		s.Samples = s.Samples[len(s.Samples)-metricsPoints:]
	}
}

func (ms *metricsStore) get(id string) (serverMetrics, bool) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	s := ms.m[id]
	if s == nil {
		return serverMetrics{}, false
	}
	cp := *s
	cp.Samples = append([]sample(nil), s.Samples...)
	return cp, true
}

// sampleOne records one health sample for an agent.
func (h *Hub) sampleOne(ctx context.Context, id string) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := h.callAgent(cctx, id, &proto.Call{Tool: "system_info", Args: json.RawMessage(`{}`), Caller: "hub:metrics", MaxTier: "read"})
	if err != nil || res == nil || len(res.Data) == 0 {
		return
	}
	var si sysInfo
	if json.Unmarshal(res.Data, &si) == nil {
		h.metrics.add(id, si, time.Now())
	}
}

func (h *Hub) collectMetrics(ctx context.Context) {
	sampleAll := func() {
		var ids []string
		h.store.View(func(d *stateData) {
			for id := range d.Agents {
				if h.reg.online(id) {
					ids = append(ids, id)
				}
			}
		})
		for _, id := range ids {
			go h.sampleOne(ctx, id)
		}
	}
	sampleAll()
	t := time.NewTicker(metricsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sampleAll()
		}
	}
}

// sparkline renders values (oldest first) as a small inline SVG.
func sparkline(vals []float64, max float64, color string) template.HTML {
	if len(vals) < 2 {
		return ""
	}
	if max <= 0 {
		for _, v := range vals {
			max = math.Max(max, v)
		}
		if max <= 0 {
			max = 1
		}
	}
	const w, hgt = 300.0, 38.0
	var line strings.Builder
	for i, v := range vals {
		x := w * float64(i) / float64(len(vals)-1)
		y := hgt - 2 - (hgt-4)*math.Min(v, max)/max
		if i == 0 {
			fmt.Fprintf(&line, "M%.1f %.1f", x, y)
		} else {
			fmt.Fprintf(&line, " L%.1f %.1f", x, y)
		}
	}
	area := line.String() + fmt.Sprintf(" L%.1f %.1f L0 %.1f Z", w, hgt, hgt)
	return template.HTML(fmt.Sprintf(`<svg class="spark" viewBox="0 0 %.0f %.0f" preserveAspectRatio="none" aria-hidden="true"><path class="area" d="%s" fill="%s"/><path class="line" d="%s" stroke="%s"/></svg>`,
		w, hgt, area, color, line.String(), color))
}

// health is a short per-server summary from the latest sample, shown by
// list_servers so problems are visible without extra calls.
type health struct {
	UptimeHours    float64   `json:"uptime_hours"`
	Load           string    `json:"load"`
	MemUsedPct     float64   `json:"mem_used_pct"`
	FullestDisk    string    `json:"fullest_disk,omitempty"`
	FullestDiskPct float64   `json:"fullest_disk_pct,omitempty"`
	RebootRequired bool      `json:"reboot_required"`
	Updates        int       `json:"updates"`
	SecurityUpd    int       `json:"security_updates"`
	FailedUnits    []string  `json:"failed_units,omitempty"`
	SampledAt      time.Time `json:"sampled_at"`
}

// healthOf returns the summary for a server, or nil without a recent sample.
func (h *Hub) healthOf(id string) *health {
	if h.metrics == nil {
		return nil
	}
	m, ok := h.metrics.get(id)
	if !ok || time.Since(m.At) > 10*time.Minute {
		return nil
	}
	si := m.Latest
	hl := &health{UptimeHours: si.UptimeHours, Load: si.Load, RebootRequired: si.RebootRequired, FailedUnits: si.FailedUnits, SampledAt: m.At}
	if si.MemTotalMB > 0 {
		hl.MemUsedPct = math.Round(1000*float64(si.MemTotalMB-si.MemAvailMB)/float64(si.MemTotalMB)) / 10
	}
	for _, d := range si.Disks {
		if d.UsedPct > hl.FullestDiskPct {
			hl.FullestDisk, hl.FullestDiskPct = d.Mount, d.UsedPct
		}
	}
	hl.Updates, hl.SecurityUpd = parseUpdates(si.Updates)
	return hl
}

// parseUpdates reads update-notifier's text, e.g. "5 updates can be applied
// immediately. ; 2 of these updates are standard security updates."
func parseUpdates(s string) (all, security int) {
	for _, part := range strings.Split(s, ";") {
		f := strings.Fields(part)
		if len(f) < 2 {
			continue
		}
		var n int
		if _, err := fmt.Sscan(f[0], &n); err != nil {
			continue
		}
		switch {
		case strings.Contains(part, "security"):
			security = n
		case strings.Contains(part, "can be applied"):
			all = n
		}
	}
	return
}

func (hl *health) line() string {
	var b strings.Builder
	up := hl.UptimeHours
	if up >= 48 {
		fmt.Fprintf(&b, "up %.0fd", up/24)
	} else {
		fmt.Fprintf(&b, "up %.0fh", up)
	}
	fmt.Fprintf(&b, ", load %s, mem %.0f%%", hl.Load, hl.MemUsedPct)
	if hl.FullestDisk != "" {
		fmt.Fprintf(&b, ", disk %s %.0f%%", hl.FullestDisk, hl.FullestDiskPct)
		if hl.FullestDiskPct >= 90 {
			b.WriteString(" (NEARLY FULL)")
		}
	}
	if hl.Updates > 0 {
		fmt.Fprintf(&b, ", %d updates", hl.Updates)
		if hl.SecurityUpd > 0 {
			fmt.Fprintf(&b, " (%d security)", hl.SecurityUpd)
		}
	}
	if hl.RebootRequired {
		b.WriteString(", REBOOT PENDING")
	}
	if len(hl.FailedUnits) > 0 {
		fmt.Fprintf(&b, ", FAILED: %s", strings.Join(hl.FailedUnits, " "))
	}
	return b.String()
}
