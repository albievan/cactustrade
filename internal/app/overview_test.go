package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// t212Account creates a Trading 212 account owned by p.
func (e *env) t212Account(t *testing.T, p Principal, name string) domain.AccountView {
	t.Helper()
	v, err := e.svc.CreateAccountOn(context.Background(), p, domain.PlatformTrading212, name, domain.ModeReal)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestViewOnlyAccountsNeverTrade: every trading path refuses a Trading 212 account, through
// the one central check, while managing it (keys, sharing, rename, delete) still works.
func TestViewOnlyAccountsNeverTrade(t *testing.T) {
	ctx := context.Background()
	e, _ := withBroker(t) // admin owns the eToro "Main demo"
	isa := e.t212Account(t, e.admin, "ISA")
	if isa.Platform != domain.PlatformTrading212 || isa.Tradable() {
		t.Fatalf("created = %+v", isa)
	}
	// the current account is never a Trading 212 one
	if cur, _ := e.svc.CurrentAccount(ctx, e.admin); cur.Platform != domain.PlatformEtoro {
		t.Fatalf("current = %+v", cur)
	}
	_ = e.store.Settings().Set(ctx, currentAccountKey(e.admin.UserID), isa.ID, e.clock.Now()) // a stale choice
	if cur, _ := e.svc.CurrentAccount(ctx, e.admin); cur.Name != "Main demo" {
		t.Fatalf("current after a stale choice = %+v", cur)
	}
	byID := []struct {
		name string
		call func() error
	}{
		{"switch to it", func() error { _, err := e.svc.SetCurrentAccount(ctx, e.admin, isa.ID); return err }},
		{"test connection", func() error { _, err := e.svc.TestConnection(ctx, e.admin, isa.ID); return err }},
		{"trade access", func() error { _, err := e.svc.account(ctx, e.admin, isa.ID, domain.AccessTrade); return err }},
		{"view access", func() error { _, err := e.svc.account(ctx, e.admin, isa.ID, domain.AccessView); return err }},
	}
	for _, tt := range byID {
		if err := tt.call(); !errors.Is(err, domain.ErrViewOnly) || !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}

	// a user whose only account is a Trading 212 one has no account to trade with
	op := e.user(t, "olivia", domain.RoleOperator)
	e.t212Account(t, op, "Olivia ISA")
	current := []struct {
		name string
		call func() error
	}{
		{"portfolio", func() error { _, err := e.svc.Portfolio(ctx, op); return err }},
		{"watchlist", func() error { _, err := e.svc.Watchlist(ctx, op); return err }},
		{"add to watchlist", func() error { _, err := e.svc.AddToWatchlist(ctx, op, 1); return err }},
		{"remove from watchlist", func() error { return e.svc.RemoveFromWatchlist(ctx, op, 1) }},
		{"quotes", func() error { _, err := e.svc.WatchlistQuotes(ctx, op); return err }},
		{"search", func() error { _, err := e.svc.SearchInstruments(ctx, op, "a"); return err }},
		{"risk limits", func() error { _, err := e.svc.RiskLimits(ctx, op); return err }},
		{"scanner", func() error { return e.svc.SetScanner(ctx, op, true) }},
		{"scan", func() error { _, err := e.svc.ScanNow(ctx, op); return err }},
		{"reviews", func() error { _, _, err := e.svc.Reviews(ctx, op, 10); return err }},
		{"themes", func() error { _, err := e.svc.Themes(ctx, op); return err }},
		{"industries", func() error { _, err := e.svc.Industries(ctx, op); return err }},
		{"test connection", func() error { _, err := e.svc.TestConnection(ctx, op, ""); return err }},
	}
	for _, tt := range current {
		if err := tt.call(); !errors.Is(err, domain.ErrNoAccount) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
	if st, err := e.svc.Status(ctx, op); err != nil || st.Account != nil {
		t.Fatalf("status = %+v %v", st, err)
	}

	// managing it works: keys (API key + secret), sharing, rename, delete
	if err := e.svc.SetAccountKeys(ctx, e.admin, isa.ID, " t212-key\n", "t212-secret"); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.sec.Get(ctx, domain.CredentialName("t212."+isa.ID+".api_secret")); v != "t212-secret" {
		t.Fatal("secret not stored under the Trading 212 name")
	}
	if k, err := e.svc.AccountKeysStatus(ctx, e.admin, isa.ID); err != nil || !k.APIKeySet || !k.UserKeySet || k.FromEnvironment {
		t.Fatalf("keys = %+v %v", k, err)
	}
	if err := e.svc.ShareAccount(ctx, e.admin, isa.ID, "olivia", domain.AccessTrade); err != nil {
		t.Fatal(err)
	}
	if sh, err := e.svc.AccountShares(ctx, e.admin, isa.ID); err != nil || len(sh) != 1 {
		t.Fatalf("shares = %+v %v", sh, err)
	}
	if _, err := e.svc.account(ctx, op, isa.ID, domain.AccessTrade); !errors.Is(err, domain.ErrViewOnly) {
		t.Fatalf("trade access on a shared Trading 212 account: %v", err)
	}
	if err := e.svc.UnshareAccount(ctx, e.admin, isa.ID, op.UserID); err != nil {
		t.Fatal(err)
	}
	if v, err := e.svc.RenameAccount(ctx, e.admin, isa.ID, "Stocks ISA"); err != nil || v.Name != "Stocks ISA" {
		t.Fatalf("rename = %+v %v", v, err)
	}
	if err := e.svc.DeleteAccount(ctx, e.admin, isa.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sec.Get(ctx, domain.CredentialName("t212."+isa.ID+".api_key")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("the Trading 212 key survived its account")
	}
	if _, err := e.svc.CreateAccountOn(ctx, e.admin, "ibkr", "x", domain.ModeDemo); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown platform: %v", err)
	}
}

func gbp(cash, equity, pnl domain.Cents) domain.Holdings {
	return domain.Holdings{
		Platform: domain.PlatformTrading212, Currency: "GBP", Cash: cash, Equity: equity, UnrealisedPnL: pnl,
		Positions: []domain.Holding{{Symbol: "AAPL_US_EQ", Name: "Apple", Quantity: 1_000_000, Value: equity - cash, Long: true}},
	}
}

func TestOverview(t *testing.T) {
	ctx := context.Background()
	e, b := withBroker(t)
	b.Account = domain.Portfolio{AvailableCash: 10_000, Positions: []domain.Position{
		{ID: 1, Instrument: 1, Units: 2_000_000, OpenRate: 1_500_000, Amount: 3_000, UnrealisedPnL: 50, Long: true, RealAsset: true, Leverage: 1},
		{ID: 2, Instrument: 7, Units: 1_000_000, Amount: 500, Long: true, RealAsset: true, Leverage: 1}, // unknown to eToro
		{ID: 3, Instrument: 1, Units: 1_000_000, Amount: 1_000, Long: true, RealAsset: true, Leverage: 1},
	}}
	_ = e.store.Instruments().Upsert(ctx, domain.Instrument{ID: 1, Symbol: "AAPL", Name: "Apple (cached)"}, domain.Eligibility{Instrument: 1}, e.clock.Now())
	ok1, ok2 := e.t212Account(t, e.admin, "ISA one"), e.t212Account(t, e.admin, "ISA two")
	e.t212Account(t, e.admin, "Fresh") // no keys yet
	broken := e.t212Account(t, e.admin, "Broken")
	src := &fakes.Holdings{
		ByAccount: map[string]domain.Holdings{ok1.ID: gbp(100, 1_000, 5), ok2.ID: gbp(200, 3_000, -15)},
		Errs:      map[string]error{broken.ID: fmt.Errorf("app.holdings: Trading 212 account_summary: HTTP 401: the API key or secret was refused: %w", domain.ErrUnavailable)},
	}
	e.svc.d.Holdings = map[domain.Platform]ports.Holdings{domain.PlatformTrading212: src}

	ov, err := e.svc.Overview(ctx, e.admin)
	if err != nil || len(ov.Accounts) != 5 || !ov.AsOf.Equal(e.clock.Now()) {
		t.Fatalf("overview = %+v %v", ov, err)
	}
	by := map[string]AccountHoldings{}
	for _, a := range ov.Accounts {
		by[a.Account.Name] = a
	}
	main := by["Main demo"].Holdings
	if main == nil || main.Currency != "USD" || main.Cash != 10_000 || len(main.Positions) != 3 ||
		main.Positions[0].Name != "Apple (cached)" || main.Positions[0].CurrentPrice != 1 || main.Positions[1].Symbol != "#7" {
		t.Fatalf("eToro holdings = %+v", main)
	}
	if a := by["Fresh"]; a.Holdings != nil || !a.NoKeys || a.Error != "no keys yet" {
		t.Fatalf("no keys = %+v", a)
	}
	if a := by["Broken"]; a.Holdings != nil || a.NoKeys || a.Error != "Trading 212 account_summary: HTTP 401: the API key or secret was refused" {
		t.Fatalf("broken = %+v", a)
	}
	if len(ov.Totals) != 2 || ov.Totals[0].Currency != "GBP" || ov.Totals[0].Accounts != 2 || ov.Totals[0].Equity != 4_000 ||
		ov.Totals[0].UnrealisedPnL != -10 || ov.Totals[1].Currency != "USD" || ov.Totals[1].Equity != main.Equity {
		t.Fatalf("totals = %+v", ov.Totals)
	}

	// a viewer sees only what is shared with them
	viewer := e.user(t, "vera", domain.RoleViewer)
	if ov, err := e.svc.Overview(ctx, viewer); err != nil || len(ov.Accounts) != 0 || len(ov.Totals) != 0 {
		t.Fatalf("viewer = %+v %v", ov, err)
	}
	if _, err := e.svc.Overview(ctx, Principal{}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("anonymous overview")
	}

	// prices and names are cosmetic: an eToro market-data failure still shows the account
	b.Err = nil
	e.svc.d.Market = failingMarket{b}
	ov, _ = e.svc.Overview(ctx, e.admin)
	for _, a := range ov.Accounts {
		if a.Account.Name == "Main demo" && (a.Holdings == nil || a.Holdings.Positions[0].CurrentPrice != 0 || a.Holdings.Positions[1].Symbol != "#7") {
			t.Fatalf("without market data = %+v", a)
		}
	}
	// without a Trading 212 client or a broker the accounts say why
	e.svc.d.Holdings, e.svc.d.Broker = nil, nil
	ov, _ = e.svc.Overview(ctx, e.admin)
	for _, a := range ov.Accounts {
		if a.Holdings != nil || a.Error == "" || !strings.Contains(a.Error, "not configured") {
			t.Fatalf("unconfigured = %+v", a)
		}
	}
}

// failingMarket is the fake broker whose market data always fails.
type failingMarket struct{ *fakes.Broker }

func (failingMarket) Quotes(context.Context, []domain.InstrumentID) ([]domain.Quote, error) {
	return nil, domain.ErrUnavailable
}

func (failingMarket) Instruments(context.Context, []domain.InstrumentID) ([]domain.Instrument, error) {
	return nil, domain.ErrUnavailable
}

func TestRecordEquityAndHistory(t *testing.T) {
	ctx := context.Background()
	e, b := withBroker(t)
	b.Account = domain.Portfolio{AvailableCash: 10_000}
	isa := e.t212Account(t, e.admin, "ISA")
	e.t212Account(t, e.admin, "Fresh") // no keys yet
	src := &fakes.Holdings{ByAccount: map[string]domain.Holdings{isa.ID: gbp(100, 1_000, 0)}}
	e.svc.d.Holdings = map[domain.Platform]ports.Holdings{domain.PlatformTrading212: src}
	var mainID string
	vs, _ := e.svc.Accounts(ctx, e.admin)
	for _, v := range vs {
		if v.Name == "Main demo" {
			mainID = v.ID
		}
	}
	old := domain.EquitySnapshot{AccountID: isa.ID, At: e.clock.Now().Add(-401 * 24 * time.Hour), Currency: "GBP", Equity: 1}
	if err := e.store.EquitySnapshots().Add(ctx, old); err != nil {
		t.Fatal(err)
	}

	if n := e.svc.RecordEquity(ctx); n != 2 { // eToro + the ISA; "Fresh" has no keys
		t.Fatalf("recorded %d", n)
	}
	if n := e.svc.RecordEquity(ctx); n != 0 { // same minute: already recorded
		t.Fatalf("recorded twice: %d", n)
	}
	e.clock.Advance(15 * time.Minute)
	src.ByAccount[isa.ID] = gbp(100, 1_100, 0)
	src.Errs = map[string]error{} // still fine
	b.Err = domain.ErrUnavailable // eToro down: skipped, logged
	if n := e.svc.RecordEquity(ctx); n != 1 {
		t.Fatalf("recorded %d with eToro down", n)
	}

	tests := []struct {
		name    string
		days    int
		wantErr error
		series  int
	}{
		{"default 30 days", 0, nil, 2},
		{"one day", 1, nil, 2},
		{"all", domain.EquityRetentionDays, nil, 2},
		{"too long", domain.EquityRetentionDays + 1, domain.ErrInvalid, 0},
		{"negative", -1, domain.ErrInvalid, 0},
	}
	for _, tt := range tests {
		got, err := e.svc.EquityHistory(ctx, e.admin, tt.days)
		if !errors.Is(err, tt.wantErr) || len(got) != tt.series {
			t.Errorf("%s: %d series, %v", tt.name, len(got), err)
		}
	}
	got, _ := e.svc.EquityHistory(ctx, e.admin, 30)
	for _, s := range got {
		switch s.Account.ID {
		case isa.ID:
			if s.Currency != "GBP" || len(s.Points) != 2 || s.Points[1].Equity != 1_100 || s.Account.Name != "ISA" {
				t.Fatalf("ISA series = %+v", s)
			}
		case mainID:
			if s.Currency != "USD" || len(s.Points) != 1 || s.Points[0].Equity != 10_000 {
				t.Fatalf("eToro series = %+v", s)
			}
		default:
			t.Fatalf("unexpected series %+v", s)
		}
	}
	// the 401-day-old snapshot was purged
	if all, _ := e.store.EquitySnapshots().List(ctx, []string{isa.ID}, time.Time{}); len(all) != 2 {
		t.Fatalf("after purge = %+v", all)
	}
	viewer := e.user(t, "vera", domain.RoleViewer)
	if got, err := e.svc.EquityHistory(ctx, viewer, 30); err != nil || len(got) != 0 {
		t.Fatalf("viewer history = %+v %v", got, err)
	}
	if _, err := e.svc.EquityHistory(ctx, Principal{}, 30); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("anonymous history")
	}
}

func TestReadableError(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("app.holdings: eToro portfolio: HTTP 500: %w", domain.ErrUnavailable), "eToro portfolio: HTTP 500"},
		{domain.ErrUnavailable, "the platform did not answer"},
		{fmt.Errorf("x.y: %w", domain.ErrNotFound), "the platform did not answer"},
	}
	for _, tt := range tests {
		if got := readableError(tt.err); got != tt.want {
			t.Errorf("readableError(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}
