package calendar

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

func mustNew(t *testing.T) *Calendar {
	t.Helper()
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSessions(t *testing.T) {
	c := mustNew(t)
	tests := []struct {
		name      string
		m         domain.Market
		at        string // RFC 3339 instant
		trading   bool
		open, end string // expected UTC open/close
	}{
		// DST: London is UTC+0 in winter, UTC+1 in summer; New York UTC-5 / UTC-4.
		{"LSE winter", domain.MarketLSE, "2026-11-23T12:00:00Z", true, "2026-11-23T08:00:00Z", "2026-11-23T16:30:00Z"},
		{"LSE summer", domain.MarketLSE, "2026-07-15T12:00:00Z", true, "2026-07-15T07:00:00Z", "2026-07-15T15:30:00Z"},
		{"NYSE winter", domain.MarketNYSE, "2026-11-23T15:00:00Z", true, "2026-11-23T14:30:00Z", "2026-11-23T21:00:00Z"},
		{"NYSE summer", domain.MarketNYSE, "2026-07-15T15:00:00Z", true, "2026-07-15T13:30:00Z", "2026-07-15T20:00:00Z"},
		// The weeks when only one side has moved its clocks (US changes earlier in March).
		{"NYSE after US DST, before UK", domain.MarketNYSE, "2026-03-16T15:00:00Z", true, "2026-03-16T13:30:00Z", "2026-03-16T20:00:00Z"},
		{"LSE same week", domain.MarketLSE, "2026-03-16T12:00:00Z", true, "2026-03-16T08:00:00Z", "2026-03-16T16:30:00Z"},
		// Weekends and holidays.
		{"Saturday", domain.MarketLSE, "2026-11-21T12:00:00Z", false, "", ""},
		{"Sunday", domain.MarketNYSE, "2026-11-22T15:00:00Z", false, "", ""},
		{"Thanksgiving", domain.MarketNYSE, "2026-11-26T15:00:00Z", false, "", ""},
		{"Thanksgiving is not a UK holiday", domain.MarketLSE, "2026-11-26T12:00:00Z", true, "2026-11-26T08:00:00Z", "2026-11-26T16:30:00Z"},
		{"Boxing Day substitute", domain.MarketLSE, "2026-12-28T12:00:00Z", false, "", ""},
		{"Independence Day observed", domain.MarketNYSE, "2026-07-03T15:00:00Z", false, "", ""},
		{"Easter Monday 2027 (UK only)", domain.MarketLSE, "2027-03-29T12:00:00Z", false, "", ""},
		{"Easter Monday 2027 NYSE open", domain.MarketNYSE, "2027-03-29T15:00:00Z", true, "2027-03-29T13:30:00Z", "2027-03-29T20:00:00Z"},
		{"Christmas observed 2027", domain.MarketNYSE, "2027-12-24T15:00:00Z", false, "", ""},
		// Half days.
		{"LSE Christmas Eve", domain.MarketLSE, "2026-12-24T10:00:00Z", true, "2026-12-24T08:00:00Z", "2026-12-24T12:30:00Z"},
		{"NYSE day after Thanksgiving", domain.MarketNYSE, "2026-11-27T15:00:00Z", true, "2026-11-27T14:30:00Z", "2026-11-27T18:00:00Z"},
		{"NYSE 2027 day after Thanksgiving", domain.MarketNYSE, "2027-11-26T15:00:00Z", true, "2027-11-26T14:30:00Z", "2027-11-26T18:00:00Z"},
		// The local date decides: 03:00 UTC Tuesday is still Monday evening in New York.
		{"local date", domain.MarketNYSE, "2026-11-24T03:00:00Z", true, "2026-11-23T14:30:00Z", "2026-11-23T21:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			at, _ := time.Parse(time.RFC3339, tt.at)
			open, end, trading, err := c.Session(tt.m, at)
			if err != nil || trading != tt.trading {
				t.Fatalf("trading = %v, err = %v", trading, err)
			}
			if !tt.trading {
				return
			}
			if got := open.UTC().Format(time.RFC3339); got != tt.open {
				t.Errorf("open = %s, want %s", got, tt.open)
			}
			if got := end.UTC().Format(time.RFC3339); got != tt.end {
				t.Errorf("close = %s, want %s", got, tt.end)
			}
		})
	}
}

func TestFailsClosed(t *testing.T) {
	c := mustNew(t)
	at := time.Date(2028, 3, 1, 15, 0, 0, 0, time.UTC)
	if _, _, trading, err := c.Session(domain.MarketNYSE, at); trading || !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("unknown year: trading=%v err=%v", trading, err)
	}
	if c.IsOpen(domain.MarketNYSE, at) {
		t.Fatal("unknown year reported open")
	}
	if _, _, trading, err := c.Session("XETRA", at); trading || !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown market: %v %v", trading, err)
	}
}

func TestIsOpen(t *testing.T) {
	c := mustNew(t)
	for at, want := range map[string]bool{
		"2026-11-23T07:59:59Z": false, // LSE one second before the open
		"2026-11-23T08:00:00Z": true,
		"2026-11-23T16:29:59Z": true,
		"2026-11-23T16:30:00Z": false, // close is exclusive
		"2026-12-24T12:30:00Z": false, // half day
	} {
		ts, _ := time.Parse(time.RFC3339, at)
		if got := c.IsOpen(domain.MarketLSE, ts); got != want {
			t.Errorf("IsOpen(LSE, %s) = %v, want %v", at, got, want)
		}
	}
}

func TestTablesAreSane(t *testing.T) {
	c := mustNew(t)
	for m, mk := range c.markets {
		if !slices.Equal(c.Years(m), []int{2026, 2027}) {
			t.Errorf("%s years = %v", m, c.Years(m))
		}
		for y, yt := range mk.years {
			for _, list := range [][]string{yt.closed, yt.early} {
				for _, d := range list {
					day, err := time.ParseInLocation("2006-01-02", time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006")+"-"+d, mk.loc)
					if err != nil {
						t.Fatalf("%s %d: bad date %q", m, y, d)
					}
					if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
						t.Errorf("%s %d-%s falls on a weekend: the table must hold the observed weekday", m, y, d)
					}
				}
			}
		}
	}
}
