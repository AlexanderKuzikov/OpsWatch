//go:build !windows

package probe

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Load reports the 1-minute load average against CPU count. Read straight
// from /proc, no exec, no deps.
func Load() Result {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return Skip("load", "no /proc/loadavg on this platform")
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return Warn("load", "cannot parse /proc/loadavg")
	}
	l1, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return Warn("load", "cannot parse /proc/loadavg")
	}
	n := float64(runtime.NumCPU())
	detail := "load " + strings.Join(f[:3], " ") + " при " + strconv.Itoa(runtime.NumCPU()) + " CPU"
	switch {
	case l1 >= 2*n:
		return Fail("load", "%s — перегруз вдвое", detail)
	case l1 >= n:
		return Warn("load", "%s — очередь к CPU", detail)
	default:
		return Pass("load", "%s", detail)
	}
}

// Mem reports RAM usage from /proc/meminfo (MemAvailable is what the kernel
// can actually hand out, not just MemFree).
func Mem() Result {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return Skip("mem", "no /proc/meminfo on this platform")
	}
	var total, avail int64
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, cerr := strconv.ParseInt(f[1], 10, 64)
		if cerr != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	}
	if total <= 0 {
		return Warn("mem", "cannot parse /proc/meminfo")
	}
	pct := float64(total-avail) / float64(total) * 100
	detail := strings.TrimSpace("память " + human(total-avail) + " из " + human(total))
	switch {
	case pct >= 95:
		return Fail("mem", "%s — почти кончилась", detail)
	case pct >= 85:
		return Warn("mem", "%s — заканчивается", detail)
	default:
		return Pass("mem", "%s", detail)
	}
}
