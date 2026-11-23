package calendar

import (
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// entryAt is an entry that only the clock can refuse: everything else is fresh and valid.
func entryAt(now, open, closeAt time.Time) domain.RiskInput {
	l := domain.DefaultRiskLimits
	l.TradingBudget = 500_000
	return domain.RiskInput{
		Now: now, Mode: domain.ModeDemo, SessionOpen: open, SessionClose: closeAt, Trading: true,
		Portfolio:  domain.Portfolio{AvailableCash: 1_000_000, AsOf: now},
		Instrument: 1, Direction: domain.Long,
		Eligibility: domain.Eligibility{Instrument: 1, RealLong: true, CheckedAt: now.Add(-time.Hour)},
		Entry:       100_000_000, QuoteAt: now, StopLoss: 98_000_000, Requested: 10_000, Limits: l,
		State:      domain.RiskState{Day: domain.RiskDay(now), StartOfDayEquity: 1_000_000, HighWaterMark: 1_000_000},
		Reconciled: true,
	}
}

// TestFlattenAndRiskDayAcrossTheCalendar cross-checks the calendar with the risk rules for
// every 2026–2027 trading day of LSE, NYSE and ASX, half days and DST changes included:
// flatten is due at close−10 min and not at close−11 min, entries are refused from
// close−30 min (and allowed a minute earlier), and no session contains the 22:00 UTC start of
// a risk day.
func TestFlattenAndRiskDayAcrossTheCalendar(t *testing.T) {
	c := mustNew(t)
	l := domain.DefaultRiskLimits
	for _, m := range domain.Markets {
		mk := c.markets[m]
		sessions, early := 0, 0
		offsets := map[int]bool{}
		for d := time.Date(2026, 1, 1, 12, 0, 0, 0, mk.loc); d.Year() < 2028; d = d.AddDate(0, 0, 1) {
			open, closeAt, trading, err := c.Session(m, d)
			if err != nil {
				t.Fatalf("%s %s: %v", m, d.Format(time.DateOnly), err)
			}
			if !trading {
				continue
			}
			sessions++
			if lc := closeAt.In(mk.loc); lc.Hour() != mk.close.h || lc.Minute() != mk.close.m {
				early++
			}
			_, off := closeAt.Zone()
			offsets[off] = true
			day := d.Format(time.DateOnly)

			// The supervisor asks the calendar at the moment itself: it must see the same session.
			for _, before := range []time.Duration{10 * time.Minute, 11 * time.Minute, 30 * time.Minute, 31 * time.Minute} {
				at := closeAt.Add(-before)
				o2, c2, tr2, err := c.Session(m, at)
				if err != nil || !tr2 || !o2.Equal(open) || !c2.Equal(closeAt) {
					t.Fatalf("%s %s: the session seen at close−%s differs", m, day, before)
				}
			}
			if !domain.FlattenDue(closeAt.Add(-10*time.Minute), closeAt, true, l) {
				t.Errorf("%s %s: flatten not due at close−10 min (%s)", m, day, closeAt.UTC())
			}
			if domain.FlattenDue(closeAt.Add(-11*time.Minute), closeAt, true, l) {
				t.Errorf("%s %s: flatten due at close−11 min (%s)", m, day, closeAt.UTC())
			}
			pos := []domain.Position{{ID: 1, Instrument: 1, OpenRate: 100_000_000, StopLossRate: 98_000_000, RealAsset: true, Long: true, Leverage: 1}}
			sess := map[domain.InstrumentID]domain.MarketSession{1: {Known: true, Trading: true, Open: open, Close: closeAt}}
			if len(domain.ExitsDue(pos, sess, closeAt.Add(-10*time.Minute), l)) != 1 || len(domain.ExitsDue(pos, sess, closeAt.Add(-11*time.Minute), l)) != 0 {
				t.Errorf("%s %s: ExitsDue disagrees with FlattenDue", m, day)
			}

			at30 := closeAt.Add(-30 * time.Minute)
			if dec := domain.AssessEntry(entryAt(at30, open, closeAt)); dec.Allowed || !strings.Contains(dec.Reason, "too close to the close") {
				t.Errorf("%s %s: entry at close−30 min: %+v", m, day, dec)
			}
			if dec := domain.AssessEntry(entryAt(at30.Add(-time.Minute), open, closeAt)); !dec.Allowed {
				t.Errorf("%s %s: entry at close−31 min refused: %s", m, day, dec.Reason)
			}

			// The whole session lies inside one risk day: the 22:00 UTC boundary never falls
			// inside [open, close).
			if domain.RiskDay(open) != domain.RiskDay(closeAt.Add(-time.Nanosecond)) {
				t.Errorf("%s %s: session %s–%s spans two risk days", m, day, open.UTC(), closeAt.UTC())
			}
			for _, base := range []time.Time{open.UTC().AddDate(0, 0, -1), open.UTC()} {
				b := time.Date(base.Year(), base.Month(), base.Day(), domain.RiskDayStartHourUTC, 0, 0, 0, time.UTC)
				if !b.Before(open) && b.Before(closeAt) {
					t.Errorf("%s %s: session %s–%s contains the risk-day boundary %s", m, day, open.UTC(), closeAt.UTC(), b)
				}
			}
		}
		// Guard against a loop that silently checks nothing: about 250 sessions a year, the
		// half days in the tables, and both sides of daylight saving.
		if sessions < 490 || early < 3 || len(offsets) != 2 {
			t.Fatalf("%s: %d sessions, %d half days, %d UTC offsets checked", m, sessions, early, len(offsets))
		}
	}
}
