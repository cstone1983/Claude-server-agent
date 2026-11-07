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
