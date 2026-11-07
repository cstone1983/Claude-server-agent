package agent

import (
	"bufio"
	"context"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cstone1983/Claude-server-agent/internal/version"
)

type DiskInfo struct {
	Mount   string  `json:"mount"`
	FS      string  `json:"fs"`
	SizeGB  float64 `json:"size_gb"`
	UsedGB  float64 `json:"used_gb"`
	UsedPct float64 `json:"used_pct"`
}

type SystemInfo struct {
	Hostname       string              `json:"hostname"`
	OS             string              `json:"os"`
	Kernel         string              `json:"kernel"`
	Arch           string              `json:"arch"`
	UptimeHours    float64             `json:"uptime_hours"`
	Load           string              `json:"load"`
	CPUs           int                 `json:"cpus"`
	MemTotalMB     int64               `json:"mem_total_mb"`
	MemAvailMB     int64               `json:"mem_available_mb"`
	SwapUsedMB     int64               `json:"swap_used_mb"`
	Disks          []DiskInfo          `json:"disks"`
	IPs            map[string][]string `json:"ips"`
	RebootRequired bool                `json:"reboot_required"`
	Updates        string              `json:"updates,omitempty"`
	FailedUnits    []string            `json:"failed_units"`
	AgentVersion   string              `json:"agent_version"`
}

func osPrettyName() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return runtime.GOOS
}

func kernelRelease() string {
	b, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return strings.TrimSpace(string(b))
}

func collectSystemInfo(ctx context.Context) SystemInfo {
	si := SystemInfo{OS: osPrettyName(), Kernel: kernelRelease(), Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), AgentVersion: version.String()}
	si.Hostname, _ = os.Hostname()
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			s, _ := strconv.ParseFloat(f[0], 64)
			si.UptimeHours = float64(int(s/36)) / 100
		}
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) >= 3 {
			si.Load = strings.Join(f[:3], " ")
		}
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		vals := map[string]int64{}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			p := strings.Fields(sc.Text())
			if len(p) >= 2 {
				n, _ := strconv.ParseInt(p[1], 10, 64)
				vals[strings.TrimSuffix(p[0], ":")] = n
			}
		}
		f.Close()
		si.MemTotalMB = vals["MemTotal"] / 1024
		si.MemAvailMB = vals["MemAvailable"] / 1024
		si.SwapUsedMB = (vals["SwapTotal"] - vals["SwapFree"]) / 1024
	}
	si.Disks = disks()
	si.IPs = map[string][]string{}
	if ifs, err := net.Interfaces(); err == nil {
		for _, ifc := range ifs {
			if ifc.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, _ := ifc.Addrs()
			for _, a := range addrs {
				si.IPs[ifc.Name] = append(si.IPs[ifc.Name], a.String())
			}
		}
	}
	_, err := os.Stat("/var/run/reboot-required")
	si.RebootRequired = err == nil
	if b, err := os.ReadFile("/var/lib/update-notifier/updates-available"); err == nil {
		si.Updates = strings.Join(strings.Fields(strings.ReplaceAll(string(b), "\n", " ; ")), " ")
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r := runProcess(cctx, procSpec{Argv: []string{"systemctl", "list-units", "--failed", "--no-legend", "--plain", "--no-pager"}, Timeout: 5 * time.Second}, 16*1024)
	si.FailedUnits = []string{}
	for _, l := range strings.Split(strings.TrimSpace(r.Stdout), "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			si.FailedUnits = append(si.FailedUnits, f[0])
		}
	}
	return si
}

var realFS = map[string]bool{"ext4": true, "ext3": true, "ext2": true, "xfs": true, "btrfs": true, "zfs": true, "vfat": true, "f2fs": true, "nfs": true, "nfs4": true, "cifs": true, "fuseblk": true, "ntfs3": true}

func disks() []DiskInfo {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []DiskInfo
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Fields(sc.Text())
		if len(p) < 3 || !realFS[p[2]] || seen[p[0]] || strings.HasPrefix(p[1], "/snap/") {
			continue
		}
		seen[p[0]] = true
		var st syscall.Statfs_t
		if syscall.Statfs(p[1], &st) != nil || st.Blocks == 0 {
			continue
		}
		size := float64(st.Blocks) * float64(st.Bsize)
		free := float64(st.Bavail) * float64(st.Bsize)
		used := size - float64(st.Bfree)*float64(st.Bsize)
		gb := func(v float64) float64 { return float64(int(v/1e7)) / 100 }
		out = append(out, DiskInfo{Mount: p[1], FS: p[2], SizeGB: gb(size), UsedGB: gb(used), UsedPct: float64(int(used/(used+free)*1000)) / 10})
	}
	return out
}
