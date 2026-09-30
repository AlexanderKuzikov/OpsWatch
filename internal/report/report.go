// Package report renders probe results and the billing forecast as markdown,
// and decides whether anything deserves an email.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AlexanderKuzikov/OpsWatch/internal/probe"
	"github.com/AlexanderKuzikov/OpsWatch/internal/registry"
)

// Report is everything one run produced.
type Report struct {
	GeneratedAt time.Time
	Host        string
	Results     []probe.Result
	Charges     []registry.Charge
	Cost        registry.Cost
	Footer      string
}

// Counts of failing, warning and skipped checks.
func (r *Report) Counts() (fails, warns, skips int) {
	for _, x := range r.Results {
		switch {
		case x.Skipped:
			skips++
		case !x.OK:
			fails++
		case x.Warn:
			warns++
		}
	}
	return
}

// Worst is the highest severity present: "FAIL", "WARN" or "OK".
func (r *Report) Worst() string {
	f, w, _ := r.Counts()
	switch {
	case f > 0:
		return "FAIL"
	case w > 0:
		return "WARN"
	default:
		return "OK"
	}
}

// Failed returns only the failing checks.
func (r *Report) Failed() []probe.Result {
	var out []probe.Result
	for _, x := range r.Results {
		if !x.OK && !x.Skipped {
			out = append(out, x)
		}
	}
	return out
}

// Warnings returns only the warning checks, skipping the not-applicable ones.
func (r *Report) Warnings() []probe.Result {
	var out []probe.Result
	for _, x := range r.Results {
		if x.OK && x.Warn && !x.Skipped {
			out = append(out, x)
		}
	}
	return out
}

// Markdown renders the full report.
func (r *Report) Markdown() string {
	var b strings.Builder
	fails, warns, skips := r.Counts()

	fmt.Fprintf(&b, "# OpsWatch %s — %s\n\n", r.GeneratedAt.Format("2006-01-02 15:04 MST"), r.Host)
	fmt.Fprintf(&b, "**Итог: %s** — проверок %d, ошибок %d, предупреждений %d, неприменимо %d\n\n",
		r.Worst(), len(r.Results), fails, warns, skips)

	if len(r.Charges) > 0 {
		b.WriteString("## Ближайшие списания\n\n")
		b.WriteString("| Через | Дата | Сумма | Что |\n|---|---|---|---|\n")
		for _, c := range r.Charges {
			fmt.Fprintf(&b, "| %s | %s | %.0f %s | %s — %s |\n",
				humanDays(c.InDays), c.Date.Format("2006-01-02"),
				c.Payment.Amount, c.Payment.Currency,
				c.Payment.Provider, c.Payment.Name)
		}
		b.WriteString("\n")
	} else {
		b.WriteString("## Ближайшие списания\n\nНичего в заданном окне.\n\n")
	}

	b.WriteString("## Расходы\n\n")
	for _, cur := range r.Cost.SortedCurrencies() {
		fmt.Fprintf(&b, "- **%.0f %s/мес**, %.0f %s/год\n", r.Cost.Monthly[cur], cur, r.Cost.Yearly[cur], cur)
	}
	b.WriteString("\n")
	if len(r.Cost.Monthly) > 1 {
		b.WriteString("Валюты разведены намеренно: складывать рубли с долларами бессмысленно.\n\n")
	}

	b.WriteString("## Проверки\n\n| | Что | Подробности |\n|---|---|---|\n")
	sorted := make([]probe.Result, len(r.Results))
	copy(sorted, r.Results)
	sort.SliceStable(sorted, func(i, j int) bool {
		return rank(sorted[i]) < rank(sorted[j])
	})
	for _, x := range sorted {
		icon := "ok"
		switch {
		case x.Skipped:
			icon = "— skip"
		case !x.OK:
			icon = "**FAIL**"
		case x.Warn:
			icon = "warn"
		}
		fmt.Fprintf(&b, "| %s | `%s` | %s |\n", icon, x.Name, x.Detail)
	}
	b.WriteString("\n")
	if r.Footer != "" {
		fmt.Fprintf(&b, "---\n%s\n", r.Footer)
	}
	return b.String()
}

func rank(r probe.Result) int {
	switch {
	case !r.OK && !r.Skipped:
		return 0
	case r.Warn && !r.Skipped:
		return 1
	case r.Skipped:
		return 2
	default:
		return 3
	}
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

// Subject is the one-line email subject, severity first.
func (r *Report) Subject() string {
	f, w, _ := r.Counts()
	switch {
	case f > 0:
		return fmt.Sprintf("OpsWatch FAIL: %d ошибок, %d предупреждений — %s", f, w, r.Host)
	case w > 0:
		return fmt.Sprintf("OpsWatch WARN: %d предупреждений — %s", w, r.Host)
	default:
		return fmt.Sprintf("OpsWatch OK: всё здорово — %s", r.Host)
	}
}

// AlertBody is the email body. It leads with what is broken so the first
// screen already answers "what do I do".
func (r *Report) AlertBody() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Сгенерировано: %s\nХост: %s\n\n", r.GeneratedAt.Format("2006-01-02 15:04:05 MST"), r.Host)

	if failed := r.Failed(); len(failed) > 0 {
		fmt.Fprintf(&b, "СЛОМАНО (%d):\n", len(failed))
		for _, x := range failed {
			fmt.Fprintf(&b, "  - %s: %s\n", x.Name, x.Detail)
		}
		b.WriteString("\n")
	}
	if warns := r.Warnings(); len(warns) > 0 {
		fmt.Fprintf(&b, "ВНИМАНИЕ (%d):\n", len(warns))
		for _, x := range warns {
			fmt.Fprintf(&b, "  - %s: %s\n", x.Name, x.Detail)
		}
		b.WriteString("\n")
	}
	if len(r.Charges) > 0 {
		fmt.Fprintf(&b, "БЛИЖАЙШИЕ СПИСАНИЯ (окно %s):\n", r.Charges[0].Date.Format("2006-01-02"))
		for _, c := range r.Charges {
			fmt.Fprintf(&b, "  - %s | %s %.0f %s | %s — %s\n",
				humanDays(c.InDays), c.Date.Format("2006-01-02"),
				c.Payment.Amount, c.Payment.Currency, c.Payment.Provider, c.Payment.Name)
		}
		b.WriteString("\n")
	}
	b.WriteString("Расходы (фиксированные):\n")
	for _, cur := range r.Cost.SortedCurrencies() {
		fmt.Fprintf(&b, "  %.0f %s/мес, %.0f %s/год\n", r.Cost.Monthly[cur], cur, r.Cost.Yearly[cur], cur)
	}
	if len(r.Failed()) == 0 && len(r.Warnings()) == 0 {
		b.WriteString("\nВсе применимые проверки зелёные.\n")
	}
	return b.String()
}