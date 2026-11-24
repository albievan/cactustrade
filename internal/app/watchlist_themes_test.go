package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// themedEnv: the admin's demo account with AAPL (1) and BARC.L (2) on the watchlist.
func themedEnv(t *testing.T) (*env, *fakes.Broker, domain.AccountView) {
	t.Helper()
	ctx := context.Background()
	e, b := withBroker(t)
	b.Known[1] = domain.Instrument{ID: 1, Symbol: "AAPL", Name: "Apple Inc", Type: "Stocks", Market: domain.MarketNYSE}
	b.Known[2] = domain.Instrument{ID: 2, Symbol: "BARC.L", Name: "Barclays Bank PLC", Type: "Stocks", Market: domain.MarketLSE}
	for _, id := range []domain.InstrumentID{1, 2} {
		if _, err := e.svc.AddToWatchlist(ctx, e.admin, id); err != nil {
			t.Fatal(err)
		}
	}
	v, err := e.svc.CurrentAccount(ctx, e.admin)
	if err != nil {
		t.Fatal(err)
	}
	return e, b, v
}

func themeShares(t *testing.T, e *env, accountID string) map[string][]domain.InstrumentID {
	t.Helper()
	ts, err := e.store.Themes().List(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]domain.InstrumentID{}
	for _, x := range ts {
		out[x.Name] = x.Instruments
	}
	return out
}

func TestAssignThemesMovesTheShare(t *testing.T) {
	ctx := context.Background()
	e, _, v := themedEnv(t)
	a, err := e.svc.SaveTheme(ctx, e.admin, domain.Theme{Name: "Tech", Instruments: []domain.InstrumentID{1}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.svc.SaveTheme(ctx, e.admin, domain.Theme{Name: "Banks", Keywords: []string{"bank"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		as   []ThemeAssignment
		want map[string][]domain.InstrumentID
		n    int
		err  error
	}{
		{"into Banks", []ThemeAssignment{{1, b.ID}}, map[string][]domain.InstrumentID{"Tech": {}, "Banks": {1}}, 2, nil},
		{"again: nothing changes", []ThemeAssignment{{1, b.ID}}, map[string][]domain.InstrumentID{"Tech": {}, "Banks": {1}}, 0, nil},
		{"both", []ThemeAssignment{{1, a.ID}, {2, b.ID}}, map[string][]domain.InstrumentID{"Tech": {1}, "Banks": {2}}, 2, nil},
		{"none", []ThemeAssignment{{1, ""}}, map[string][]domain.InstrumentID{"Tech": {}, "Banks": {2}}, 1, nil},
		{"not on the watchlist", []ThemeAssignment{{2, a.ID}, {5, a.ID}}, map[string][]domain.InstrumentID{"Tech": {}, "Banks": {2}}, 0, domain.ErrNotFound},
		{"unknown theme", []ThemeAssignment{{1, "nope"}}, map[string][]domain.InstrumentID{"Tech": {}, "Banks": {2}}, 0, domain.ErrNotFound},
		{"empty", nil, map[string][]domain.InstrumentID{"Tech": {}, "Banks": {2}}, 0, domain.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := e.svc.AssignThemes(ctx, e.admin, tt.as)
			if !errors.Is(err, tt.err) || n != tt.n {
				t.Fatalf("AssignThemes = %d %v, want %d %v", n, err, tt.n, tt.err)
			}
			got := themeShares(t, e, v.ID)
			for name, ids := range tt.want {
				if !slices.Equal(got[name], ids) {
					t.Fatalf("%s = %v, want %v", name, got[name], ids)
				}
			}
		})
	}
	// the list shows each share's theme
	es, err := e.svc.WatchlistEntries(ctx, e.admin)
	if err != nil || len(es) != 2 || es[0].ThemeID != "" || es[1].ThemeID != b.ID {
		t.Fatalf("entries = %+v %v", es, err)
	}
	// a full theme refuses one more share, and nothing is stored
	full := make([]domain.InstrumentID, domain.MaxThemeInstruments)
	for i := range full {
		full[i] = domain.InstrumentID(1000 + i)
	}
	tt, _ := e.store.Themes().Get(ctx, a.ID)
	tt.Instruments = full
	if err := e.store.Themes().Update(ctx, tt); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AssignThemes(ctx, e.admin, []ThemeAssignment{{2, a.ID}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("full theme: %v", err)
	}
	if got := themeShares(t, e, v.ID); !slices.Equal(got["Banks"], []domain.InstrumentID{2}) {
		t.Fatalf("a refused assignment changed Banks: %v", got["Banks"])
	}
	if _, err := e.svc.AssignThemes(ctx, e.admin, make([]ThemeAssignment, MaxThemeAssignments+1)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("too many: %v", err)
	}
}

func TestProposeThemes(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		themes    []domain.Theme
		screen    error
		want      map[string]string // symbol → theme name
		score     map[string]int
		notes     int
		unmatched []string
	}{
		{"no themes", nil, nil, map[string]string{}, nil, 1, []string{"AAPL", "BARC.L"}},
		{"keywords", []domain.Theme{
			{Name: "Banks", Keywords: []string{"bank"}},
			{Name: "Tech", Keywords: []string{"apple", "AAPL"}},
		}, nil, map[string]string{"AAPL": "Tech", "BARC.L": "Banks"}, map[string]int{"AAPL": 8, "BARC.L": 3}, 0, []string{}},
		{"industry beats a name word", []domain.Theme{
			{Name: "Banks", Keywords: []string{"bank"}},
			{Name: "Finance", IndustryID: 9},
		}, nil, map[string]string{"BARC.L": "Finance"}, map[string]int{"BARC.L": 4}, 0, []string{"AAPL"}},
		{"industry unreadable", []domain.Theme{
			{Name: "Banks", Keywords: []string{"bank"}},
			{Name: "Finance", IndustryID: 9},
		}, errors.New("HTTP 500"), map[string]string{"BARC.L": "Banks"}, map[string]int{"BARC.L": 3}, 1, []string{"AAPL"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, b, _ := themedEnv(t)
			e.svc.d.Research = b
			b.Screened = map[int64][]domain.Instrument{9: {{ID: 2}, {ID: 77}}}
			b.Inds = []domain.Industry{{ID: 9, Name: "Financials"}}
			for _, th := range tt.themes {
				th.Enabled = true
				if _, err := e.svc.SaveTheme(ctx, e.admin, th); err != nil {
					t.Fatal(err)
				}
			}
			b.Err = tt.screen
			for range 2 { // deterministic
				ps, err := e.svc.ProposeThemes(ctx, e.admin)
				if err != nil {
					t.Fatal(err)
				}
				got, score := map[string]string{}, map[string]int{}
				for _, p := range ps.Proposals {
					got[p.Symbol], score[p.Symbol] = p.ThemeName, p.Match.Score
					if len(p.Match.Reasons) == 0 || p.ThemeID == "" {
						t.Fatalf("proposal without reasons or id: %+v", p)
					}
				}
				if !reflect.DeepEqual(got, tt.want) || (tt.score != nil && !reflect.DeepEqual(score, tt.score)) ||
					len(ps.Notes) != tt.notes || !slices.Equal(ps.Unmatched, tt.unmatched) {
					t.Fatalf("proposals = %v %v, notes %q, unmatched %v", got, score, ps.Notes, ps.Unmatched)
				}
			}
			// nothing was stored
			ts, _ := e.store.Themes().List(ctx, e.mustAccount(t).ID)
			for _, th := range ts {
				if len(th.Instruments) != 0 {
					t.Fatalf("proposing stored %v in %s", th.Instruments, th.Name)
				}
			}
		})
	}
	// an assigned share is not proposed again
	e, _, _ := themedEnv(t)
	th, err := e.svc.SaveTheme(ctx, e.admin, domain.Theme{Name: "Tech", Keywords: []string{"apple"}, Instruments: []domain.InstrumentID{1}})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := e.svc.ProposeThemes(ctx, e.admin)
	if err != nil || len(ps.Proposals) != 0 || !slices.Equal(ps.Unmatched, []string{"BARC.L"}) || th.ID == "" {
		t.Fatalf("after assignment = %+v %v", ps, err)
	}
}

func (e *env) mustAccount(t *testing.T) domain.AccountView {
	t.Helper()
	v, err := e.svc.CurrentAccount(context.Background(), e.admin)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestExportImportWatchlist(t *testing.T) {
	ctx := context.Background()
	e, b, v := themedEnv(t)
	prof, err := e.svc.SaveLLMProfile(ctx, e.admin, domain.LLMProfile{Name: "Desk", LLMConfig: domain.LLMConfig{Provider: "openai", Model: "gpt"}}, sp("k-desk"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SaveTheme(ctx, e.admin, domain.Theme{Name: "Tech", Keywords: []string{"apple"}, Instruments: []domain.InstrumentID{1}, LLMProfileID: prof.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	f, err := e.svc.ExportWatchlist(ctx, e.admin)
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != 1 || f.AccountName != "Main demo" || len(f.Watchlist) != 2 || f.Watchlist[0] != (domain.FileShare{Instrument: 1, Symbol: "AAPL", Market: "NYSE"}) ||
		len(f.Themes) != 1 || f.Themes[0].LLMProfileName != "Desk" || !slices.Equal(f.Themes[0].Instruments, []domain.InstrumentID{1}) {
		t.Fatalf("export = %+v", f)
	}
	if dump := fmt.Sprintf("%+v", f); strings.Contains(dump, v.ID) || strings.Contains(dump, "k-desk") || strings.Contains(dump, prof.ID) {
		t.Fatalf("the export carries an id or a key: %+v", f)
	}

	// into a second account of the admin: shares go through eToro's check
	other, err := e.svc.CreateAccount(ctx, e.admin, "Second", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetCurrentAccount(ctx, e.admin, other.ID); err != nil {
		t.Fatal(err)
	}
	f.Watchlist = append(f.Watchlist,
		domain.FileShare{Instrument: 3, Symbol: "SAP.DE", Market: "LSE"},   // the file lies: eToro says no supported market
		domain.FileShare{Instrument: 5, Symbol: "CFDONLY", Market: "NYSE"}, // not eligible
		domain.FileShare{Instrument: 6, Symbol: "BMW.DE", Market: "XETRA"}, // never asked
	)
	f.Themes = append(f.Themes, domain.FileTheme{Name: "Banks", Keywords: []string{"bank"}, Instruments: []domain.InstrumentID{2}, LLMProfileName: "Missing"})
	elig := b.EligCalls
	res, err := e.svc.ImportWatchlist(ctx, e.admin, f)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Added, []string{"AAPL", "BARC.L"}) || len(res.Already) != 0 || len(res.Skipped) != 3 ||
		!slices.Equal(res.ThemesCreated, []string{"Tech", "Banks"}) || len(res.ThemesUpdated) != 0 || len(res.Notes) != 1 {
		t.Fatalf("first import = %+v", res)
	}
	for i, sym := range []string{"SAP.DE", "CFDONLY", "BMW.DE"} {
		if res.Skipped[i].Name != sym || res.Skipped[i].Reason == "" {
			t.Fatalf("skipped = %+v", res.Skipped)
		}
	}
	if b.EligCalls != elig+3 { // AAPL, BARC.L, CFDONLY: SAP.DE stops at the market, BMW.DE before eToro
		t.Fatalf("eligibility calls = %d", b.EligCalls-elig)
	}
	wl, _ := e.store.Watchlist().List(ctx, other.ID)
	if len(wl) != 2 || slices.ContainsFunc(wl, func(w domain.WatchItem) bool {
		return w.Instrument.ID == 3 || w.Instrument.ID == 5 || w.Instrument.ID == 6
	}) {
		t.Fatalf("watchlist after import = %+v", wl)
	}
	ts, _ := e.store.Themes().List(ctx, other.ID)
	if len(ts) != 2 || ts[1].Name != "Tech" || ts[1].LLMProfileID != prof.ID || ts[0].LLMProfileID != "" {
		t.Fatalf("themes after import = %+v", ts)
	}

	// again: nothing changes, eToro is not asked
	elig = b.EligCalls
	res, err = e.svc.ImportWatchlist(ctx, e.admin, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 0 || !slices.Equal(res.Already, []string{"AAPL", "BARC.L"}) || len(res.ThemesCreated)+len(res.ThemesUpdated) != 0 ||
		!slices.Equal(res.ThemesUnchanged, []string{"Tech", "Banks"}) || b.EligCalls != elig+1 {
		t.Fatalf("second import = %+v (eligibility calls %d)", res, b.EligCalls-elig)
	}
	ts2, _ := e.store.Themes().List(ctx, other.ID)
	if !reflect.DeepEqual(ts, ts2) {
		t.Fatalf("the second import changed themes:\n%+v\n%+v", ts, ts2)
	}

	// a theme of the same name is updated, and its shares leave every other theme
	if _, err := e.svc.SaveTheme(ctx, e.admin, domain.Theme{Name: "Mine", Keywords: []string{"x"}, Instruments: []domain.InstrumentID{1, 2}}); err != nil {
		t.Fatal(err)
	}
	g := domain.WatchlistFile{Version: 1, Themes: []domain.FileTheme{{Name: "tech", Keywords: []string{"apple", "iphone"}, Instruments: []domain.InstrumentID{1}}}}
	res, err = e.svc.ImportWatchlist(ctx, e.admin, g)
	if err != nil || !slices.Equal(res.ThemesUpdated, []string{"tech"}) || len(res.Notes) != 1 {
		t.Fatalf("update = %+v %v", res, err)
	}
	got := themeShares(t, e, other.ID)
	if !slices.Equal(got["Mine"], []domain.InstrumentID{2}) || !slices.Equal(got["tech"], []domain.InstrumentID{1}) {
		t.Fatalf("after update = %v", got)
	}

	// refused files change nothing
	for name, bad := range map[string]domain.WatchlistFile{
		"version":     {Version: 2},
		"too many":    {Version: 1, Watchlist: make([]domain.FileShare, domain.MaxFileShares+1)},
		"duplicate":   {Version: 1, Themes: []domain.FileTheme{{Name: "A", Keywords: []string{"a"}}, {Name: "a", Keywords: []string{"b"}}}},
		"control":     {Version: 1, Themes: []domain.FileTheme{{Name: "A\x07", Keywords: []string{"a"}}}},
		"negative id": {Version: 1, Watchlist: []domain.FileShare{{Instrument: -4, Market: "NYSE"}}},
	} {
		if _, err := e.svc.ImportWatchlist(ctx, e.admin, bad); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if got2 := themeShares(t, e, other.ID); !reflect.DeepEqual(got, got2) {
		t.Fatal("a refused file changed themes")
	}

	// eToro unreachable: shares are reported, not added; the import still answers
	b.Err = fmt.Errorf("HTTP 503: %w", domain.ErrUnavailable)
	res, err = e.svc.ImportWatchlist(ctx, e.admin, domain.WatchlistFile{Version: 1, Watchlist: []domain.FileShare{{Instrument: 9, Symbol: "MSFT", Market: "NYSE"}}})
	if err != nil || len(res.Skipped) != 1 || res.Skipped[0].Reason == "" {
		t.Fatalf("eToro down = %+v %v", res, err)
	}
}

func TestWatchlistThemesAccess(t *testing.T) {
	ctx := context.Background()
	e, _, v := themedEnv(t)
	th, err := e.svc.SaveTheme(ctx, e.admin, domain.Theme{Name: "Tech", Keywords: []string{"apple"}})
	if err != nil {
		t.Fatal(err)
	}
	trader := e.user(t, "tom", domain.RoleOperator)
	viewer := e.user(t, "vic", domain.RoleOperator)
	readOnly := e.user(t, "rae", domain.RoleViewer)
	stranger := e.user(t, "sam", domain.RoleOperator)
	for who, acc := range map[string]domain.AccountAccess{"tom": domain.AccessTrade, "vic": domain.AccessView, "rae": domain.AccessTrade} {
		if err := e.svc.ShareAccount(ctx, e.admin, v.ID, who, acc); err != nil {
			t.Fatal(err)
		}
	}
	key := trader
	key.Via = ViaAPIKey
	file := domain.WatchlistFile{Version: 1}
	tests := []struct {
		name    string
		p       Principal
		canEdit bool
		edit    error // assign, propose, export
		imp     error
	}{
		{"owner", e.admin, true, nil, nil},
		{"trade share", trader, true, nil, nil},
		{"view share", viewer, false, domain.ErrForbidden, domain.ErrForbidden},
		{"viewer role on a trade share", readOnly, false, domain.ErrForbidden, domain.ErrForbidden},
		{"api key with trade access", key, true, nil, domain.ErrForbidden},
		{"not shared", stranger, false, domain.ErrNoAccount, domain.ErrNoAccount},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := e.svc.WatchlistOverview(ctx, tt.p)
			if errors.Is(tt.edit, domain.ErrNoAccount) {
				if !errors.Is(err, domain.ErrNoAccount) {
					t.Fatalf("overview: %v", err)
				}
			} else if err != nil || o.CanEdit != tt.canEdit {
				t.Fatalf("overview = %+v %v", o, err)
			}
			if _, err := e.svc.AssignThemes(ctx, tt.p, []ThemeAssignment{{1, th.ID}}); !errors.Is(err, tt.edit) {
				t.Fatalf("assign: %v", err)
			}
			if _, err := e.svc.ProposeThemes(ctx, tt.p); !errors.Is(err, tt.edit) {
				t.Fatalf("propose: %v", err)
			}
			if _, err := e.svc.ExportWatchlist(ctx, tt.p); !errors.Is(err, tt.edit) {
				t.Fatalf("export: %v", err)
			}
			if _, err := e.svc.ImportWatchlist(ctx, tt.p, file); !errors.Is(err, tt.imp) {
				t.Fatalf("import: %v", err)
			}
		})
	}
}

// portfolioCounter counts portfolio reads.
type portfolioCounter struct {
	*fakes.Broker
	mu sync.Mutex
	n  int
}

func (c *portfolioCounter) Portfolio(ctx context.Context) (domain.Portfolio, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return c.Broker.Portfolio(ctx)
}

func TestWatchlistHoldingsFromTheViewCache(t *testing.T) {
	ctx := context.Background()
	e, b, v := themedEnv(t)
	pc := &portfolioCounter{Broker: b}
	e.svc.d.Broker = pc
	pf := domain.Portfolio{AvailableCash: 1000_00, AsOf: e.clock.Now(), Positions: []domain.Position{
		{ID: 11, Instrument: 1, Units: 3_000_000, Amount: 600_00, UnrealisedPnL: 12_00, RealAsset: true, Long: true, Leverage: 1},
		{ID: 12, Instrument: 1, Units: 1_000_000, Amount: 200_00, UnrealisedPnL: -2_00, RealAsset: true, Long: true, Leverage: 1},
		{ID: 13, Instrument: 4, Units: 500_000, Amount: 50_00, UnrealisedPnL: 1_00, RealAsset: true, Long: true, Leverage: 1},
	}}

	// no keys: unknown, and eToro is not asked
	o, err := e.svc.WatchlistOverview(ctx, e.admin)
	if err != nil || o.Holdings.Known || o.Holdings.Error == "" || pc.n != 0 || !o.CanEdit {
		t.Fatalf("without keys = %+v %v (%d reads)", o, err, pc.n)
	}
	ak, uk := domain.AccountKeyNames(v.Account)
	for _, n := range []domain.CredentialName{ak, uk} {
		if err := e.sec.Put(ctx, n, "test-key"); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.store.AgentPositions().Add(ctx, domain.AgentPosition{AccountID: v.ID, PositionID: 12, Instrument: 1, OpenRequestID: "r12", OpenedAt: e.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	e.svc.rt.views.put(v.ID, pf, e.clock.Now())
	es, err := e.svc.WatchlistEntries(ctx, e.admin)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.InstrumentHolding{Instrument: 1, Positions: 2, Units: 4_000_000, Value: 810_00, Cost: 800_00, PnL: 10_00, Ours: 1}
	if es[0].Holding == nil || es[0].Holding.InstrumentHolding != want || es[0].Holding.Market != domain.MarketNYSE || es[1].Holding != nil {
		t.Fatalf("entries = %+v / %+v", es[0].Holding, es[1].Holding)
	}
	o, err = e.svc.WatchlistOverview(ctx, e.admin)
	if err != nil || !o.Holdings.Known || o.Holdings.Stale || o.Holdings.WatchlistValue != 810_00 || o.Holdings.WatchlistPnL != 10_00 ||
		o.Holdings.WatchlistPositions != 2 || len(o.Holdings.NotOnWatchlist) != 1 || o.Holdings.NotOnWatchlist[0].Symbol != "#4" {
		t.Fatalf("overview = %+v %v", o, err)
	}
	if pc.n != 0 {
		t.Fatalf("the cache was fresh, yet eToro was read %d times", pc.n)
	}
	// stale cache: one shared read for the whole page, never one per row
	e.clock.Advance(viewCacheTTL)
	for range 3 {
		if _, err := e.svc.WatchlistEntries(ctx, e.admin); err != nil {
			t.Fatal(err)
		}
	}
	if pc.n != 1 {
		t.Fatalf("reads after the TTL = %d, want 1", pc.n)
	}
	// a failed read: holdings unknown, the list still answers
	e.clock.Advance(viewCacheTTL)
	b.Err = fmt.Errorf("HTTP 500: %w", domain.ErrUnavailable)
	es, err = e.svc.WatchlistEntries(ctx, e.admin)
	if err != nil || len(es) != 2 || es[0].Holding != nil {
		t.Fatalf("failed read = %+v %v", es, err)
	}
	e.svc.d.Broker = nil
	if o, _ := e.svc.WatchlistOverview(ctx, e.admin); o.Holdings.Known || o.Holdings.Error == "" {
		t.Fatalf("no broker = %+v", o.Holdings)
	}
}
