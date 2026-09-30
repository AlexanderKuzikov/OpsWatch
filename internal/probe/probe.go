// Package probe runs the live checks: HTTP endpoints, TLS expiry, systemd
// units, listening ports and rclone-reported quotas.
package probe

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/AlexanderKuzikov/OpsWatch/internal/registry"
)

// errNoStatfs is returned by the platform disk probe where the free space of a
// volume cannot be queried. Defined here so both build tags can see it.
var errNoStatfs = errors.New("filesystem free space not available on this platform")

// Result is one check outcome.
//
// Warn means "attention soon" but not broken, which keeps the alert from
// firing on a cert that has 20 days left. Skipped means the check does not
// apply on this machine — running the VPS-only systemd probes from a laptop
// must not be reported as a broken server.
type Result struct {
	Name    string
	OK      bool
	Warn    bool
	Skipped bool
	Detail  string
}

// Status renders the result as a single word for the report.
func (r Result) Status() string {
	switch {
	case r.Skipped:
		return "SKIP"
	case !r.OK:
		return "FAIL"
	case r.Warn:
		return "WARN"
	default:
		return "OK"
	}
}

// Fail builds a failed result.
func Fail(name string, format string, a ...any) Result {
	return Result{Name: name, OK: false, Detail: fmt.Sprintf(format, a...)}
}

// Warn builds a warning result.
func Warn(name string, format string, a ...any) Result {
	return Result{Name: name, OK: true, Warn: true, Detail: fmt.Sprintf(format, a...)}
}

// Pass builds a passing result.
func Pass(name string, format string, a ...any) Result {
	return Result{Name: name, OK: true, Detail: fmt.Sprintf(format, a...)}
}

// Skip builds a not-applicable-here result.
func Skip(name string, format string, a ...any) Result {
	return Result{Name: name, OK: true, Skipped: true, Detail: fmt.Sprintf(format, a...)}
}

// missingTool reports whether exec failed because the binary is absent, which
// means "not this platform" rather than "broken".
func missingTool(err error) bool {
	var ee *exec.Error
	if errors.As(err, &ee) {
		return true
	}
	return strings.Contains(err.Error(), "executable file not found") ||
		strings.Contains(err.Error(), "not found in %PATH%") ||
		strings.Contains(err.Error(), "cannot find the file")
}

// HTTP probes an endpoint for status code, latency and optional body marker.
func HTTP(ctx context.Context, ep registry.Endpoint) Result {
	timeout := time.Duration(ep.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.URL, nil)
	if err != nil {
		return Fail(ep.Name, "bad url %s: %v", ep.URL, err)
	}
	for k, v := range ep.Headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: timeout,
		},
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Fail(ep.Name, "%s: %v", ep.URL, err)
	}
	defer resp.Body.Close()
	latency := time.Since(start)

	body := ""
	if ep.BodyContains != "" {
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if rerr != nil {
			return Fail(ep.Name, "%s: reading body: %v", ep.URL, rerr)
		}
		body = string(b)
	}
	want := ep.ExpectStatus
	if resp.StatusCode != want {
		return Fail(ep.Name, "%s: got HTTP %d, want %d", ep.URL, resp.StatusCode, want)
	}
	if ep.BodyContains != "" && !strings.Contains(body, ep.BodyContains) {
		return Fail(ep.Name, "%s: body missing %q", ep.URL, ep.BodyContains)
	}
	return Pass(ep.Name, "HTTP %d in %s", resp.StatusCode, latency.Round(time.Millisecond))
}

// TLSCert dials host:443 and reports days until the leaf certificate expires.
// It fails closed: a TLS error is a FAIL, not a silent skip, because an
// expired or untrusted cert is exactly what this check exists to catch.
func TLSCert(host string, warnBeforeDays int) Result {
	name := "tls " + host
	if warnBeforeDays <= 0 {
		warnBeforeDays = 14
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(host, "443"), &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return Fail(name, "%s: %v", host, err)
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return Fail(name, "%s: no certificate presented", host)
	}
	leaf := state.PeerCertificates[0]
	days := int(time.Until(leaf.NotAfter).Hours() / 24)
	issuer := leaf.Issuer.CommonName
	switch {
	case days < 0:
		return Fail(name, "%s: EXPIRED %s (%d days ago), issuer %s", host, leaf.NotAfter.Format("2006-01-02"), -days, issuer)
	case days <= warnBeforeDays:
		return Warn(name, "%s: expires in %d days (%s), issuer %s", host, days, leaf.NotAfter.Format("2006-01-02"), issuer)
	default:
		return Pass(name, "%s: valid %d more days (until %s)", host, days, leaf.NotAfter.Format("2006-01-02"))
	}
}

// Unit checks a systemd unit is active.
func Unit(name string) Result {
	out, err := exec.Command("systemctl", "is-active", name).Output()
	state := strings.TrimSpace(string(out))
	if err != nil {
		if missingTool(err) {
			return Skip("unit "+name, "systemctl not present: this host is not the server being watched")
		}
		return Fail("unit "+name, "systemctl is-active %s: %v (state %q)", name, err, state)
	}
	if state != "active" {
		return Fail("unit "+name, "%s is %s, want active", name, state)
	}
	return Pass("unit "+name, "active")
}

// NRestarts reads the cumulative restart counter for a unit.
//
// The number is cumulative for the unit's lifetime and is NOT reset by
// automatic restarts, so a large value says nothing about the present. Use
// Snapshot and compare two samples to detect a live crash loop.
//
// ok is false when the unit has no such counter (timers and sockets) or
// systemd cannot answer.
func NRestarts(name string) (int, bool) {
	out, err := exec.Command("systemctl", "show", name, "-p", "NRestarts", "--value").Output()
	if err != nil {
		return 0, false
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return 0, false
	}
	n, cerr := strconv.Atoi(v)
	if cerr != nil {
		return 0, false
	}
	return n, true
}

// Snapshot reads the restart counter for every unit in one pass, so the caller
// can sleep once and re-read instead of sleeping per unit.
func Snapshot(units []string) map[string]int {
	out := make(map[string]int, len(units))
	for _, u := range units {
		if n, ok := NRestarts(u); ok {
			out[u] = n
		}
	}
	return out
}

// RestartLoop compares two counter snapshots taken window apart. A rising
// counter means the unit is restarting right now, whatever the historical
// total says — this is the check that catches a stale unit left enabled after
// a rename, the way seogladys.service sat in a loop for a month unnoticed.
func RestartLoop(unit string, before, after map[string]int) Result {
	name := "restarts " + unit
	b, hadBefore := before[unit]
	a, hadAfter := after[unit]
	if !hadBefore || !hadAfter {
		return Skip(name, "no restart counter (timers and sockets have none)")
	}
	delta := a - b
	switch {
	case delta > 0:
		level := Warn(name, "%d restarts in the sampling window — идёт петля рестартов (всего %d с начала жизни юнита)", delta, a)
		if delta >= 5 {
			level.OK = false
		}
		return level
	default:
		return Pass(name, "стабилен, всего %d рестартов за всю жизнь юнита", a)
	}
}

// Port checks a local TCP port accepts a connection.
func Port(p int) Result {
	name := fmt.Sprintf("port %d", p)
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p), 5*time.Second)
	if err != nil {
		return Fail(name, "127.0.0.1:%d not accepting: %v", p, err)
	}
	conn.Close()
	return Pass(name, "accepting")
}

// DiskFree reports free space on the filesystem holding path.
func DiskFree(path string) Result {
	free, total, err := diskSpace(path)
	if err != nil {
		if errors.Is(err, errNoStatfs) {
			return Skip("disk "+path, "free space not queryable on this platform")
		}
		return Warn("disk "+path, "cannot stat filesystem: %v", err)
	}
	pct := float64(total-free) / float64(total) * 100
	switch {
	case pct >= 95:
		return Fail("disk "+path, "%.1f%% used, %s free of %s", pct, human(free), human(total))
	case pct >= 85:
		return Warn("disk "+path, "%.1f%% used, %s free of %s", pct, human(free), human(total))
	default:
		return Pass("disk "+path, "%.1f%% used, %s free of %s", pct, human(free), human(total))
	}
}

// RcloneAbout reads a backend's live quota through `rclone about`.
func RcloneAbout(remote string) Result {
	name := "balance " + remote
	cmd := exec.Command("rclone", "about", remote, "--json")
	out, err := cmd.Output()
	if err != nil {
		if missingTool(err) {
			return Skip(name, "rclone not present on this host")
		}
		return Fail(name, "rclone about %s: %v", remote, err)
	}
	var r struct {
		Total *float64 `json:"total"`
		Used  *float64 `json:"used"`
		Trash *float64 `json:"trash"`
		Other *float64 `json:"other"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return Fail(name, "cannot parse rclone output: %v", err)
	}
	if r.Total == nil || r.Used == nil {
		return Warn(name, "rclone reported no quota for %s", remote)
	}
	used, total := *r.Used, *r.Total
	if total <= 0 {
		return Warn(name, "quota unknown for %s", remote)
	}
	pct := used / total * 100
	extra := ""
	if r.Trash != nil && *r.Trash > 0 {
		extra = fmt.Sprintf(", trash %s (rclone cleanup %s frees it)", human(int64(*r.Trash)), remote)
	}
	switch {
	case pct >= 95:
		return Fail(name, "%s: %.1f%% used, %s of %s free%s", remote, pct, human(int64(total-used)), human(int64(total)), extra)
	case pct >= 85:
		return Warn(name, "%s: %.1f%% used, %s of %s free%s", remote, pct, human(int64(total-used)), human(int64(total)), extra)
	default:
		return Pass(name, "%s: %.1f%% used, %s of %s free%s", remote, pct, human(int64(total-used)), human(int64(total)), extra)
	}
}

// IsServer reports whether this host is the machine the registry describes.
//
// The registry lists systemd units, local ports and the local filesystem, all
// of which only make sense on the server itself. Running the same config from
// a laptop would report a perfectly healthy VPS as a total outage, so the
// host-local checks are gated on this instead of producing false alarms.
func IsServer() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

// ServiceVersion returns a short version banner for the report footer.
func ServiceVersion(binary string, args ...string) string {
	out, err := exec.Command(binary, args...).Output()
	if err != nil {
		return ""
	}
	lines := bufio.NewScanner(strings.NewReader(string(out)))
	for lines.Scan() {
		if l := strings.TrimSpace(lines.Text()); l != "" {
			return l
		}
	}
	return ""
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}