package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// t212Env is an app with an eToro market-data fake, a Trading 212 practice account (GBP),
// its owner's eToro demo price source with keys, and a fresh Trading 212 catalogue.
type t212Env struct {
	*env
	ctx     context.Context
	market  *fakes.Broker
	sim     *fakes.T212Sim
	owner   Principal
	t212    domain.AccountView // practice, GBP
	etoro   domain.AccountView // the owner's eToro demo (price source)
	t212Rl  domain.AccountView // a real Trading 212 account (never traded)
	etoroRl domain.AccountView // the owner's eToro real account
}

func setKeys(t *testing.T, e *env, a domain.Account) {
	t.Helper()
	k, u := domain.AccountKeyNames(a)
	if err := e.sec.Put(context.Background(), k, "test-only-key"); err != nil {
		t.Fatal(err)
	}
	if err := e.sec.Put(context.Background(), u, "test-only-user"); err != nil {
		t.Fatal(err)
	}
}

var t212Catalogue = []domain.T212Instrument{
	{Ticker: "BARCl_EQ", ShortName: "BARC", Name: "Barclays", Currency: "GBX", Type: "STOCK", ISIN: "GB0031348658", MaxOpenQuantity: 1e12},
	{Ticker: "AAPL_US_EQ", ShortName: "AAPL", Name: "Apple", Currency: "USD", Type: "STOCK", ISIN: "US0378331005", MaxOpenQuantity: 1e12},
	{Ticker: "VODl_EQ", ShortName: "VOD", Name: "Vodafone", Currency: "GBX", Type: "STOCK", ISIN: "GB00BH4HKS39", MaxOpenQuantity: 1e12},
	{Ticker: "VOD2l_EQ", ShortName: "VOD", Name: "Vodafone (2)", Currency: "GBX", Type: "STOCK", ISIN: "GB00BH4HKS40", MaxOpenQuantity: 1e12},
	{Ticker: "TINYl_EQ", ShortName: "TINY", Currency: "GBX", Type: "STOCK", MaxOpenQuantity: 0},
}

func newT212Env(t *testing.T) *t212Env {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	market := &fakes.Broker{Known: map[domain.InstrumentID]domain.Instrument{
		1: {ID: 1, Symbol: "BARC.L", Name: "Barclays", Type: "Stocks", Market: domain.MarketLSE, Currency: "GBX"},
		2: {ID: 2, Symbol: "AAPL", Name: "Apple", Type: "Stocks", Market: domain.MarketNYSE, Currency: "USD"},
		3: {ID: 3, Symbol: "CBA.ASX", Type: "Stocks", Market: domain.MarketASX, Currency: "AUD"},
		4: {ID: 4, Symbol: "VOD.L", Type: "Stocks", Market: domain.MarketLSE, Currency: "GBX"},
		5: {ID: 5, Symbol: "NONE.L", Type: "Stocks", Market: domain.MarketLSE, Currency: "GBX"},
		6: {ID: 6, Symbol: "TINY.L", Type: "Stocks", Market: domain.MarketLSE, Currency: "GBX"},
	}, Prices: map[domain.InstrumentID]domain.Quote{1: {Instrument: 1, Bid: 199_000_000, Ask: 200_000_000}}}
	sim := &fakes.T212Sim{Now: e.clock.Now, Currency: "GBP", Cash: 500_000, Catalogue: t212Catalogue}
	e.svc.d.Broker, e.svc.d.Market, e.svc.d.T212, e.svc.d.Calendar = market, market, sim, fixedCalendar{}
	owner := e.user(t, "olga", domain.RoleOperator)
	te := &t212Env{env: e, ctx: ctx, market: market, sim: sim, owner: owner}
	var err error
	mk := func(pl domain.Platform, name string, m domain.TradingMode) domain.AccountView {
		v, err := e.svc.CreateAccountOn(ctx, owner, pl, name, m)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	te.etoro = mk(domain.PlatformEtoro, "A eToro demo", domain.ModeDemo)
	te.t212 = mk(domain.PlatformTrading212, "B Trading 212 practice", domain.ModeDemo)
	te.t212Rl = mk(domain.PlatformTrading212, "C Trading 212 real", domain.ModeReal)
	te.etoroRl = mk(domain.PlatformEtoro, "D eToro real", domain.ModeReal)
	for _, v := range []domain.AccountView{te.etoro, te.t212, te.t212Rl, te.etoroRl} {
		setKeys(t, e, v.Account)
	}
	if _, err = e.svc.RefreshT212Instruments(ctx, owner, te.t212.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetT212PriceSource(ctx, owner, te.t212.ID, te.etoro.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetCurrentAccount(ctx, owner, te.t212.ID); err != nil {
		t.Fatal(err)
	}
	return te
}

func audited(e *env, action string) int {
	n := 0
	for _, a := range e.store.AuditEntries() {
		if a.Action == action {
			n++
		}
	}
	return n
}

func TestT212SetupCatalogue(t *testing.T) {
	te := newT212Env(t)
	ctx, svc := te.ctx, te.svc
	st, err := svc.T212CatalogueStatus(ctx, te.owner, te.t212.ID)
	if err != nil || st.Stale || !st.FetchedAt.Equal(te.clock.Now()) {
		t.Fatalf("status = %+v %v", st, err)
	}
	if audited(te.env, "t212.instruments_refreshed") != 1 {
		t.Fatal("refresh not audited")
	}
	// At most once per 50 s.
	if _, err := svc.RefreshT212Instruments(ctx, te.owner, te.t212.ID); !errors.Is(err, domain.ErrBusy) {
		t.Fatalf("second refresh within 50 s: %v", err)
	}
	te.clock.T = te.clock.T.Add(T212CatalogueMinInterval)
	// Unusable tickers are skipped; the first of a repeated ticker wins.
	te.sim.Catalogue = append(slices212(t212Catalogue), domain.T212Instrument{Ticker: ""}, domain.T212Instrument{Ticker: strings.Repeat("x", 65)},
		domain.T212Instrument{Ticker: "BARCl_EQ", ShortName: "OTHER"})
	got, err := svc.RefreshT212Instruments(ctx, te.owner, te.t212.ID)
	if err != nil || got.Count != len(t212Catalogue) {
		t.Fatalf("refresh = %+v %v", got, err)
	}
	if b, _ := te.store.T212Instruments().Get(ctx, "BARCl_EQ"); b.ShortName != "BARC" {
		t.Fatalf("repeated ticker: %+v", b)
	}
	te.clock.T = te.clock.T.Add(time.Minute)
	te.sim.Catalogue = nil
	if _, err := svc.RefreshT212Instruments(ctx, te.owner, te.t212.ID); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("empty catalogue: %v", err)
	}
	te.sim.ReadErr = domain.ErrUnavailable
	if _, err := svc.RefreshT212Instruments(ctx, te.owner, te.t212.ID); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("read error: %v", err)
	}
	te.sim.ReadErr, te.sim.Catalogue = nil, t212Catalogue
	// The loop's refresh uses the practice account with keys.
	if got, err := svc.RefreshT212InstrumentsAuto(ctx); err != nil || got.Count != len(t212Catalogue) {
		t.Fatalf("auto refresh = %+v %v", got, err)
	}
	// Stale after 24 h.
	te.clock.T = te.clock.T.Add(T212CatalogueMaxAge + time.Second)
	if st, _ := svc.T212CatalogueStatus(ctx, te.owner, te.t212.ID); !st.Stale {
		t.Fatal("a day-old catalogue is not stale")
	}
	// Without the adapter.
	svc.d.T212 = nil
	if _, err := svc.RefreshT212Instruments(ctx, te.owner, te.t212.ID); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("no adapter: %v", err)
	}
}

func slices212(in []domain.T212Instrument) []domain.T212Instrument {
	return append([]domain.T212Instrument(nil), in...)
}

func TestT212SetupPriceSource(t *testing.T) {
	te := newT212Env(t)
	ctx, svc := te.ctx, te.svc
	if audited(te.env, "t212.price_source_changed") != 1 {
		t.Fatal("price source not audited")
	}
	if src, err := svc.t212PriceSource(ctx, te.t212.Account); err != nil || src.ID != te.etoro.ID {
		t.Fatalf("price source = %+v %v", src, err)
	}
	other := te.user(t, "otto", domain.RoleOperator)
	otherDemo, err := svc.CreateAccountOn(ctx, other, domain.PlatformEtoro, "Otto demo", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	setKeys(t, te.env, otherDemo.Account)
	noKeys, _ := svc.CreateAccountOn(ctx, te.owner, domain.PlatformEtoro, "E no keys", domain.ModeDemo)
	tests := []struct {
		name string
		src  string
		want error
	}{
		{"an eToro real account", te.etoroRl.ID, domain.ErrInvalid},
		{"another owner's account", otherDemo.ID, domain.ErrInvalid},
		{"a Trading 212 account", te.t212.ID, domain.ErrInvalid},
		{"an account without keys", noKeys.ID, domain.ErrInvalid},
		{"no such account", "nope", domain.ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := svc.SetT212PriceSource(ctx, te.owner, te.t212.ID, tc.src); !errors.Is(err, tc.want) {
				t.Fatalf("SetT212PriceSource = %v", err)
			}
		})
	}
	// An API key, a user without owner access and an eToro target account are refused.
	api := te.owner
	api.Via = ViaAPIKey
	if err := svc.SetT212PriceSource(ctx, api, te.t212.ID, te.etoro.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("API key: %v", err)
	}
	if err := svc.SetT212PriceSource(ctx, other, te.t212.ID, te.etoro.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("not the owner: %v", err)
	}
	if err := svc.SetT212PriceSource(ctx, te.owner, te.etoro.ID, te.etoro.ID); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("an eToro account as target: %v", err)
	}
	if err := svc.SetT212PriceSource(ctx, te.owner, te.t212Rl.ID, te.etoro.ID); !errors.Is(err, domain.ErrViewOnly) {
		t.Fatalf("a real Trading 212 account as target: %v", err)
	}
	// Not while the agent runs.
	_ = te.store.Settings().Set(ctx, agentStateKey(te.t212.ID), string(domain.AgentRunning), te.clock.T)
	if err := svc.SetT212PriceSource(ctx, te.owner, te.t212.ID, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("agent running: %v", err)
	}
	_ = te.store.Settings().Set(ctx, agentStateKey(te.t212.ID), string(domain.AgentStopped), te.clock.T)
	// Checked again on every use: a source that lost its keys no longer serves.
	_ = te.sec.Delete(ctx, mustKey(te.etoro.Account))
	if _, err := svc.t212PriceCtx(ctx, te.t212.Account); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("source without keys: %v", err)
	}
	setKeys(t, te.env, te.etoro.Account)
	// Removed: no source.
	if err := svc.SetT212PriceSource(ctx, te.owner, te.t212.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.t212PriceCtx(ctx, te.t212.Account); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("no source: %v", err)
	}
}

func mustKey(a domain.Account) domain.CredentialName {
	k, _ := domain.AccountKeyNames(a)
	return k
}

func TestT212WatchlistAddAndConfirm(t *testing.T) {
	te := newT212Env(t)
	ctx, svc := te.ctx, te.svc
	w, err := svc.AddToWatchlist(ctx, te.owner, 1)
	if err != nil || w.Instrument.Symbol != "BARC.L" || w.Eligibility.RealLong || !strings.Contains(w.Eligibility.Reason, "not confirmed") {
		t.Fatalf("add = %+v %v", w, err)
	}
	m, err := te.store.InstrumentMatches().Get(ctx, te.t212.ID, 1)
	if err != nil || m.Ticker != "BARCl_EQ" || m.ISIN != "GB0031348658" || m.Confirmed() || m.Method != domain.MatchMethodAuto {
		t.Fatalf("match = %+v %v", m, err)
	}
	if _, el, _ := te.store.Instruments().Get(ctx, 1); el.RealLong || !el.CheckedAt.IsZero() {
		t.Fatalf("eToro's eligibility cache was written: %+v", el)
	}
	if items, err := svc.Watchlist(ctx, te.owner); err != nil || len(items) != 1 {
		t.Fatalf("watchlist = %+v %v", items, err)
	}
	if audited(te.env, "watchlist.add") != 1 {
		t.Fatal("add not audited")
	}
	if _, err := svc.AddToWatchlist(ctx, te.owner, 1); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second add: %v", err)
	}
	// Not eligible until confirmed.
	el, _, err := svc.t212Eligibility(ctx, te.t212.Account, w.Instrument, "GBP")
	if err != nil || el.RealLong || !strings.Contains(el.Reason, "not confirmed") {
		t.Fatalf("eligibility before confirm = %+v %v", el, err)
	}
	// Only the owner, in a web session, confirms.
	trader := te.user(t, "tara", domain.RoleOperator)
	if err := svc.ShareAccount(ctx, te.owner, te.t212.ID, "tara", domain.AccessTrade); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetCurrentAccount(ctx, trader, te.t212.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmT212Match(ctx, trader, 1); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("trader confirmed: %v", err)
	}
	api := te.owner
	api.Via = ViaAPIKey
	if _, err := svc.ConfirmT212Match(ctx, api, 1); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("API key confirmed: %v", err)
	}
	if _, err := svc.ConfirmT212Match(ctx, te.owner, 2); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("confirm without a match: %v", err)
	}
	got, err := svc.ConfirmT212Match(ctx, te.owner, 1)
	if err != nil || !got.Confirmed() || got.ConfirmedBy != te.owner.UserID {
		t.Fatalf("confirm = %+v %v", got, err)
	}
	if audited(te.env, "t212.match_confirmed") != 1 {
		t.Fatal("confirm not audited")
	}
	el, _, err = svc.t212Eligibility(ctx, te.t212.Account, w.Instrument, "GBP")
	if err != nil || !el.RealLong || el.ShortCFD || !el.CheckedAt.Equal(te.clock.Now()) {
		t.Fatalf("eligibility after confirm = %+v %v", el, err)
	}
	// The trader may add and remove (trade access), not confirm; remove drops the match.
	if err := svc.RemoveFromWatchlist(ctx, trader, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := te.store.InstrumentMatches().Get(ctx, te.t212.ID, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("the match survived the removal")
	}
	if el, _, _ := svc.t212Eligibility(ctx, te.t212.Account, w.Instrument, "GBP"); el.RealLong {
		t.Fatal("eligible without a match")
	}
}

func TestT212WatchlistRefusals(t *testing.T) {
	tests := []struct {
		name  string
		id    domain.InstrumentID
		setup func(*t212Env)
		want  error
		why   string
	}{
		{"Sydney share", 3, func(*t212Env) {}, domain.ErrInvalid, "London (LSE) or New York"},
		{"no match", 5, func(*t212Env) {}, domain.ErrInvalid, "no Trading 212 instrument matches"},
		{"ambiguous", 4, func(*t212Env) {}, domain.ErrInvalid, "more than one"},
		{"currency mismatch (USD share, GBP account)", 2, func(*t212Env) {}, domain.ErrInvalid, "cannot be traded"},
		{"no position allowed", 6, func(*t212Env) {}, domain.ErrInvalid, "no position"},
		{"unknown to eToro", 99, func(*t212Env) {}, domain.ErrInvalid, "does not know"},
		{"not positive", 0, func(*t212Env) {}, domain.ErrInvalid, "positive"},
		{"stale catalogue", 1, func(te *t212Env) { te.clock.T = te.clock.T.Add(25 * time.Hour) }, domain.ErrUnavailable, "refresh"},
		{"no price source", 1, func(te *t212Env) {
			_ = te.store.Settings().Set(te.ctx, priceSourceKey(te.t212.ID), "", te.clock.T)
		}, domain.ErrUnavailable, "price source"},
		{"Trading 212 down", 1, func(te *t212Env) { te.sim.ReadErr = domain.ErrUnavailable }, domain.ErrUnavailable, ""},
		{"eToro down", 1, func(te *t212Env) { te.market.Err = domain.ErrUnavailable }, domain.ErrUnavailable, ""},
		{"no Trading 212 adapter", 1, func(te *t212Env) { te.svc.d.T212 = nil }, domain.ErrUnavailable, ""},
		{"no eToro adapter", 1, func(te *t212Env) { te.svc.d.Broker = nil }, domain.ErrUnavailable, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			te := newT212Env(t)
			tc.setup(te)
			_, err := te.svc.AddToWatchlist(te.ctx, te.owner, tc.id)
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.why) {
				t.Fatalf("AddToWatchlist = %v, want %v (%q)", err, tc.want, tc.why)
			}
			if items, _ := te.store.Watchlist().List(te.ctx, te.t212.ID); len(items) != 0 {
				t.Fatal("a refused share is on the watchlist")
			}
			if l, _ := te.store.InstrumentMatches().List(te.ctx, te.t212.ID); len(l) != 0 {
				t.Fatal("a refused share has a match")
			}
		})
	}
}

func TestT212ConfirmRefusedWhenTheCatalogueChanged(t *testing.T) {
	te := newT212Env(t)
	ctx, svc := te.ctx, te.svc
	if _, err := svc.AddToWatchlist(ctx, te.owner, 1); err != nil {
		t.Fatal(err)
	}
	// Trading 212 now lists BARC with another ISIN: the shown match is no longer what is traded.
	te.clock.T = te.clock.T.Add(time.Minute)
	cat := slices212(t212Catalogue)
	cat[0].ISIN = "GB0000000000"
	te.sim.Catalogue = cat
	if _, err := svc.RefreshT212Instruments(ctx, te.owner, te.t212.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmT212Match(ctx, te.owner, 1); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("confirm after an ISIN change: %v", err)
	}
	// A stale catalogue confirms nothing either.
	te.clock.T = te.clock.T.Add(25 * time.Hour)
	if _, err := svc.ConfirmT212Match(ctx, te.owner, 1); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("confirm on a stale catalogue: %v", err)
	}
	// A confirmed match whose instrument changes or goes is not eligible.
	m, _ := te.store.InstrumentMatches().Get(ctx, te.t212.ID, 1)
	m.ConfirmedBy, m.ConfirmedAt = te.owner.UserID, te.clock.T
	_ = te.store.InstrumentMatches().Put(ctx, m)
	in := domain.Instrument{ID: 1, Symbol: "BARC.L"}
	if el, _, _ := svc.t212Eligibility(ctx, te.t212.Account, in, "GBP"); el.RealLong || !strings.Contains(el.Reason, "ISIN") {
		t.Fatalf("changed ISIN eligible: %+v", el)
	}
	m.Ticker = "GONEl_EQ"
	_ = te.store.InstrumentMatches().Put(ctx, m)
	if el, _, _ := svc.t212Eligibility(ctx, te.t212.Account, in, "GBP"); el.RealLong || !strings.Contains(el.Reason, "no longer listed") {
		t.Fatalf("delisted eligible: %+v", el)
	}
	te.store.T212Err = errors.New("db down")
	if _, _, err := svc.t212Eligibility(ctx, te.t212.Account, in, "GBP"); err == nil {
		t.Fatal("a store error is not an error")
	}
}

func TestT212RiskLimits(t *testing.T) {
	te := newT212Env(t)
	ctx, svc := te.ctx, te.svc
	// Defaults on a Trading 212 account: no shorts.
	l, err := svc.RiskLimits(ctx, te.owner)
	if err != nil || l.MaxShort != 0 || l.MinOrderValue != 0 || l.T212Fractional {
		t.Fatalf("defaults = %+v %v", l, err)
	}
	good := domain.DefaultRiskLimits
	good.MaxShort, good.TradingBudget, good.MinOrderValue = 0, 200_000, 5_000
	tests := []struct {
		name   string
		change func(*domain.RiskLimits)
		want   error
		field  string
	}{
		{"budget above the Trading 212 equity", func(l *domain.RiskLimits) { l.TradingBudget = 500_001 }, domain.ErrInvalid, "tradingBudgetCents"},
		{"no minimum order value", func(l *domain.RiskLimits) { l.MinOrderValue = 0 }, domain.ErrInvalid, "minOrderValueCents"},
		{"minimum above the budget", func(l *domain.RiskLimits) { l.MinOrderValue = 200_001 }, domain.ErrInvalid, "minOrderValueCents"},
		{"shorts", func(l *domain.RiskLimits) { l.MaxShort = 100 }, domain.ErrInvalid, "maxShortBp"},
		{"fractions while unverified", func(l *domain.RiskLimits) { l.T212Fractional = true }, domain.ErrInvalid, "t212Fractional"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := good
			tc.change(&l)
			_, err := svc.SetRiskLimits(ctx, te.owner, l)
			var ve *domain.ValidationError
			if !errors.Is(err, tc.want) || !errors.As(err, &ve) || ve.Field != tc.field {
				t.Fatalf("SetRiskLimits = %v", err)
			}
		})
	}
	if _, err := svc.SetRiskLimits(ctx, te.owner, good); err != nil {
		t.Fatal(err)
	}
	if l, _ := svc.RiskLimits(ctx, te.owner); l != good {
		t.Fatalf("stored = %+v", l)
	}
	if v, err := svc.RiskLimitsView(ctx, te.owner); err != nil || !v.CanEdit || v.Limits != good {
		t.Fatalf("view = %+v %v", v, err)
	}
	// Trading 212 down: a budget cannot be checked, so it is refused.
	te.sim.ReadErr = domain.ErrUnavailable
	if _, err := svc.SetRiskLimits(ctx, te.owner, good); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("Trading 212 down: %v", err)
	}
	te.sim.ReadErr = nil
	// An eToro account takes no minimum order value or fractional switch.
	if _, err := svc.SetCurrentAccount(ctx, te.owner, te.etoro.ID); err != nil {
		t.Fatal(err)
	}
	e := domain.DefaultRiskLimits
	e.MinOrderValue = 1
	if _, err := svc.SetRiskLimits(ctx, te.owner, e); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("eToro with a minimum order value: %v", err)
	}
}

// TestT212UseCaseMatrix (T212-c): the setup use cases and the agent controls serve a Trading
// 212 practice account; the eToro-only ones still refuse it (ErrViewOnly); a real Trading 212 account is refused by
// all of them.
func TestT212UseCaseMatrix(t *testing.T) {
	te := newT212Env(t)
	ctx, svc := te.ctx, te.svc
	owner := te.owner
	api := owner
	api.Via = ViaAPIKey
	viewer := te.user(t, "vic", domain.RoleViewer)
	trader := te.user(t, "tim", domain.RoleOperator)
	for _, sh := range []struct {
		user   string
		access domain.AccountAccess
	}{{"vic", domain.AccessTrade}, {"tim", domain.AccessTrade}} {
		if err := svc.ShareAccount(ctx, owner, te.t212.ID, sh.user, sh.access); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []Principal{viewer, trader} {
		if _, err := svc.SetCurrentAccount(ctx, p, te.t212.ID); err != nil {
			t.Fatal(err)
		}
	}
	type call func(Principal) error
	calls := map[string]call{
		"RiskLimits":       func(p Principal) error { _, err := svc.RiskLimits(ctx, p); return err },
		"RiskLimitsView":   func(p Principal) error { _, err := svc.RiskLimitsView(ctx, p); return err },
		"SetRiskLimits":    func(p Principal) error { _, err := svc.SetRiskLimits(ctx, p, domain.RiskLimits{}); return err },
		"Watchlist":        func(p Principal) error { _, err := svc.Watchlist(ctx, p); return err },
		"WatchlistQuotes":  func(p Principal) error { _, err := svc.WatchlistQuotes(ctx, p); return err },
		"SearchInstrument": func(p Principal) error { _, err := svc.SearchInstruments(ctx, p, "barc"); return err },
		"AddToWatchlist":   func(p Principal) error { _, err := svc.AddToWatchlist(ctx, p, 1); return err },
		"Remove":           func(p Principal) error { return svc.RemoveFromWatchlist(ctx, p, 77) },
		"Confirm":          func(p Principal) error { _, err := svc.ConfirmT212Match(ctx, p, 77); return err },
		"Refresh":          func(p Principal) error { _, err := svc.RefreshT212Instruments(ctx, p, ""); return err },
		"PriceSource":      func(p Principal) error { return svc.SetT212PriceSource(ctx, p, "", te.etoro.ID) },
		"Status":           func(p Principal) error { _, err := svc.T212CatalogueStatus(ctx, p, ""); return err },
	}
	// Who is let through on the practice account (a later error, e.g. validation or "not
	// found", proves the gate passed): the decisive refusal is ErrForbidden (or ErrViewOnly).
	allowed := map[string]map[string]bool{
		"owner":  {"RiskLimits": true, "RiskLimitsView": true, "SetRiskLimits": true, "Watchlist": true, "WatchlistQuotes": true, "SearchInstrument": true, "AddToWatchlist": true, "Remove": true, "Confirm": true, "Refresh": true, "PriceSource": true, "Status": true},
		"api":    {"RiskLimits": true, "RiskLimitsView": true, "Watchlist": true, "WatchlistQuotes": true, "SearchInstrument": true, "AddToWatchlist": true, "Remove": true, "Status": true},
		"trader": {"RiskLimits": true, "RiskLimitsView": true, "Watchlist": true, "WatchlistQuotes": true, "SearchInstrument": true, "AddToWatchlist": true, "Remove": true, "Status": true},
		"viewer": {"RiskLimits": true, "RiskLimitsView": true, "Watchlist": true, "WatchlistQuotes": true, "Status": true},
	}
	who := map[string]Principal{"owner": owner, "api": api, "trader": trader, "viewer": viewer}
	for name, p := range who {
		for op, c := range calls {
			err := c(p)
			denied := errors.Is(err, domain.ErrForbidden)
			if allowed[name][op] == denied {
				t.Errorf("%s %s: %v (allowed %v)", name, op, err, allowed[name][op])
			}
		}
	}
	// eToro-only use cases refuse the practice account.
	eToroOnly := map[string]call{
		"Portfolio":      func(p Principal) error { _, err := svc.Portfolio(ctx, p); return err },
		"TestConnection": func(p Principal) error { _, err := svc.TestConnection(ctx, p, te.t212.ID); return err },
		"ApplyPreset":    func(p Principal) error { _, err := svc.ApplyRiskPreset(ctx, p, domain.PresetLow); return err },
	}
	for op, c := range eToroOnly {
		if err := c(owner); !errors.Is(err, domain.ErrViewOnly) {
			t.Errorf("%s on a Trading 212 account: %v", op, err)
		}
	}
	// The agent controls serve the practice account (R45.7): a later refusal (no budget)
	// proves the account gate passed; the real Trading 212 account stays view-only.
	if _, err := svc.StartAgent(ctx, owner, te.t212.ID); err == nil || errors.Is(err, domain.ErrViewOnly) {
		t.Errorf("StartAgent on a Trading 212 practice account: %v", err)
	}
	if _, err := svc.StartAgent(ctx, owner, te.t212Rl.ID); !errors.Is(err, domain.ErrViewOnly) {
		t.Errorf("StartAgent on a real Trading 212 account: %v", err)
	}
	if _, err := svc.StopAgent(ctx, owner, te.t212Rl.ID); !errors.Is(err, domain.ErrViewOnly) {
		t.Errorf("StopAgent on a real Trading 212 account: %v", err)
	}
	// A real Trading 212 account is refused by every one of them.
	if _, err := svc.SetCurrentAccount(ctx, owner, te.t212Rl.ID); !errors.Is(err, domain.ErrViewOnly) {
		t.Fatalf("SetCurrentAccount(real Trading 212): %v", err)
	}
	if _, err := svc.RefreshT212Instruments(ctx, owner, te.t212Rl.ID); !errors.Is(err, domain.ErrViewOnly) {
		t.Fatalf("Refresh(real Trading 212): %v", err)
	}
	if _, err := svc.T212CatalogueStatus(ctx, owner, te.t212Rl.ID); !errors.Is(err, domain.ErrViewOnly) {
		t.Fatalf("Status(real Trading 212): %v", err)
	}
	// An eToro account is not a Trading 212 account for the setup.
	if _, err := svc.RefreshT212Instruments(ctx, owner, te.etoro.ID); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Refresh(eToro): %v", err)
	}
}

// TestCurrentAccountPrefersEToro: without a stored choice the current account is an eToro
// one, even when a Trading 212 practice account sorts first, and a user with only a practice
// account still has "no eToro account" (nothing changes for anyone who never chose a Trading
// 212 account); a chosen practice account holds.
func TestCurrentAccountPrefersEToro(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.svc.CreateAccountOn(ctx, e.admin, domain.PlatformTrading212, "A practice", domain.ModeDemo); err != nil {
		t.Fatal(err)
	}
	et, err := e.svc.CreateAccountOn(ctx, e.admin, domain.PlatformEtoro, "Z eToro", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := e.svc.CurrentAccount(ctx, e.admin); err != nil || v.ID != et.ID {
		t.Fatalf("current = %+v %v", v, err)
	}
	// With only a practice account and no choice: no current account, as before.
	e2 := newEnv(t)
	pr, _ := e2.svc.CreateAccountOn(ctx, e2.admin, domain.PlatformTrading212, "Practice", domain.ModeDemo)
	if _, err := e2.svc.RiskLimits(ctx, e2.admin); !errors.Is(err, domain.ErrNoAccount) {
		t.Fatalf("limits without a chosen account: %v", err)
	}
	// Chosen, it is the current one for the use cases that serve it.
	if _, err := e2.svc.SetCurrentAccount(ctx, e2.admin, pr.ID); err != nil {
		t.Fatal(err)
	}
	if id := e2.svc.currentAccountID(ctx, e2.admin, mustAccounts(t, e2)); id != pr.ID {
		t.Fatalf("current id = %q", id)
	}
	if _, err := e2.svc.RiskLimits(ctx, e2.admin); err != nil {
		t.Fatalf("limits of the chosen practice account: %v", err)
	}
	// A stored choice that is no longer allowed falls back to eToro (none here).
	_ = e2.store.Settings().Set(ctx, currentAccountKey(e2.admin.UserID), "gone", e2.clock.T)
	if id := e2.svc.currentAccountID(ctx, e2.admin, mustAccounts(t, e2)); id != "" {
		t.Fatalf("current id after a stale choice = %q", id)
	}
}

func mustAccounts(t *testing.T, e *env) []domain.AccountView {
	t.Helper()
	vs, err := e.svc.Accounts(context.Background(), e.admin)
	if err != nil {
		t.Fatal(err)
	}
	return vs
}
