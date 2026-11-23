package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// fixedCalendar: LSE trades 08:00–16:30 UTC every day; NYSE has no table (fails closed).
type fixedCalendar struct{}

func (fixedCalendar) Session(m domain.Market, at time.Time) (time.Time, time.Time, bool, error) {
	if m != domain.MarketLSE {
		return time.Time{}, time.Time{}, false, fmt.Errorf("no table: %w", domain.ErrUnavailable)
	}
	y, mo, d := at.UTC().Date()
	return time.Date(y, mo, d, 8, 0, 0, 0, time.UTC), time.Date(y, mo, d, 16, 30, 0, 0, time.UTC), true, nil
}

func withBroker(t *testing.T) (*env, *fakes.Broker) {
	t.Helper()
	e := newEnv(t)
	b := &fakes.Broker{
		Account: domain.Portfolio{AvailableCash: 100000, Positions: []domain.Position{
			{ID: 1, Amount: 5000, UnrealisedPnL: 100, RealAsset: true, Long: true, Leverage: 1},
			{ID: 2, Amount: 1000, RealAsset: false, Long: true, Leverage: 1},
		}},
		Known: map[domain.InstrumentID]domain.Instrument{
			1: {ID: 1, Symbol: "AAPL", Type: "Stocks", Market: domain.MarketNYSE},
			2: {ID: 2, Symbol: "BARC.L", Type: "Stocks", Market: domain.MarketLSE},
			3: {ID: 3, Symbol: "SAP.DE", Type: "Stocks"},
			4: {ID: 4, Symbol: "BTC", Type: "Crypto"},
			5: {ID: 5, Symbol: "CFDONLY", Type: "Stocks", Market: domain.MarketNYSE},
		},
		Eligible: map[domain.InstrumentID]domain.Eligibility{
			1: {Instrument: 1, RealLong: true, MinPositionAmount: 1000},
			2: {Instrument: 2, RealLong: true},
			5: {Instrument: 5, Reason: "not offered as a real share, long, at leverage 1"},
		},
		Prices: map[domain.InstrumentID]domain.Quote{1: {Instrument: 1, Bid: 1, Ask: 2}},
	}
	e.svc.d.Broker, e.svc.d.Market, e.svc.d.Calendar = b, b, fixedCalendar{}
	return e, b
}

func TestTestConnection(t *testing.T) {
	ctx := context.Background()
	e, b := withBroker(t)
	viewer := e.user(t, "vera", domain.RoleViewer)
	if _, err := e.svc.TestConnection(ctx, viewer); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("viewer tested the connection")
	}
	res, err := e.svc.TestConnection(ctx, e.admin)
	if err != nil || !res.OK || res.Mode != domain.ModeDemo || res.AvailableCash != 100000 || res.Positions != 2 ||
		res.NonConforming != 1 || res.Equity != 100000+5000+100+1000 {
		t.Fatalf("ok check = %+v %v", res, err)
	}
	b.Err = fmt.Errorf("eToro portfolio: HTTP 401 Unauthorized: %w", domain.ErrUnavailable)
	res, err = e.svc.TestConnection(ctx, e.admin)
	if err != nil || res.OK || !res.ConfiguredKeys || res.Error == "" {
		t.Fatalf("failed check = %+v %v", res, err)
	}
	b.Err = fmt.Errorf("keys: %w: %w", domain.ErrCredentialsMissing, domain.ErrUnavailable)
	if res, _ := e.svc.TestConnection(ctx, e.admin); res.ConfiguredKeys {
		t.Fatal("missing keys not reported")
	}
	b.Err = errors.New("boom")
	if _, err := e.svc.TestConnection(ctx, e.admin); err == nil {
		t.Fatal("unexpected error swallowed")
	}
	last := e.store.AuditEntries()
	if a := last[len(last)-1]; a.Action != "broker.test_connection" || a.Details != "failed" {
		t.Fatalf("audit = %+v", a)
	}
}

func TestNoBrokerConfigured(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.svc.TestConnection(ctx, e.admin); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("test connection")
	}
	if _, err := e.svc.Portfolio(ctx, e.admin); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("portfolio")
	}
	if _, err := e.svc.SearchInstruments(ctx, e.admin, "a"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("search")
	}
	if _, err := e.svc.AddToWatchlist(ctx, e.admin, 1); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("add")
	}
	if _, err := e.svc.MarketStatus(ctx, e.admin); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("market status")
	}
	if qs, err := e.svc.WatchlistQuotes(ctx, e.admin); err != nil || len(qs) != 0 {
		t.Fatalf("empty watchlist quotes = %v %v", qs, err)
	}
}

func TestPortfolioAndMarketStatus(t *testing.T) {
	ctx := context.Background()
	e, b := withBroker(t)
	viewer := e.user(t, "vera", domain.RoleViewer)
	if pf, err := e.svc.Portfolio(ctx, viewer); err != nil || pf.AvailableCash != 100000 {
		t.Fatalf("portfolio = %+v %v", pf, err)
	}
	b.Err = domain.ErrUnavailable
	if _, err := e.svc.Portfolio(ctx, viewer); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("broker error swallowed")
	}
	if _, err := e.svc.Portfolio(ctx, Principal{}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("anonymous portfolio")
	}
	e.clock.T = time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	ms, err := e.svc.MarketStatus(ctx, viewer)
	if err != nil || len(ms) != 2 {
		t.Fatalf("status = %+v %v", ms, err)
	}
	if !ms[0].Trading || !ms[0].Open || ms[0].Market != domain.MarketLSE {
		t.Errorf("LSE = %+v", ms[0])
	}
	if ms[1].Trading || ms[1].Open || ms[1].Note == "" {
		t.Errorf("NYSE without a table must be closed with a note: %+v", ms[1])
	}
	e.clock.T = time.Date(2026, 11, 23, 17, 0, 0, 0, time.UTC)
	if ms, _ := e.svc.MarketStatus(ctx, viewer); ms[0].Open || !ms[0].Trading {
		t.Errorf("LSE after the close = %+v", ms[0])
	}
}

func TestWatchlist(t *testing.T) {
	ctx := context.Background()
	e, b := withBroker(t)
	op := e.user(t, "olivia", domain.RoleOperator)
	if _, err := e.svc.SearchInstruments(ctx, op, "a"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("operator searched")
	}
	if res, err := e.svc.SearchInstruments(ctx, e.admin, "a"); err != nil || len(res) != 5 {
		t.Fatalf("search = %v %v", res, err)
	}
	if _, err := e.svc.AddToWatchlist(ctx, op, 1); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("operator added")
	}
	for id, why := range map[domain.InstrumentID]string{
		0: "bad id", 3: "unsupported market", 4: "not a share", 5: "not eligible", 99: "unknown",
	} {
		if _, err := e.svc.AddToWatchlist(ctx, e.admin, id); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%d (%s): %v", id, why, err)
		}
	}
	if _, el, err := e.store.Instruments().Get(ctx, 5); err != nil || el.RealLong || el.Reason == "" {
		t.Fatalf("refusal reason not cached: %+v %v", el, err)
	}
	w, err := e.svc.AddToWatchlist(ctx, e.admin, 1)
	if err != nil || w.Instrument.Symbol != "AAPL" || !w.Eligibility.RealLong || w.AddedBy != "admin" || w.Eligibility.CheckedAt.IsZero() {
		t.Fatalf("add = %+v %v", w, err)
	}
	if _, err := e.svc.AddToWatchlist(ctx, e.admin, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AddToWatchlist(ctx, e.admin, 1); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	items, err := e.svc.Watchlist(ctx, op)
	if err != nil || len(items) != 2 || items[0].Instrument.Symbol != "AAPL" {
		t.Fatalf("watchlist = %+v %v", items, err)
	}
	qs, err := e.svc.WatchlistQuotes(ctx, op)
	if err != nil || len(qs) != 1 || qs[0].Instrument != 1 {
		t.Fatalf("quotes = %+v %v", qs, err)
	}
	if err := e.svc.RemoveFromWatchlist(ctx, op, 2); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("operator removed")
	}
	if err := e.svc.RemoveFromWatchlist(ctx, e.admin, 2); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RemoveFromWatchlist(ctx, e.admin, 2); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("remove missing")
	}
	b.Err = domain.ErrUnavailable
	if _, err := e.svc.AddToWatchlist(ctx, e.admin, 2); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("broker failure on add")
	}
	if _, err := e.svc.WatchlistQuotes(ctx, op); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("broker failure on quotes")
	}
	if _, err := e.svc.SearchInstruments(ctx, e.admin, "a"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("broker failure on search")
	}
	if _, err := e.svc.Watchlist(ctx, Principal{}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("anonymous watchlist")
	}
}
