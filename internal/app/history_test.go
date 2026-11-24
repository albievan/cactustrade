package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// histEnv is an admin with an eToro demo account that has keys, on a fake broker whose
// trade history the test scripts. The clock stands at 2026-11-23 09:00 UTC.
type histEnv struct {
	*env
	b   *fakes.Broker
	ctx context.Context
	a   domain.Account
}

func newHistEnv(t *testing.T) *histEnv {
	t.Helper()
	e, b := withBroker(t)
	h := &histEnv{env: e, b: b, ctx: context.Background()}
	v, err := e.svc.CurrentAccount(h.ctx, e.admin)
	if err != nil {
		t.Fatal(err)
	}
	h.a = v.Account
	h.keys(t, h.a)
	return h
}

func (h *histEnv) keys(t *testing.T, a domain.Account) {
	t.Helper()
	ak, uk := domain.AccountKeyNames(a)
	for _, n := range []domain.CredentialName{ak, uk} {
		if err := h.sec.Put(h.ctx, n, "test-key"); err != nil {
			t.Fatal(err)
		}
	}
}

// closed is a trade of position id closed at, with net profit net and fees 10 cents.
func closed(id int64, at time.Time, net domain.Cents) domain.ClosedTrade {
	return domain.ClosedTrade{
		PositionID: id, Instrument: 1, OpenedAt: at.Add(-time.Hour), ClosedAt: at, NetProfit: net, Long: true, Leverage: 1,
		Fees: 10, FeesKnown: true, Investment: 100_00, Units: 1_000_000, OpenRate: 100_000_000, CloseRate: 101_000_000,
	}
}

func (h *histEnv) stored(t *testing.T, accountID string) []domain.Trade {
	t.Helper()
	ts, err := h.store.Trades().List(h.ctx, accountID, ports.TradeFilter{Limit: 1_000})
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestSyncTrades(t *testing.T) {
	h := newHistEnv(t)
	now := h.clock.Now()
	// Position 101 was opened by cactusTrade; 102 by the owner (OD1).
	if err := h.store.AgentPositions().Add(h.ctx, domain.AgentPosition{AccountID: h.a.ID, PositionID: 101, OpenRequestID: "req-101", Instrument: 1, Direction: domain.Long, OpenedAt: now.Add(-50 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	unknownFees := closed(102, now.Add(-47*time.Hour), -250)
	unknownFees.Fees, unknownFees.FeesKnown = 0, false
	h.b.History = []domain.ClosedTrade{closed(101, now.Add(-48*time.Hour), 500), unknownFees, {PositionID: 0, ClosedAt: now}}

	r, err := h.svc.SyncTrades(h.ctx, h.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(-90 * 24 * time.Hour); !r.Since.Equal(want) || r.Read != 3 || r.Stored != 2 || r.Ours != 1 || r.Skipped != 1 {
		t.Fatalf("first sync = %+v (since must be 90 days back, OD18)", r)
	}
	if c := h.b.HistoryCalls[0]; c.Account.ID != h.a.ID || c.Account.Mode != domain.ModeDemo {
		t.Fatalf("history read for %+v", c.Account)
	}
	ts := h.stored(t, h.a.ID)
	if len(ts) != 2 {
		t.Fatalf("stored %+v", ts)
	}
	byID := map[int64]domain.Trade{ts[0].PositionID: ts[0], ts[1].PositionID: ts[1]}
	if x := byID[101]; !x.Ours || x.OpenRequestID != "req-101" || x.Mode != domain.ModeDemo || x.NetProfit != 500 || !x.SyncedAt.Equal(now) {
		t.Fatalf("ours = %+v", x)
	}
	if x := byID[102]; x.Ours || x.OpenRequestID != "" || x.FeesKnown {
		t.Fatalf("foreign = %+v", x)
	}

	// The next sync starts 2 days before the latest close and is idempotent.
	h.clock.Advance(time.Hour)
	r, err = h.svc.SyncTrades(h.ctx, h.a.ID)
	if err != nil || !r.Since.Equal(now.Add(-47*time.Hour-48*time.Hour)) || r.Stored != 2 {
		t.Fatalf("second sync = %+v %v", r, err)
	}
	if got := h.stored(t, h.a.ID); len(got) != 2 {
		t.Fatalf("syncing twice must not duplicate: %d rows", len(got))
	}

	// A broker error changes nothing and is returned.
	before := h.stored(t, h.a.ID)
	h.b.HistoryErr = fmt.Errorf("eToro: %w", domain.ErrUnavailable)
	h.b.History = []domain.ClosedTrade{closed(101, now, 999_999)}
	if _, err := h.svc.SyncTrades(h.ctx, h.a.ID); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("broker error = %v", err)
	}
	if after := h.stored(t, h.a.ID); len(after) != len(before) || after[0] != before[0] || after[1] != before[1] {
		t.Fatalf("a broker error changed the stored rows: %+v", after)
	}
}

func TestSyncTradesRefusals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, h *histEnv) string
		want  error
	}{
		{"unknown account", func(*testing.T, *histEnv) string { return "nope" }, domain.ErrNotFound},
		{"view-only platform", func(t *testing.T, h *histEnv) string { return h.t212Account(t, h.admin, "ISA").ID }, domain.ErrViewOnly},
		{"no keys", func(t *testing.T, h *histEnv) string {
			v, err := h.svc.CreateAccount(h.ctx, h.admin, "No keys", domain.ModeDemo)
			if err != nil {
				t.Fatal(err)
			}
			return v.ID
		}, domain.ErrCredentialsMissing},
		{"no eToro client", func(_ *testing.T, h *histEnv) string { h.svc.d.Broker = nil; return h.a.ID }, domain.ErrUnavailable},
		{"store failure", func(_ *testing.T, h *histEnv) string { h.store.TradesErr = errors.New("disk full"); return h.a.ID }, nil},
		{"store failure while writing", func(_ *testing.T, h *histEnv) string {
			h.b.History = []domain.ClosedTrade{closed(1, h.clock.Now(), 1)}
			h.svc.d.Store = failingTrades{Store: h.store}
			return h.a.ID
		}, errTradesWrite},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHistEnv(t)
			id := tt.setup(t, h)
			_, err := h.svc.SyncTrades(h.ctx, id)
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

var errTradesWrite = errors.New("trades write failed")

// failingTrades is a store whose trades can be read but not written.
type failingTrades struct{ *fakes.Store }

func (f failingTrades) Trades() ports.Trades { return failingTradeRepo{f.Store.Trades()} }

type failingTradeRepo struct{ ports.Trades }

func (failingTradeRepo) Upsert(context.Context, domain.Trade) error { return errTradesWrite }

// TestSyncTradesRealAccountReadOnly: a real account's history is read (never anything else)
// and stored with mode real.
func TestSyncTradesRealAccountReadOnly(t *testing.T) {
	h := newHistEnv(t)
	sim := &fakes.SimBroker{Now: h.clock.Now, History: []domain.ClosedTrade{closed(7, h.clock.Now().Add(-time.Hour), -5)}}
	h.svc.d.Broker = sim
	v, err := h.svc.CreateAccount(h.ctx, h.admin, "Real", domain.ModeReal)
	if err != nil {
		t.Fatal(err)
	}
	h.keys(t, v.Account)
	if _, err := h.svc.SyncTrades(h.ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if ts := h.stored(t, v.ID); len(ts) != 1 || ts[0].Mode != domain.ModeReal || ts[0].Ours {
		t.Fatalf("real trades = %+v", ts)
	}
	if sim.Opens+sim.Closes+sim.Cancels != 0 || len(sim.Intents) != 0 {
		t.Fatalf("a history sync sent something: %d opens, %d closes, %d cancels", sim.Opens, sim.Closes, sim.Cancels)
	}
}

// historyBroker fails the trade history of one account.
type historyBroker struct {
	*fakes.Broker
	failFor string
}

func (b historyBroker) TradeHistory(ctx context.Context, since time.Time) ([]domain.ClosedTrade, error) {
	if a, _ := ports.AccountFrom(ctx); a.ID == b.failFor {
		return nil, fmt.Errorf("eToro: %w", domain.ErrUnavailable)
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("SyncTradesAll must bound each account")
	}
	return b.Broker.TradeHistory(ctx, since)
}

func TestSyncTradesAll(t *testing.T) {
	h := newHistEnv(t)
	h.b.History = []domain.ClosedTrade{closed(1, h.clock.Now().Add(-time.Hour), 1)}
	real, err := h.svc.CreateAccount(h.ctx, h.admin, "Real", domain.ModeReal)
	if err != nil {
		t.Fatal(err)
	}
	h.keys(t, real.Account)
	failing, err := h.svc.CreateAccount(h.ctx, h.admin, "Failing", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	h.keys(t, failing.Account)
	if _, err := h.svc.CreateAccount(h.ctx, h.admin, "No keys", domain.ModeDemo); err != nil {
		t.Fatal(err)
	}
	h.t212Account(t, h.admin, "ISA")
	h.svc.d.Broker = historyBroker{Broker: h.b, failFor: failing.ID}

	rs, err := h.svc.SyncTradesAll(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]TradeSync{}
	for _, r := range rs {
		got[r.AccountID] = r
	}
	if len(rs) != 3 || got[h.a.ID].Stored != 1 || got[real.ID].Stored != 1 || got[h.a.ID].Err != nil || !errors.Is(got[failing.ID].Err, domain.ErrUnavailable) {
		t.Fatalf("results = %+v", rs)
	}
	if len(h.stored(t, failing.ID)) != 0 {
		t.Fatal("the failing account stored trades")
	}
	ctx, cancel := context.WithCancel(h.ctx)
	cancel()
	if rs, err := h.svc.SyncTradesAll(ctx); !errors.Is(err, context.Canceled) || len(rs) != 0 {
		t.Fatalf("a cancelled context still synced: %+v %v", rs, err)
	}
	h.store.TradesErr = nil
	h.svc.d.Store = listFails{h.store}
	if _, err := h.svc.SyncTradesAll(h.ctx); err == nil {
		t.Fatal("a store failure listing accounts was not returned")
	}
}

// listFails is a store whose accounts cannot be listed.
type listFails struct{ *fakes.Store }

func (l listFails) Accounts() ports.Accounts { return failingAccounts{l.Store.Accounts()} }

type failingAccounts struct{ ports.Accounts }

func (failingAccounts) List(context.Context) ([]domain.Account, error) {
	return nil, errors.New("db down")
}

// seedHistory stores trades and snapshots for the views: on 2026-11-22 (risk day) ours +500
// and -200, a foreign +1000; on 2026-11-23 ours 0 with unknown fees. Equity 1000 → 1200 →
// 900 → 1100, 5 hours apart.
func (h *histEnv) seedHistory(t *testing.T) {
	t.Helper()
	day1 := time.Date(2026, 11, 22, 15, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	unknown := closed(4, day2, 0)
	unknown.Fees, unknown.FeesKnown = 0, false
	for _, x := range []struct {
		ct   domain.ClosedTrade
		ours bool
	}{{closed(1, day1, 500), true}, {closed(2, day1.Add(time.Hour), -200), true}, {closed(3, day1.Add(2*time.Hour), 1_000), false}, {unknown, true}} {
		if err := h.store.Trades().Upsert(h.ctx, domain.Trade{AccountID: h.a.ID, Mode: domain.ModeDemo, ClosedTrade: x.ct, Ours: x.ours, SyncedAt: day2}); err != nil {
			t.Fatal(err)
		}
	}
	for i, eq := range []domain.Cents{1_000, 1_200, 900, 1_100} {
		at := day1.Add(time.Duration(i) * 5 * time.Hour)
		if err := h.store.EquitySnapshots().Add(h.ctx, domain.EquitySnapshot{AccountID: h.a.ID, At: at, Currency: "USD", Cash: eq / 2, Equity: eq}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.Instruments().Upsert(h.ctx, domain.Instrument{ID: 1, Symbol: "AAPL"}, domain.Eligibility{Instrument: 1}, day1); err != nil {
		t.Fatal(err)
	}
}

// histAccess runs call for each kind of caller: the owner, a viewer the account is shared
// with (view access only; an API key works the same), a user without access and nobody.
func histAccess(t *testing.T, h *histEnv, call func(p Principal, accountID string) error) {
	t.Helper()
	viewer := h.user(t, "vera", domain.RoleViewer)
	if err := h.svc.ShareAccount(h.ctx, h.admin, h.a.ID, "vera", domain.AccessView); err != nil {
		t.Fatal(err)
	}
	viewerKey := viewer
	viewerKey.Via, viewerKey.APIKeyID = ViaAPIKey, "k1"
	stranger := h.user(t, "sam", domain.RoleOperator)
	isa := h.t212Account(t, h.admin, "ISA")
	for _, tt := range []struct {
		name string
		p    Principal
		id   string
		want error
	}{
		{"owner", h.admin, h.a.ID, nil},
		{"owner, current account", h.admin, "", nil},
		{"viewer with view access", viewer, h.a.ID, nil},
		{"viewer with an API key", viewerKey, h.a.ID, nil},
		{"another account", stranger, h.a.ID, domain.ErrNotFound},
		{"view-only platform", h.admin, isa.ID, domain.ErrViewOnly},
		{"nobody", Principal{}, h.a.ID, domain.ErrUnauthenticated},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := call(tt.p, tt.id)
			if (tt.want == nil && err != nil) || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestHistoryAccess: every history use case needs view access only (no trade access), keeps
// other accounts out (404) and refuses a view-only platform.
func TestHistoryAccess(t *testing.T) {
	calls := map[string]func(h *histEnv, p Principal, id string) error{
		"trades": func(h *histEnv, p Principal, id string) error {
			_, err := h.svc.Trades(h.ctx, p, id, TradeQuery{})
			return err
		},
		"daily": func(h *histEnv, p Principal, id string) error { _, err := h.svc.DailyPnL(h.ctx, p, id, 0); return err },
		"summary": func(h *histEnv, p Principal, id string) error {
			_, err := h.svc.HistorySummary(h.ctx, p, id, 0)
			return err
		},
		"export": func(h *histEnv, p Principal, id string) error {
			_, err := h.svc.ExportRows(h.ctx, p, id, ExportTrades, time.Time{}, time.Time{})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			h := newHistEnv(t)
			h.seedHistory(t)
			histAccess(t, h, func(p Principal, id string) error { return call(h, p, id) })
		})
	}
}

func TestHistoryTrades(t *testing.T) {
	h := newHistEnv(t)
	h.seedHistory(t)
	yes, no := true, false
	day1 := time.Date(2026, 11, 22, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		q    TradeQuery
		want []int64
		err  error
	}{
		{"all, newest first", TradeQuery{}, []int64{4, 3, 2, 1}, nil},
		{"ours", TradeQuery{Ours: &yes}, []int64{4, 2, 1}, nil},
		{"not ours (flagged, still listed)", TradeQuery{Ours: &no}, []int64{3}, nil},
		{"range", TradeQuery{From: day1.Add(time.Minute), To: day1.Add(3 * time.Hour)}, []int64{3, 2}, nil},
		{"page before", TradeQuery{Before: day1.Add(2 * time.Hour), Limit: 1}, []int64{2}, nil},
		{"limit too high", TradeQuery{Limit: 501}, nil, domain.ErrInvalid},
		{"negative limit", TradeQuery{Limit: -1}, nil, domain.ErrInvalid},
		{"from after to", TradeQuery{From: day1, To: day1}, nil, domain.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := h.svc.Trades(h.ctx, h.admin, h.a.ID, tt.q)
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("err = %v, want %v", err, tt.err)
				}
				return
			}
			var ids []int64
			for _, x := range got {
				ids = append(ids, x.PositionID)
				if x.Symbol != "AAPL" {
					t.Fatalf("symbol = %q", x.Symbol)
				}
			}
			if err != nil || fmt.Sprint(ids) != fmt.Sprint(tt.want) {
				t.Fatalf("trades = %v %v, want %v", ids, err, tt.want)
			}
		})
	}
	h.store.TradesErr = errors.New("db down")
	if _, err := h.svc.Trades(h.ctx, h.admin, h.a.ID, TradeQuery{}); err == nil {
		t.Fatal("a store error was swallowed")
	}
}

func TestHistoryDailyAndSummary(t *testing.T) {
	h := newHistEnv(t) // now 2026-11-23 09:00 UTC: risk day 2026-11-23
	h.seedHistory(t)
	daily, err := h.svc.DailyPnL(h.ctx, h.admin, h.a.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !daily.From.Equal(time.Date(2026, 11, 21, 22, 0, 0, 0, time.UTC)) || daily.Days != 2 || len(daily.Rows) != 2 {
		t.Fatalf("daily = %+v", daily)
	}
	d1, d2 := daily.Rows[0], daily.Rows[1]
	// Ours only (OD15): the foreign +1000 is not in the realised figure.
	if d1.Day != "2026-11-22" || d1.Trades != 2 || d1.Realised != 300 || d1.Fees != 20 || d1.EquityEnd != 1_200 || d1.EquityChange != 200 {
		t.Fatalf("day 1 = %+v", d1)
	}
	if d2.Day != "2026-11-23" || d2.Trades != 1 || d2.FeesUnknown != 1 || d2.EquityEnd != 1_100 || d2.EquityChange != -100 {
		t.Fatalf("day 2 = %+v", d2)
	}
	if one, _ := h.svc.DailyPnL(h.ctx, h.admin, h.a.ID, 1); len(one.Rows) != 1 || one.Rows[0].Day != "2026-11-23" {
		t.Fatalf("one day = %+v", one.Rows)
	}

	sum, err := h.svc.HistorySummary(h.ctx, h.admin, h.a.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Days != 30 || sum.Stats.Count != 3 || sum.Stats.Wins != 1 || sum.Stats.Losses != 1 || sum.Stats.Breakeven != 1 ||
		sum.Stats.Net != 300 || sum.Stats.FeesUnknown != 1 || sum.ForeignTrades != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if dd := sum.Drawdown; !dd.Known || dd.Peak != 1_200 || dd.MaxDrawdown != 300 || dd.MaxDrawdownBp != 2_500 || dd.Current != 100 {
		t.Fatalf("drawdown = %+v", dd)
	}

	// Empty history: no trades, no win rate.
	e := newHistEnv(t)
	empty, err := e.svc.HistorySummary(e.ctx, e.admin, e.a.ID, 7)
	if err != nil || empty.Stats.Count != 0 || empty.Stats.HasWinRate || empty.Drawdown.Known {
		t.Fatalf("empty summary = %+v %v", empty, err)
	}

	for _, days := range []int{-1, 401} {
		if _, err := h.svc.DailyPnL(h.ctx, h.admin, h.a.ID, days); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("days %d: %v", days, err)
		}
		if _, err := h.svc.HistorySummary(h.ctx, h.admin, h.a.ID, days); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("summary days %d: %v", days, err)
		}
	}
	if _, err := h.svc.DailyPnL(h.ctx, h.admin, h.a.ID, 400); err != nil {
		t.Fatal(err)
	}
	h.store.TradesErr = errors.New("db down")
	if _, err := h.svc.DailyPnL(h.ctx, h.admin, h.a.ID, 0); err == nil {
		t.Fatal("daily: store error swallowed")
	}
	if _, err := h.svc.HistorySummary(h.ctx, h.admin, h.a.ID, 0); err == nil {
		t.Fatal("summary: store error swallowed")
	}
}

func TestExportRows(t *testing.T) {
	h := newHistEnv(t)
	h.seedHistory(t)
	now := h.clock.Now()
	o, err := domain.NewOrder(domain.Order{
		RequestID: "11111111-1111-1111-1111-111111111111", AccountID: h.a.ID, Mode: domain.ModeDemo, Kind: domain.OrderOpen,
		Direction: domain.Long, Instrument: 1, Amount: 100_00, Entry: 100_000_000, StopLoss: 97_000_000,
	}, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Orders().Create(h.ctx, o); err != nil {
		t.Fatal(err)
	}
	dec := domain.Decision{ID: "d1", AccountID: h.a.ID, At: now.Add(-time.Hour), Signal: domain.DecisionSignal{Instrument: 1, Symbol: "=cmd", Direction: domain.Long}, Outcome: domain.DecisionRejectedRisk}
	if err := h.store.Decisions().Add(h.ctx, dec); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		kind    ExportKind
		from    time.Time
		rows    int
		columns int
		check   func(t *testing.T, e Export)
	}{
		{ExportTrades, time.Time{}, 4, 19, func(t *testing.T, e Export) {
			first, last := e.Rows[0], e.Rows[3]
			if first[2] != int64(1) || first[13] != domain.Cents(500) || first[14] != domain.Cents(10) || first[15] != true || first[4] != "AAPL" {
				t.Fatalf("first trade row = %v", first)
			}
			if last[14] != unknownFees {
				t.Fatalf("unknown fees cell = %v", last[14])
			}
			if e.Rows[2][15] != false {
				t.Fatalf("a foreign trade must be flagged: %v", e.Rows[2])
			}
		}},
		{ExportDaily, time.Time{}, 2, 7, func(t *testing.T, e Export) {
			if e.Rows[0][0] != "2026-11-22" || e.Rows[0][2] != domain.Cents(300) || e.Rows[0][6] != domain.Cents(200) {
				t.Fatalf("daily row = %v", e.Rows[0])
			}
		}},
		{ExportEquity, time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC), 2, 4, func(t *testing.T, e Export) {
			if e.Rows[0][3] != domain.Cents(900) || e.Rows[1][3] != domain.Cents(1_100) {
				t.Fatalf("equity rows = %v", e.Rows)
			}
		}},
		{ExportOrders, time.Time{}, 1, 17, func(t *testing.T, e Export) {
			if e.Rows[0][1] != o.RequestID || e.Rows[0][5] != domain.Cents(100_00) {
				t.Fatalf("order row = %v", e.Rows[0])
			}
		}},
		{ExportDecisions, time.Time{}, 1, 18, func(t *testing.T, e Export) {
			if e.Rows[0][1] != "d1" || e.Rows[0][3] != "=cmd" { // the CSV writer neutralises it (6.7)
				t.Fatalf("decision row = %v", e.Rows[0])
			}
		}},
		{ExportOrders, now.Add(-30 * time.Minute), 0, 17, nil},
		{ExportDecisions, now.Add(-30 * time.Minute), 0, 18, nil},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			e, err := h.svc.ExportRows(h.ctx, h.admin, h.a.ID, tt.kind, tt.from, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			if len(e.Rows) != tt.rows || len(e.Columns) != tt.columns || !e.To.Equal(now) || e.Kind != tt.kind {
				t.Fatalf("export = %d rows, %d columns, to %s", len(e.Rows), len(e.Columns), e.To)
			}
			for _, r := range e.Rows {
				if len(r) != len(e.Columns) {
					t.Fatalf("row %v has %d cells for %d columns", r, len(r), len(e.Columns))
				}
			}
			if tt.check != nil {
				tt.check(t, e)
			}
		})
	}
	if _, err := h.svc.ExportRows(h.ctx, h.admin, h.a.ID, "positions", time.Time{}, time.Time{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown kind: %v", err)
	}
	if _, err := h.svc.ExportRows(h.ctx, h.admin, h.a.ID, ExportTrades, now, now.Add(-time.Hour)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("from after to: %v", err)
	}
	for _, kind := range []ExportKind{ExportTrades, ExportDaily, ExportEquity, ExportOrders, ExportDecisions} {
		hh := newHistEnv(t)
		hh.svc.d.Store = brokenHistory{hh.store}
		if _, err := hh.svc.ExportRows(hh.ctx, hh.admin, hh.a.ID, kind, time.Time{}, time.Time{}); err == nil {
			t.Fatalf("%s: a store error was swallowed", kind)
		}
	}
}

// brokenHistory is a store whose history tables cannot be read.
type brokenHistory struct{ *fakes.Store }

var errHistoryRead = errors.New("history read failed")

func (b brokenHistory) Trades() ports.Trades       { return brokenTrades{b.Store.Trades()} }
func (b brokenHistory) Orders() ports.Orders       { return brokenOrders{b.Store.Orders()} }
func (b brokenHistory) Decisions() ports.Decisions { return brokenDecisions{b.Store.Decisions()} }
func (b brokenHistory) EquitySnapshots() ports.EquitySnapshots {
	return brokenEquity{b.Store.EquitySnapshots()}
}

type (
	brokenTrades    struct{ ports.Trades }
	brokenOrders    struct{ ports.Orders }
	brokenDecisions struct{ ports.Decisions }
	brokenEquity    struct{ ports.EquitySnapshots }
)

func (brokenTrades) List(context.Context, string, ports.TradeFilter) ([]domain.Trade, error) {
	return nil, errHistoryRead
}

func (brokenOrders) List(context.Context, string, int) ([]domain.Order, error) {
	return nil, errHistoryRead
}

func (brokenDecisions) List(context.Context, string, time.Time, domain.DecisionOutcome, int) ([]domain.Decision, error) {
	return nil, errHistoryRead
}

func (brokenEquity) List(context.Context, []string, time.Time) ([]domain.EquitySnapshot, error) {
	return nil, errHistoryRead
}

// TestExportOverTheCap: more than domain.ExportMaxRows rows is ErrTooLarge (HTTP 422), never
// a silently short file.
func TestExportOverTheCap(t *testing.T) {
	h := newHistEnv(t)
	now := h.clock.Now()
	for i := range domain.ExportMaxRows + 1 {
		at := now.Add(-time.Duration(i+1) * time.Second)
		if err := h.store.Trades().Upsert(h.ctx, domain.Trade{AccountID: h.a.ID, Mode: domain.ModeDemo, ClosedTrade: closed(int64(i+1), at, 1), Ours: true, SyncedAt: now}); err != nil {
			t.Fatal(err)
		}
		o := domain.Order{RequestID: fmt.Sprintf("r-%06d", i), AccountID: h.a.ID, Mode: domain.ModeDemo, Kind: domain.OrderOpen, State: domain.OrderFilled, CreatedAt: at}
		if err := h.store.Orders().Create(h.ctx, o); err != nil {
			t.Fatal(err)
		}
		d := domain.Decision{ID: fmt.Sprintf("d-%06d", i), AccountID: h.a.ID, At: at, Signal: domain.DecisionSignal{Direction: domain.Long}, Outcome: domain.DecisionRejectedRisk}
		if err := h.store.Decisions().Add(h.ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []ExportKind{ExportTrades, ExportOrders, ExportDecisions, ExportDaily} {
		if _, err := h.svc.ExportRows(h.ctx, h.admin, h.a.ID, kind, time.Time{}, time.Time{}); !errors.Is(err, domain.ErrTooLarge) {
			t.Fatalf("%s over the cap: %v", kind, err)
		}
		// A narrower range fits.
		if e, err := h.svc.ExportRows(h.ctx, h.admin, h.a.ID, kind, now.Add(-time.Hour), time.Time{}); err != nil || (kind != ExportDaily && len(e.Rows) != 3_600) {
			t.Fatalf("%s narrowed: %d rows, %v", kind, len(e.Rows), err)
		}
	}
	if _, err := h.svc.HistorySummary(h.ctx, h.admin, h.a.ID, 400); !errors.Is(err, domain.ErrTooLarge) {
		t.Fatalf("summary over the cap: %v", err)
	}
}
