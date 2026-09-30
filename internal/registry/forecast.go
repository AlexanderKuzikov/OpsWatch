package registry

import (
	"time"
)

// Charge is an upcoming billing event.
type Charge struct {
	Payment Payment
	Date    time.Time
	InDays  int
}

// addMonths adds n months and clamps the day to the last day of the target
// month. Without the clamp, Jan 31 + 1 month lands on Mar 2/3 and a monthly
// subscription silently drifts a day every few months.
func addMonths(t time.Time, n int) time.Time {
	y := t.Year()
	m := int(t.Month()) + n
	for m > 12 {
		m -= 12
		y++
	}
	for m < 1 {
		m += 12
		y--
	}
	last := time.Date(y, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day()
	d := t.Day()
	if d > last {
		d = last
	}
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
}

func day(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// step returns the month interval for a period.
func step(p Period) int {
	switch p {
	case Monthly:
		return 1
	case Quarterly:
		return 3
	case Yearly:
		return 12
	}
	return 0
}

// occurrence computes the n-th charge straight from the anchor.
//
// It must NOT chain addMonths onto the previous result: a charge anchored on
// Jan 31 clamps to Feb 28, and adding a month to Feb 28 gives Mar 28 instead
// of Mar 31, so the schedule creeps forward every short month. Always
// recompute from the anchor and let the clamp apply once.
func (p *Payment) occurrence(n int) (time.Time, error) {
	a, err := time.Parse("2006-01-02", p.Anchor)
	if err != nil {
		return time.Time{}, err
	}
	return addMonths(day(a), step(p.Period)*n), nil
}

// NextCharge returns the next billing date strictly after now. ok is false
// when the schedule has no future charges (usage-based, or a one-off in the
// past).
func (p *Payment) NextCharge(now time.Time) (time.Time, bool) {
	today := day(now)

	// One-off: the anchor itself, only while it is still ahead of us.
	if step(p.Period) == 0 {
		if p.Period != OneOff {
			return time.Time{}, false // Usage: no fixed schedule
		}
		c, err := p.occurrence(0)
		if err != nil {
			return time.Time{}, false
		}
		if c.After(today) {
			return c, true
		}
		return time.Time{}, false
	}

	// Start near the anchor: how many whole intervals fit between anchor and
	// today, then walk a few candidates forward. addMonths is monotonic in n,
	// so this cannot skip a charge.
	anchor, err := p.occurrence(0)
	if err != nil {
		return time.Time{}, false
	}
	gap := (today.Year()-anchor.Year())*12 + int(today.Month()) - int(anchor.Month())
	if gap < 0 {
		gap = 0
	}
	k0 := gap / step(p.Period)
	for k := k0 - 1; k <= k0+3; k++ {
		if k < 0 {
			continue
		}
		c, err := p.occurrence(k)
		if err != nil {
			return time.Time{}, false
		}
		if c.After(today) {
			return c, true
		}
	}
	return time.Time{}, false
}

// Upcoming returns the next count charges starting from the first one after
// now. Used for the forecast table.
func (p *Payment) Upcoming(now time.Time, count int) []time.Time {
	first, ok := p.NextCharge(now)
	if !ok {
		return nil
	}
	// Recover the interval index of the first charge so the rest can be
	// computed from the anchor without drifting.
	n := step(p.Period)
	if n == 0 {
		return []time.Time{first}
	}
	anchor, err := p.occurrence(0)
	if err != nil {
		return nil
	}
	gap := (first.Year()-anchor.Year())*12 + int(first.Month()) - int(anchor.Month())
	k0 := gap / n

	out := []time.Time{first}
	for len(out) < count {
		c, err := p.occurrence(k0 + len(out))
		if err != nil {
			break
		}
		out = append(out, c)
	}
	return out
}