// Command opswatch reports on recurring payments, server health, certificates,
// quotas and running services, and emails the owner when something needs
// attention.
//
// It is designed to run on the machine it watches, so a separate heartbeat
// subcommand mails the owner once a day: no mail means the machine died.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AlexanderKuzikov/OpsWatch/internal/alert"
	"github.com/AlexanderKuzikov/OpsWatch/internal/probe"
	"github.com/AlexanderKuzikov/OpsWatch/internal/registry"
	"github.com/AlexanderKuzikov/OpsWatch/internal/report"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "report":
		err = cmdReport(os.Args[2:])
	case "heartbeat":
		err = cmdHeartbeat(os.Args[2:])
	case "validate":
		err = cmdValidate(os.Args[2:])
	case "set-balance":
		err = cmdSetBalance(os.Args[2:])
	case "version":
		fmt.Println("opswatch", version)
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

const version = "0.1.0"

func usage() {
	fmt.Fprint(os.Stderr, `opswatch - payments, server and certificate watchdog

   opswatch report    [-registry f] [-format markdown|html] [-out f] [-mail always|never|problems] [-quiet]
   opswatch heartbeat [-registry f]
   opswatch validate  [-registry f]
   opswatch set-balance -id balance-id -value 1234 [-registry f] [-date YYYY-MM-DD]
   opswatch version

report     runs every check, prints markdown, optionally writes it to a file
           and mails it. Exit code is 1 when any check FAILs, so cron and
           systemd notice on their own.
heartbeat  mails a plain "still alive" note. Schedule it daily: if the mail
            stops arriving, the machine is gone — which no self-hosted check
            can ever tell you.
validate   parses the registry and reports problems without probing anything.
set-balance
            stamps a hand-kept balance (Known + today as LastUpdate) so a
            manual figure costs one command instead of hand-editing JSON.
            The registry stays the user's data: this only writes what you
            tell it.
`)
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	path := fs.String("registry", defaultRegistry(), "registry file")
	fs.Parse(args)
	reg, err := registry.Load(*path)
	if err != nil {
		return err
	}
	cost := reg.MonthlyCost()
	fmt.Printf("registry ok: %d платежей, %d эндпоинтов, %d сертификатов, %d юнитов\n",
		len(reg.Payments), len(reg.Endpoints), len(reg.TLS), len(reg.Units))
	for _, cur := range cost.SortedCurrencies() {
		fmt.Printf("расходы: %.0f %s/мес, %.0f %s/год\n", cost.Monthly[cur], cur, cost.Yearly[cur], cur)
	}
	return nil
}

func cmdSetBalance(args []string) error {
	fs := flag.NewFlagSet("set-balance", flag.ExitOnError)
	path := fs.String("registry", defaultRegistry(), "registry file")
	id := fs.String("id", "", "balance id")
	value := fs.String("value", "", "new known figure")
	date := fs.String("date", "", "update date YYYY-MM-DD (default today)")
	fs.Parse(args)

	if *id == "" || *value == "" {
		return fmt.Errorf("-id and -value are required")
	}
	var v float64
	if _, err := fmt.Sscanf(*value, "%f", &v); err != nil || v < 0 {
		return fmt.Errorf("-value must be a non-negative number, got %q", *value)
	}
	day := *date
	if day == "" {
		day = time.Now().Format("2006-01-02")
	} else if _, err := time.Parse("2006-01-02", day); err != nil {
		return fmt.Errorf("-date must be YYYY-MM-DD, got %q", day)
	}
	reg, err := registry.Load(*path)
	if err != nil {
		return err
	}
	found := false
	for i := range reg.Balances {
		if reg.Balances[i].ID == *id {
			reg.Balances[i].Known = v
			reg.Balances[i].LastUpdate = day
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no balance %q in %s", *id, *path)
	}
	out, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(*path, string(out)+"\n"); err != nil {
		return err
	}
	fmt.Printf("balance %s: %.2f, обновлено %s\n", *id, v, day)
	return nil
}

func cmdHeartbeat(args []string) error {
	fs := flag.NewFlagSet("heartbeat", flag.ExitOnError)
	path := fs.String("registry", defaultRegistry(), "registry file")
	fs.Parse(args)
	reg, err := registry.Load(*path)
	if err != nil {
		return err
	}
	sender := alert.New(reg.Email)
	if !sender.Configured() {
		return alert.ErrNoMailer
	}
	host, _ := os.Hostname()
	body := fmt.Sprintf("OpsWatch жив. Хост %s, время %s.\n\n"+
		"Это ежедневный пинг-сигнал. Если письмо не пришло — хост умер, а его\n"+
		"собственные проверки об этом сообщить не могли.\n",
		host, time.Now().Format("2006-01-02 15:04:05 MST"))
	if err := sender.Send(fmt.Sprintf("OpsWatch heartbeat: %s", host), body); err != nil {
		return err
	}
	fmt.Println("heartbeat sent to", reg.Email.To)
	return nil
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	path := fs.String("registry", defaultRegistry(), "registry file")
	out := fs.String("out", "", "write markdown report here")
	mail := fs.String("mail", "problems", "always | never | problems")
	quiet := fs.Bool("quiet", false, "print nothing to stdout")
	dataDir := fs.String("data-dir", "/", "filesystem to report free space for")
	restartWindow := fs.Duration("restart-window", 20*time.Second,
		"sampling window used to tell a live crash loop from a historical restart count")
	format := fs.String("format", "markdown", "markdown | html")
	fs.Parse(args)

	if *format != "markdown" && *format != "html" {
		return fmt.Errorf("-format must be markdown or html")
	}

	if *mail != "always" && *mail != "never" && *mail != "problems" {
		return fmt.Errorf("-mail must be always, never or problems")
	}
	reg, err := registry.Load(*path)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	rep := &report.Report{
		GeneratedAt: time.Now(),
		Host:        host,
		Charges:     reg.DueBefore(time.Now(), reg.ChargeWarningWindow()),
	}
	rep.Cost = reg.MonthlyCost()

	// Host-local checks only mean anything on the machine the registry
	// describes. From a laptop they would turn a healthy VPS into a wall of
	// false failures.
	if probe.IsServer() {
		rep.Results = append(rep.Results, probe.DiskFree(*dataDir))
		rep.Results = append(rep.Results, probe.Load())
		rep.Results = append(rep.Results, probe.Mem())
		for _, u := range reg.Units {
			rep.Results = append(rep.Results, probe.Unit(u.Unit))
		}
		// NRestarts is cumulative for the unit's lifetime, so a single reading
		// cannot tell a live crash loop from an old one. Sample twice.
		names := make([]string, 0, len(reg.Units))
		for _, u := range reg.Units {
			names = append(names, u.Unit)
		}
		before := probe.Snapshot(names)
		time.Sleep(*restartWindow)
		after := probe.Snapshot(names)
		for _, u := range reg.Units {
			rep.Results = append(rep.Results, probe.RestartLoop(u.Unit, before, after))
		}
		for _, p := range reg.Ports {
			rep.Results = append(rep.Results, probe.Port(p.Port))
		}
		for _, b := range reg.Balances {
			if b.ViaRclone != "" {
				if r, ok := probe.BalanceCheck(ctx, b); ok {
					rep.Results = append(rep.Results, r)
				}
			}
		}
	} else {
		rep.Results = append(rep.Results, probe.Skip("host-local checks",
			"%s is not the watched server (no systemd): юниты, порты и файловая система пропущены", host))
	}

	// Balances: rclone entries already ran on the server above; HTTP and
	// manual ones run everywhere, a laptop included.
	for _, b := range reg.Balances {
		if b.ViaRclone != "" {
			continue
		}
		if r, ok := probe.BalanceCheck(ctx, b); ok {
			rep.Results = append(rep.Results, r)
		}
	}
	for _, ep := range reg.Endpoints {
		rep.Results = append(rep.Results, probe.HTTP(ctx, ep))
	}
	for _, t := range reg.TLS {
		rep.Results = append(rep.Results, probe.TLSCert(t.Host, t.WarnBeforeDay))
	}
	for _, d := range reg.Domains {
		if d.Renewal == "" {
			continue
		}
		if days, ok := until(d.Renewal); ok && days <= reg.ChargeWarningWindow() {
			rep.Results = append(rep.Results, probe.Warn("domain "+d.Host,
				"продление %s — %s", d.Renewal, humanDays(days)))
		} else if ok {
			rep.Results = append(rep.Results, probe.Pass("domain "+d.Host,
				"продление %s — %s", d.Renewal, humanDays(days)))
		}
	}
	rep.Footer = strings.TrimSpace(probe.ServiceVersion("uname", "-srm") + " | " + os.Args[0])

	md := rep.Markdown()
	if *format == "html" {
		html, herr := rep.RenderHTML()
		if herr != nil {
			return fmt.Errorf("render html: %w", herr)
		}
		md = html
	}
	if *out != "" {
		if err := writeFile(*out, md); err != nil {
			return fmt.Errorf("write %s: %w", *out, err)
		}
	}
	if !*quiet {
		if *format == "html" {
			fmt.Println("HTML-отчёт:", *out)
			if *out == "" {
				fmt.Println(html2stdout(rep))
			}
		} else {
			fmt.Print(md)
		}
	}

	fails, warns, _ := rep.Counts()
	if *mail != "never" {
		sender := alert.New(reg.Email)
		want := *mail == "always" || (fails > 0 || warns > 0)
		if want && sender.Configured() {
			subject := rep.Subject()
			body := rep.AlertBody()
			if *mail == "problems" {
				// Plain markdown is easier to read in a mail client than the
				// full table dump, so problems get the short body.
				if err := sender.Send(subject, body); err != nil {
					return fmt.Errorf("send alert: %w", err)
				}
			} else {
				if err := sender.Send(subject, body+"\n\n"+md); err != nil {
					return fmt.Errorf("send report: %w", err)
				}
			}
		}
	}

	if fails > 0 {
		fmt.Fprintf(os.Stderr, "%d checks failed\n", fails)
		os.Exit(1)
	}
	return nil
}

func html2stdout(rep *report.Report) string {
	h, err := rep.RenderHTML()
	if err != nil {
		return "render html: " + err.Error()
	}
	return h
}

func until(date string) (int, bool) {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0, false
	}
	now := time.Now()
	a := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return int(t.Sub(a).Hours() / 24), true
}

func humanDays(d int) string {
	switch {
	case d < 0:
		return fmt.Sprintf("%d дн. назад", -d)
	case d == 0:
		return "сегодня"
	case d == 1:
		return "завтра"
	default:
		return fmt.Sprintf("через %d дн.", d)
	}
}

func writeFile(path, body string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func defaultRegistry() string {
	if p := os.Getenv("OPSWATCH_REGISTRY"); p != "" {
		return p
	}
	return "config/registry.json"
}
