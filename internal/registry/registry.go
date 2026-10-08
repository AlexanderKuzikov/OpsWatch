// Package registry holds the single source of truth for what OpsWatch
// watches: recurring payments, domains, endpoints, TLS certs, balances and
// host-level checks. It is plain JSON on disk so the binary stays
// dependency-free.
package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Period is the billing cycle of a payment.
type Period string

const (
	Monthly   Period = "monthly"
	Quarterly Period = "quarterly"
	Yearly    Period = "yearly"
	OneOff    Period = "oneoff"
	Usage     Period = "usage" // no fixed charge, metered
)

// Payment is one thing the user pays for on a schedule. There is no API for
// "when will I be charged", so this is hand-maintained on purpose: OpsWatch
// computes the forecast from anchor + period and warns ahead of the charge.
type Payment struct {
	ID        string  `json:"id"`
	Provider  string  `json:"provider"`
	Name      string  `json:"name"`
	Amount    float64 `json:"amount"`
	Currency  string  `json:"currency"`
	Period    Period  `json:"period"`
	Anchor    string  `json:"anchor"` // YYYY-MM-DD of a known charge
	Method    string  `json:"method"` // card / sbp / invoice / balance
	AutoRenew bool    `json:"autoRenew"`
	BalanceID string  `json:"balanceId,omitempty"`
	Notes     string  `json:"notes,omitempty"`
	Active    bool    `json:"active"`
}

// Domain is a name whose renewal or resolution must be watched.
type Domain struct {
	Host      string `json:"host"`
	Renewal   string `json:"renewal,omitempty"` // YYYY-MM-DD
	Registrar string `json:"registrar,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

// Endpoint is an HTTP probe.
type Endpoint struct {
	Name           string            `json:"name"`
	URL            string            `json:"url"`
	ExpectStatus   int               `json:"expectStatus"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	BodyContains   string            `json:"bodyContains,omitempty"`
}

// TLSCheck is a certificate expiry probe.
type TLSCheck struct {
	Host          string `json:"host"`
	WarnBeforeDay int    `json:"warnBeforeDays,omitempty"`
}

// Balance is a quota check. Three flavours: hand-kept (Known + LastUpdate),
// rclone backends (ViaRclone) and live HTTP providers (Via + AuthEnvVar).
// The secret itself is never in the registry — only the env var name.
type Balance struct {
	ID             string  `json:"id"`
	Provider       string  `json:"provider"`
	Currency       string  `json:"currency"`
	Known          float64 `json:"known"`
	WarnBelow      float64 `json:"warnBelow"`
	ViaRclone      string  `json:"viaRclone,omitempty"` // remote name, e.g. mailru:
	Via            string  `json:"via,omitempty"`       // manual | openrouter | routerai | selectel
	URL            string  `json:"url,omitempty"`       // override the default endpoint
	AuthEnvVar     string  `json:"authEnvVar,omitempty"`
	AuthHeader     string  `json:"authHeader,omitempty"` // default Authorization (Selectel: X-Token)
	AuthScheme     string  `json:"authScheme,omitempty"` // default Bearer; "" = raw token
	PredictionURL  string  `json:"predictionUrl,omitempty"`
	LastUpdate     string  `json:"lastUpdate,omitempty"`
	StaleAfterDays int     `json:"staleAfterDays,omitempty"` // default 30 for manual
	Notes          string  `json:"notes,omitempty"`
}

// UnitCheck is a systemd unit that must be active.
type UnitCheck struct {
	Unit string `json:"unit"`
	Why  string `json:"why,omitempty"`
}

// PortCheck is a local TCP port that must accept connections.
type PortCheck struct {
	Port int    `json:"port"`
	Why  string `json:"why,omitempty"`
}

// Registry is the whole config.
type Registry struct {
	Payments     []Payment   `json:"payments"`
	Domains      []Domain    `json:"domains"`
	Endpoints    []Endpoint  `json:"endpoints"`
	TLS          []TLSCheck  `json:"tls"`
	Balances     []Balance   `json:"balances"`
	Units        []UnitCheck `json:"units"`
	Ports        []PortCheck `json:"ports"`
	WarnChargeIn int        `json:"warnChargeInDays,omitempty"`
	Email        EmailCfg    `json:"email"`
}

// EmailCfg holds SMTP settings. Password comes from an env var name, never a
// literal, so the registry stays safe to commit.
type EmailCfg struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	From       string `json:"from"`
	To         string `json:"to"`
	PassEnvVar string `json:"passEnvVar"`
	UseTLS     bool   `json:"useTLS"`
}

// Load reads and validates the registry.
func Load(path string) (*Registry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Registry
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

// Validate rejects configs that would silently produce a wrong report.
func (r *Registry) Validate() error {
	seen := map[string]bool{}
	for _, p := range r.Payments {
		if p.ID == "" {
			return fmt.Errorf("payment without id: %+v", p)
		}
		if seen[p.ID] {
			return fmt.Errorf("duplicate payment id %q", p.ID)
		}
		seen[p.ID] = true
		switch p.Period {
		case Monthly, Quarterly, Yearly, OneOff, Usage:
		default:
			return fmt.Errorf("payment %s: unknown period %q", p.ID, p.Period)
		}
		if p.Period != Usage && p.Period != OneOff {
			if _, err := time.Parse("2006-01-02", p.Anchor); err != nil {
				return fmt.Errorf("payment %s: bad anchor %q (want YYYY-MM-DD)", p.ID, p.Anchor)
			}
		}
		if p.Amount < 0 {
			return fmt.Errorf("payment %s: negative amount", p.ID)
		}
	}
	for _, e := range r.Endpoints {
		if e.Name == "" || e.URL == "" {
			return fmt.Errorf("endpoint needs name and url: %+v", e)
		}
		if e.ExpectStatus == 0 {
			return fmt.Errorf("endpoint %s: expectStatus not set", e.Name)
		}
	}
	for _, b := range r.Balances {
		if b.ID == "" {
			return fmt.Errorf("balance without id: %+v", b)
		}
		switch b.Via {
		case "", "manual", "openrouter", "routerai", "selectel":
		default:
			return fmt.Errorf("balance %s: unknown via %q", b.ID, b.Via)
		}
		if b.Via == "openrouter" || b.Via == "routerai" || b.Via == "selectel" {
			if b.AuthEnvVar == "" {
				return fmt.Errorf("balance %s: via %q needs authEnvVar (env var name, not the secret)", b.ID, b.Via)
			}
		}
	}
	for _, t := range r.TLS {
		if t.Host == "" {
			return fmt.Errorf("tls check without host: %+v", t)
		}
		if t.WarnBeforeDay == 0 {
			return fmt.Errorf("tls %s: warnBeforeDays not set", t.Host)
		}
	}
	return nil
}

// ChargeWarningWindow returns how many days ahead to warn, with a sane default.
func (r *Registry) ChargeWarningWindow() int {
	if r.WarnChargeIn > 0 {
		return r.WarnChargeIn
	}
	return 7
}

// Payment returns the payment with the given id.
func (p Payment) Ref() *Payment { return &p }

// DueBefore returns payments with an active schedule whose next charge falls
// inside the window.
func (r *Registry) DueBefore(now time.Time, days int) []Charge {
	var out []Charge
	for i := range r.Payments {
		p := &r.Payments[i]
		if !p.Active || p.Period == Usage {
			continue
		}
		next, ok := p.NextCharge(now)
		if !ok {
			continue
		}
		if next.Before(now.AddDate(0, 0, days+1)) {
			out = append(out, Charge{Payment: *p, Date: next, InDays: daysBetween(now, next)})
		}
	}
	sortCharges(out)
	return out
}

// Cost is a per-currency normalised cost. Adding roubles to dollars produces a
// number that means nothing, so currencies are always kept apart.
type Cost struct {
	Monthly map[string]float64
	Yearly  map[string]float64
}

// SortedCurrencies gives a stable order for printing.
func (c Cost) SortedCurrencies() []string {
	out := make([]string, 0, len(c.Monthly))
	for k := range c.Monthly {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// MonthlyCost is the normalised per-month cost of every active fixed payment,
// grouped by currency. Yearly is divided by 12, quarterly by 3. Usage and
// OneOff are excluded because they have no steady rate.
func (r *Registry) MonthlyCost() Cost {
	c := Cost{Monthly: map[string]float64{}, Yearly: map[string]float64{}}
	for i := range r.Payments {
		p := &r.Payments[i]
		if !p.Active || p.Period == Usage || p.Period == OneOff {
			continue
		}
		cur := p.Currency
		if cur == "" {
			cur = "?"
		}
		var monthly float64
		switch p.Period {
		case Monthly:
			monthly = p.Amount
		case Quarterly:
			monthly = p.Amount / 3
		case Yearly:
			monthly = p.Amount / 12
		}
		c.Monthly[cur] += monthly
		c.Yearly[cur] += monthly * 12
	}
	return c
}

func daysBetween(from, to time.Time) int {
	a := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	b := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	return int(b.Sub(a).Hours() / 24)
}

func sortCharges(c []Charge) {
	for i := 0; i < len(c); i++ {
		for j := i + 1; j < len(c); j++ {
			if c[j].Date.Before(c[i].Date) {
				c[i], c[j] = c[j], c[i]
			}
		}
	}
}