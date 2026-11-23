// Package calendar implements ports.MarketCalendar for the London Stock Exchange and the New
// York markets (NYSE and NASDAQ share hours and holidays). Daylight saving comes from the IANA
// time zone database, embedded in the binary (time/tzdata) so it works in a distroless image
// and on Windows. Holidays and half days are tables per year; a year without a table is
// closed (fail closed) and reported with an error, so it must be added before trading.
//
// Tables verified 2026-11-23 against the exchanges' own calendars:
//   - NYSE: https://www.nyse.com/markets/hours-calendars (2026–2027; NASDAQ 2026 matches
//     https://www.nasdaq.com/market-activity/stock-market-holiday-schedule).
//   - LSE: https://www.londonstockexchange.com/equities-trading/business-days, the Millennium
//     Exchange business parameters ("Trading Cycles"), and gov.uk bank holidays (England and
//     Wales) for 2026 dates the exchange no longer lists.
package calendar

import (
	"fmt"
	"slices"
	"time"
	_ "time/tzdata" // embed the zone database: correct DST on any host

	"github.com/albievan/cactustrade/internal/domain"
)

// hm is a wall-clock time of day.
type hm struct{ h, m int }

type market struct {
	loc        *time.Location
	open       hm
	close      hm
	earlyClose hm
	// years holds, per supported year, the closed days and the early-close days (MM-DD).
	years map[int]yearTable
}

type yearTable struct {
	closed []string // full-day closures
	early  []string // early closes at market.earlyClose
}

// Calendar answers when a market trades.
type Calendar struct {
	markets map[domain.Market]market
}

// New loads the time zones and the tables.
func New() (*Calendar, error) {
	lon, err := time.LoadLocation("Europe/London")
	if err != nil {
		return nil, fmt.Errorf("calendar: %w", err)
	}
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil, fmt.Errorf("calendar: %w", err)
	}
	return &Calendar{markets: map[domain.Market]market{
		// Continuous trading 08:00–16:30 London (the closing auction runs to ~16:35; we do
		// not trade in auctions). Half days: continuous trading to 12:30.
		domain.MarketLSE: {
			loc: lon, open: hm{8, 0}, close: hm{16, 30}, earlyClose: hm{12, 30},
			years: map[int]yearTable{
				2026: {
					closed: []string{"01-01", "04-03", "04-06", "05-04", "05-25", "08-31", "12-25", "12-28"},
					early:  []string{"12-24", "12-31"},
				},
				2027: {
					closed: []string{"01-01", "03-26", "03-29", "05-03", "05-31", "08-30", "12-27", "12-28"},
					early:  []string{"12-24", "12-31"},
				},
			},
		},
		// Core session 09:30–16:00 New York; early closes at 13:00.
		domain.MarketNYSE: {
			loc: ny, open: hm{9, 30}, close: hm{16, 0}, earlyClose: hm{13, 0},
			years: map[int]yearTable{
				2026: {
					closed: []string{"01-01", "01-19", "02-16", "04-03", "05-25", "06-19", "07-03", "09-07", "11-26", "12-25"},
					early:  []string{"11-27", "12-24"},
				},
				2027: {
					closed: []string{"01-01", "01-18", "02-15", "03-26", "05-31", "06-18", "07-05", "09-06", "11-25", "12-24"},
					early:  []string{"11-26"},
				},
			},
		},
	}}, nil
}

// Session returns the open and close of market m on its local trading day containing at,
// and whether it trades that day. Weekends and holidays return trading=false with no error.
// An unknown market or a year without a table returns trading=false and an error.
func (c *Calendar) Session(m domain.Market, at time.Time) (open, closeAt time.Time, trading bool, err error) {
	mk, ok := c.markets[m]
	if !ok {
		return time.Time{}, time.Time{}, false, fmt.Errorf("calendar: unknown market %q: %w", m, domain.ErrInvalid)
	}
	local := at.In(mk.loc)
	y, mo, d := local.Date()
	yt, ok := mk.years[y]
	if !ok {
		return time.Time{}, time.Time{}, false, fmt.Errorf("calendar: no %s holiday table for %d: %w", m, y, domain.ErrUnavailable)
	}
	if wd := local.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return time.Time{}, time.Time{}, false, nil
	}
	day := fmt.Sprintf("%02d-%02d", int(mo), d)
	if slices.Contains(yt.closed, day) {
		return time.Time{}, time.Time{}, false, nil
	}
	cl := mk.close
	if slices.Contains(yt.early, day) {
		cl = mk.earlyClose
	}
	open = time.Date(y, mo, d, mk.open.h, mk.open.m, 0, 0, mk.loc)
	closeAt = time.Date(y, mo, d, cl.h, cl.m, 0, 0, mk.loc)
	return open, closeAt, true, nil
}

// IsOpen reports whether market m is in its trading session at the instant at. Any error
// (unknown year or market) is closed.
func (c *Calendar) IsOpen(m domain.Market, at time.Time) bool {
	open, closeAt, trading, err := c.Session(m, at)
	return err == nil && trading && !at.Before(open) && at.Before(closeAt)
}

// Years lists the years with tables for market m (for the UI and a start-up warning).
func (c *Calendar) Years(m domain.Market) []int {
	var out []int
	for y := range c.markets[m].years {
		out = append(out, y)
	}
	slices.Sort(out)
	return out
}
