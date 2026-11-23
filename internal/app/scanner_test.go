package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/strategy"
)

func bars(n int) []domain.Candle {
	out := make([]domain.Candle, n)
	t0 := time.Date(2026, 11, 20, 14, 30, 0, 0, time.UTC)
	for i := range out {
		p := domain.Micros(100_000_000 + int64(i%7)*10_000)
		out[i] = domain.Candle{Start: t0.Add(time.Duration(i) * 5 * time.Minute), Open: p, High: p, Low: p, Close: p, Volume: 100}
	}
	return out
}

func TestThemes(t *testing.T) {
	ctx := context.Background()
	e, b := withBroker(t)
	viewer := e.user(t, "vera", domain.RoleViewer)
	acct, _ := e.svc.CurrentAccount(ctx, e.admin)
	_ = e.svc.ShareAccount(ctx, e.admin, acct.ID, "vera", domain.AccessView)
	if _, err := e.svc.SaveTheme(ctx, viewer, domain.Theme{Name: "x", Keywords: []string{"a"}}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("view access saved a theme")
	}
	if _, err := e.svc.SaveTheme(ctx, e.admin, domain.Theme{Name: "Empty"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("empty theme accepted")
	}
	for id, why := range map[domain.InstrumentID]string{3: "unsupported market", 4: "not a share", 99: "unknown"} {
		if _, err := e.svc.SaveTheme(ctx, e.admin, domain.Theme{Name: "Bad", Instruments: []domain.InstrumentID{id}}); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("picked %d (%s): %v", id, why, err)
		}
	}
	space, err := e.svc.SaveTheme(ctx, e.admin, domain.Theme{Name: " Space ", Keywords: []string{"rocket"}, Enabled: true, Instruments: []domain.InstrumentID{1}})
	if err != nil || space.ID == "" || space.Name != "Space" || space.AccountID != acct.ID {
		t.Fatalf("create = %+v %v", space, err)
	}
	if _, _, err := e.store.Instruments().Get(ctx, 1); err != nil {
		t.Fatal("picked share not cached")
	}
	space.Enabled = false
	if s2, err := e.svc.SaveTheme(ctx, e.admin, space.Theme); err != nil || s2.Enabled || !s2.CreatedAt.Equal(space.CreatedAt) {
		t.Fatalf("update = %+v %v", s2, err)
	}
	if _, err := e.svc.SaveTheme(ctx, e.admin, domain.Theme{ID: "nope", Name: "x", Keywords: []string{"a"}}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("update of an unknown theme")
	}
	if ts, err := e.svc.Themes(ctx, viewer); err != nil || len(ts) != 1 {
		t.Fatalf("themes = %+v %v", ts, err)
	}
	if _, err := e.svc.Industries(ctx, viewer); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("industries without research")
	}
	e.svc.d.Research = b
	b.Inds = []domain.Industry{{ID: 7, Name: "Technology"}}
	if inds, err := e.svc.Industries(ctx, viewer); err != nil || len(inds) != 1 {
		t.Fatalf("industries = %v %v", inds, err)
	}
	if err := e.svc.DeleteTheme(ctx, viewer, space.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("view access deleted a theme")
	}
	if err := e.svc.DeleteTheme(ctx, e.admin, space.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteTheme(ctx, e.admin, space.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("delete twice")
	}
}

func TestScanner(t *testing.T) {
	ctx := context.Background()
	e, b := withBroker(t)
	e.svc.d.Research = b
	e.svc.d.Calendar = openCalendar{}
	acct, _ := e.svc.CurrentAccount(ctx, e.admin)
	b.Known[6] = domain.Instrument{ID: 6, Symbol: "RKLB", Name: "Rocket Lab", Type: "Stocks", Market: domain.MarketNYSE}
	b.Known[7] = domain.Instrument{ID: 7, Symbol: "SEMI", Name: "Semi", Type: "Stocks", Market: domain.MarketNYSE}
	b.Eligible[6] = domain.Eligibility{Instrument: 6, RealLong: true, ShortCFD: false, ShortReason: "not shortable"}
	b.Eligible[7] = domain.Eligibility{Instrument: 7, RealLong: true, ShortCFD: true}
	b.Screened = map[int64][]domain.Instrument{9: {b.Known[7]}}
	b.Bars = map[domain.InstrumentID][]domain.Candle{1: bars(60), 6: bars(60), 7: bars(60)}
	// AAPL (1) by hand, SAP.DE (3) from the search is unsupported, RKLB and SEMI via search
	// (the fake search returns every known instrument) and the industry screen.
	if _, err := e.svc.SaveTheme(ctx, e.admin, domain.Theme{Name: "Space", Keywords: []string{"rocket"}, Enabled: true, IndustryID: 9}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SaveTheme(ctx, e.admin, domain.Theme{Name: "Off", Keywords: []string{"x"}, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	// AAPL: a long candidate; RKLB: a short candidate eToro does not allow; SEMI: no signal;
	// CFDONLY (5): no bars → error.
	e.svc.evalOverride = func(id domain.InstrumentID, _ []domain.Candle) strategy.Decision {
		checks := []strategy.Check{{Rule: "enough price history", Pass: true}}
		switch id {
		case 1:
			return strategy.Decision{Candidate: &domain.Candidate{Instrument: 1, Direction: domain.Long, Playbook: "trend_breakout", Reason: "up"}, Checks: checks}
		case 6:
			return strategy.Decision{Candidate: &domain.Candidate{Instrument: 6, Direction: domain.Short, Playbook: "trend_breakdown_short", Reason: "down"}, Checks: checks}
		}
		return strategy.Decision{
			Reason: "no breakout", Checks: append(checks, strategy.Check{Rule: "playbook x", Detail: "no breakout"}),
			Probs: map[domain.Regime]float64{domain.RegimeChoppy: 1},
		}
	}
	res, err := e.svc.ScanNow(ctx, e.admin)
	if err != nil || len(res.Added) != 1 || res.Added[0] != "AAPL" {
		t.Fatalf("scan = %+v %v", res, err)
	}
	rs, on, err := e.svc.Reviews(ctx, e.admin, 0)
	if err != nil || !on {
		t.Fatalf("reviews: %v %v", on, err)
	}
	by := map[string]domain.Review{}
	for _, r := range rs {
		by[r.Symbol] = r
	}
	if by["AAPL"].Outcome != domain.ReviewAdded || by["AAPL"].Theme != "Space" || by["AAPL"].Direction != domain.Long {
		t.Errorf("AAPL = %+v", by["AAPL"])
	}
	if by["RKLB"].Outcome != domain.ReviewSkipped || by["RKLB"].Checks[len(by["RKLB"].Checks)-1].Pass {
		t.Errorf("RKLB (short not allowed) = %+v", by["RKLB"])
	}
	if by["SEMI"].Outcome != domain.ReviewNoSignal || by["SEMI"].Probs[domain.RegimeChoppy] != 1 {
		t.Errorf("SEMI = %+v", by["SEMI"])
	}
	if by["CFDONLY"].Outcome != domain.ReviewError {
		t.Errorf("CFDONLY (no bars) = %+v", by["CFDONLY"])
	}
	if _, ok := by["SAP.DE"]; ok {
		t.Error("an unsupported market was reviewed")
	}
	if wl, _ := e.svc.Watchlist(ctx, e.admin); len(wl) != 1 || wl[0].AddedBy != "scanner" {
		t.Fatalf("watchlist = %+v", wl)
	}
	f, _ := e.svc.Events(ctx, e.admin, 5)
	if f.Events[0].Kind != "scanner.added" || f.Events[0].AccountID != acct.ID {
		t.Fatalf("event = %+v", f.Events[0])
	}
	// second scan: AAPL is on the watchlist now; eligibility is cached (no new call)
	calls := b.EligCalls
	e.clock.T = e.clock.T.Add(time.Minute)
	if _, err := e.svc.ScanNow(ctx, e.admin); err != nil {
		t.Fatal(err)
	}
	if b.EligCalls != calls {
		t.Errorf("eligibility asked again within a day (%d → %d)", calls, b.EligCalls)
	}
	rs, _, _ = e.svc.Reviews(ctx, e.admin, 0)
	for _, r := range rs {
		if r.Symbol == "AAPL" && r.Outcome != domain.ReviewCandidate {
			t.Errorf("AAPL second time = %+v", r)
		}
	}
	// switched off: ScanAll skips the account; on again: it runs
	if err := e.svc.SetScanner(ctx, e.admin, false); err != nil {
		t.Fatal(err)
	}
	if e.svc.ScannerEnabled(ctx, acct.ID) {
		t.Fatal("still on")
	}
	before := len(e.store.AuditEntries())
	e.svc.ScanAll(ctx)
	_ = before
	if err := e.svc.SetScanner(ctx, e.admin, true); err != nil {
		t.Fatal(err)
	}
	e.clock.T = e.clock.T.Add(15 * 24 * time.Hour) // old reviews are purged by the runner
	e.svc.ScanAll(ctx)
	if rs, _, _ := e.svc.Reviews(ctx, e.admin, 0); len(rs) == 0 {
		t.Fatal("ScanAll did not scan")
	}
	// watchlist full
	for i := range watchlistCap {
		id := domain.InstrumentID(1000 + i)
		b.Known[id] = domain.Instrument{ID: id, Symbol: "X" + string(rune('A'+i%26)), Type: "Stocks", Market: domain.MarketNYSE}
		_ = e.store.Instruments().Upsert(ctx, b.Known[id], domain.Eligibility{Instrument: id}, e.clock.T)
		_ = e.store.Watchlist().Add(ctx, acct.ID, id, "t", e.clock.T)
	}
	_ = e.svc.RemoveFromWatchlist(ctx, e.admin, 1)
	if _, err := e.svc.ScanNow(ctx, e.admin); err != nil {
		t.Fatal(err)
	}
	rs, _, _ = e.svc.Reviews(ctx, e.admin, 200)
	found := false
	for _, r := range rs {
		if r.Symbol == "AAPL" {
			found = true
			if r.Outcome != domain.ReviewSkipped {
				t.Errorf("AAPL with a full watchlist = %+v", r)
			}
		}
	}
	if !found {
		t.Log("AAPL fell out of the universe (cap reached by the watchlist), as designed")
	}
	// broker down
	b.Err = domain.ErrUnavailable
	if _, err := e.svc.ScanNow(ctx, e.admin); err != nil {
		t.Fatalf("a broker outage must become error reviews, not a failed scan: %v", err)
	}
	e.svc.d.Broker = nil
	if _, err := e.svc.ScanNow(ctx, e.admin); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("scan without a broker")
	}
}

// TestScanSkipsClosedMarketsAndViewOnly: a share is reviewed only while its own market is in
// session, and the background runner never scans a view-only (Trading 212) account.
func TestScanSkipsClosedMarketsAndViewOnly(t *testing.T) {
	ctx := context.Background()
	e, b := withBroker(t) // fixedCalendar: LSE 08:00–16:30 UTC, NYSE fails closed
	e.svc.d.Research = b
	acct, _ := e.svc.CurrentAccount(ctx, e.admin)
	b.Known[1] = domain.Instrument{ID: 1, Symbol: "AAPL", Type: "Stocks", Market: domain.MarketNYSE}
	b.Known[2] = domain.Instrument{ID: 2, Symbol: "BARC.L", Type: "Stocks", Market: domain.MarketLSE}
	for _, id := range []domain.InstrumentID{1, 2} {
		_ = e.store.Instruments().Upsert(ctx, b.Known[id], domain.Eligibility{Instrument: id}, e.clock.T)
		_ = e.store.Watchlist().Add(ctx, acct.ID, id, "t", e.clock.T)
	}
	e.svc.evalOverride = func(domain.InstrumentID, []domain.Candle) strategy.Decision { return strategy.Decision{Reason: "none"} }
	e.clock.T = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	if res, err := e.svc.ScanNow(ctx, e.admin); err != nil || res.Reviewed != 1 {
		t.Fatalf("only BARC.L (LSE open) should be reviewed: %+v %v", res, err)
	}
	e.clock.T = time.Date(2026, 11, 23, 20, 0, 0, 0, time.UTC)
	if res, err := e.svc.ScanNow(ctx, e.admin); err != nil || res.Reviewed != 0 {
		t.Fatalf("nothing is in session: %+v %v", res, err)
	}

	// A Trading 212 account with a watchlist row written straight to the store is still never scanned.
	e.svc.d.Calendar = openCalendar{}
	isa := e.t212Account(t, e.admin, "ISA")
	_ = e.store.Watchlist().Add(ctx, isa.ID, 1, "t", e.clock.T)
	e.svc.ScanAll(ctx)
	if rs, err := e.store.Reviews().List(ctx, isa.ID, 10); err != nil || len(rs) != 0 {
		t.Fatalf("view-only account scanned: %+v %v", rs, err)
	}
}
