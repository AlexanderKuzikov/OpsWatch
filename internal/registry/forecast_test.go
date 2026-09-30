package registry

import (
	"testing"
	"time"
)

func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// The classic billing bug: a charge anchored on the 31st must stay on the
// last day of short months, not roll forward into the next one.
func TestAddMonthsClampsToMonthEnd(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026-01-31", "2026-02-28"}, // 2026 is not a leap year
		{"2024-01-31", "2024-02-29"}, // leap
		{"2026-03-31", "2026-04-30"},
		{"2026-12-15", "2027-01-15"},
		{"2026-01-15", "2026-02-15"},
	}
	for _, c := range cases {
		got := addMonths(mustDate(c.in), 1)
		if got.Format("2006-01-02") != c.want {
			t.Errorf("addMonths(%s,1) = %s, want %s", c.in, got.Format("2006-01-02"), c.want)
		}
	}
}

func TestNextChargeMonthly(t *testing.T) {
	p := Payment{Period: Monthly, Anchor: "2026-08-20", Amount: 650, Active: true}
	// mid-month: next is the same day next month
	got, ok := p.NextCharge(mustDate("2026-09-30"))
	if !ok || got.Format("2006-01-02") != "2026-10-20" {
		t.Fatalf("got %s ok=%v, want 2026-10-20", got.Format("2006-01-02"), ok)
	}
	// exactly on the anchor day: must roll to the next one, not return today
	got, ok = p.NextCharge(mustDate("2026-08-20"))
	if !ok || got.Format("2006-01-02") != "2026-09-20" {
		t.Fatalf("same-day gave %s ok=%v, want 2026-09-20", got.Format("2006-01-02"), ok)
	}
}

func TestNextChargeMonthlyClampedDoesNotDrift(t *testing.T) {
	// anchored on the 31st: every month must be the last day, never Mar 2
	p := Payment{Period: Monthly, Anchor: "2026-01-31", Active: true}
	got := p.Upcoming(mustDate("2026-01-01"), 6)
	want := []string{"2026-01-31", "2026-02-28", "2026-03-31", "2026-04-30", "2026-05-31", "2026-06-30"}
	if len(got) != len(want) {
		t.Fatalf("got %d charges want %d", len(got), len(want))
	}
	for i, w := range want {
		if g := got[i].Format("2006-01-02"); g != w {
			t.Errorf("charge %d = %s, want %s", i, g, w)
		}
	}
}

func TestNextChargeYearlyLeapDay(t *testing.T) {
	p := Payment{Period: Yearly, Anchor: "2024-02-29", Active: true}
	got, ok := p.NextCharge(mustDate("2026-03-01"))
	if !ok || got.Format("2006-01-02") != "2027-02-28" {
		t.Fatalf("got %s ok=%v, want 2027-02-28", got.Format("2006-01-02"), ok)
	}
}

func TestNextChargeUsageHasNone(t *testing.T) {
	p := Payment{Period: Usage, Active: true}
	if _, ok := p.NextCharge(mustDate("2026-01-01")); ok {
		t.Fatal("usage-based payment must not report a charge date")
	}
}

func TestMonthlyCostKeepsCurrenciesApart(t *testing.T) {
	r := Registry{Payments: []Payment{
		{ID: "a", Amount: 650, Currency: "RUB", Period: Monthly, Anchor: "2026-01-01", Active: true},
		{ID: "b", Amount: 1200, Currency: "RUB", Period: Yearly, Anchor: "2026-01-01", Active: true},
		{ID: "c", Amount: 300, Currency: "RUB", Period: Quarterly, Anchor: "2026-01-01", Active: true},
		{ID: "d", Amount: 30, Currency: "USD", Period: Monthly, Anchor: "2026-01-01", Active: true},
		{ID: "e", Amount: 500, Currency: "RUB", Period: Usage, Active: true},          // excluded
		{ID: "f", Amount: 900, Currency: "RUB", Period: Monthly, Anchor: "2026-01-01"}, // inactive
	}}
	got := r.MonthlyCost()

	wantRUB := 650.0 + 1200.0/12 + 300.0/3
	if diff := got.Monthly["RUB"] - wantRUB; diff > 0.01 || diff < -0.01 {
		t.Fatalf("RUB monthly = %v, want %v", got.Monthly["RUB"], wantRUB)
	}
	if diff := got.Monthly["USD"] - 30; diff > 0.01 || diff < -0.01 {
		t.Fatalf("USD monthly = %v, want 30", got.Monthly["USD"])
	}
	if diff := got.Yearly["RUB"] - wantRUB*12; diff > 0.01 || diff < -0.01 {
		t.Fatalf("RUB yearly = %v, want %v", got.Yearly["RUB"], wantRUB*12)
	}
	// The whole point: a single blended number would be meaningless.
	if got.Monthly["USD"] == 0 {
		t.Fatal("USD entry lost")
	}
}

func TestDueBeforeWindow(t *testing.T) {
	r := Registry{Payments: []Payment{
		// anchor in the future: charges on 2026-10-02, 3 days out
		{ID: "soon", Provider: "P", Name: "soon", Amount: 100, Period: Monthly, Anchor: "2026-10-02", Active: true},
		// anchor in the past: next charge is 2026-10-28, outside a 7 day window
		{ID: "later", Provider: "P", Name: "later", Amount: 100, Period: Monthly, Anchor: "2026-09-28", Active: true},
		// metered: never reported as a charge
		{ID: "metered", Provider: "P", Name: "metered", Amount: 100, Period: Usage, Active: true},
	}}
	got := r.DueBefore(mustDate("2026-09-30"), 7)
	if len(got) != 1 {
		t.Fatalf("got %d charges want 1: %+v", len(got), got)
	}
	if got[0].Payment.ID != "soon" {
		t.Fatalf("wrong charge %s", got[0].Payment.ID)
	}
	if got[0].Date.Format("2006-01-02") != "2026-10-02" {
		t.Fatalf("date = %s, want 2026-10-02", got[0].Date.Format("2006-01-02"))
	}
	if got[0].InDays != 2 {
		t.Fatalf("InDays = %d, want 2", got[0].InDays)
	}
}

func TestValidateCatchesBadConfig(t *testing.T) {
	bad := []Registry{
		{Payments: []Payment{{ID: "x", Period: "weekly", Anchor: "2026-01-01"}}},         // bad period
		{Payments: []Payment{{ID: "", Period: Monthly, Anchor: "2026-01-01"}}},           // no id
		{Payments: []Payment{{ID: "a", Period: Monthly, Anchor: "01-01-2026"}}},          // bad date
		{Endpoints: []Endpoint{{Name: "e", URL: "http://x"}}},                            // no expectStatus
		{TLS: []TLSCheck{{Host: "h"}}},                                                   // no warnBeforeDays
		{Payments: []Payment{{ID: "a", Period: Monthly, Anchor: "2026-01-01", Active: true}, {ID: "a", Period: Monthly, Anchor: "2026-01-01"}}},
	}
	for i, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("case %d: expected validation error, got nil", i)
		}
	}
	good := Registry{
		Payments:  []Payment{{ID: "a", Amount: 1, Period: Monthly, Anchor: "2026-01-01", Active: true}},
		Endpoints: []Endpoint{{Name: "e", URL: "http://x", ExpectStatus: 200}},
		TLS:       []TLSCheck{{Host: "h", WarnBeforeDay: 14}},
	}
	if err := good.Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}