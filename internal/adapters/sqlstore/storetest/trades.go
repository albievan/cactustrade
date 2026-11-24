package storetest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// runTrades covers the closed-trade history (migration 00011).
func runTrades(t *testing.T, s ports.Store, ownerID string, t0 time.Time) {
	t.Helper()
	ctx := context.Background()
	acct := domain.Account{ID: "acc-trades", Name: "Trades", OwnerID: ownerID, Mode: domain.ModeDemo, CreatedAt: t0, UpdatedAt: t0}
	other := domain.Account{ID: "acc-trades-2", Name: "Trades 2", OwnerID: ownerID, Mode: domain.ModeReal, CreatedAt: t0, UpdatedAt: t0}
	for _, a := range []domain.Account{acct, other} {
		if err := s.Accounts().Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	r := s.Trades()
	yes, no := true, false
	trade := func(id int64, closed time.Time, ours bool) domain.Trade {
		return domain.Trade{
			AccountID: acct.ID, Mode: domain.ModeDemo, Ours: ours, SyncedAt: t0,
			ClosedTrade: domain.ClosedTrade{PositionID: id, Instrument: 1001, OpenedAt: closed.Add(-time.Hour), ClosedAt: closed, NetProfit: domain.Cents(id), Long: true, Leverage: 1},
		}
	}

	t.Run("trades", func(t *testing.T) {
		if last, err := r.LastClosedAt(ctx, acct.ID); err != nil || !last.IsZero() {
			t.Fatalf("LastClosedAt without trades = %s %v", last, err)
		}
		full := domain.Trade{
			AccountID: acct.ID, Mode: domain.ModeDemo, Ours: true, OpenRequestID: "11111111-2222-3333-4444-555555555555", SyncedAt: t0,
			ClosedTrade: domain.ClosedTrade{
				PositionID: 9_000_000_001, Instrument: 1001, OpenedAt: t0.Add(-2 * time.Hour), ClosedAt: t0.Add(-time.Hour),
				OpenRate: 189_500_000, CloseRate: 191_250_000, NetProfit: -9_000_000_000_000_000, Long: false, Leverage: 1,
				Fees: 107, FeesKnown: true, Investment: 50_000, Units: 2_645_503, OrderID: 555, StopLoss: 195_000_000, TakeProfit: 180_000_000,
			},
		}
		if err := r.Upsert(ctx, full); err != nil {
			t.Fatal(err)
		}
		got, err := r.List(ctx, acct.ID, ports.TradeFilter{Limit: 10})
		if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], full) {
			t.Fatalf("round trip = %+v %v\nwant %+v", got, err, full)
		}
		// Upserting again keeps one row, with the newer values.
		newer := full
		newer.NetProfit, newer.Fees, newer.FeesKnown, newer.Ours, newer.OpenRequestID, newer.SyncedAt = 1_234, 0, false, false, "", t0.Add(time.Minute)
		newer.OpenedAt = time.Time{}
		if err := r.Upsert(ctx, newer); err != nil {
			t.Fatal(err)
		}
		if err := r.Upsert(ctx, newer); err != nil { // the same values again
			t.Fatal(err)
		}
		got, _ = r.List(ctx, acct.ID, ports.TradeFilter{Limit: 10})
		if len(got) != 1 || !reflect.DeepEqual(got[0], newer) {
			t.Fatalf("after a second upsert = %+v\nwant %+v", got, newer)
		}
		missing := full
		missing.AccountID = "nope"
		if err := r.Upsert(ctx, missing); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown account: %v", err)
		}
		bad := full
		bad.PositionID = 0
		if err := r.Upsert(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid trade: %v", err)
		}

		// Five more, one hour apart; two with the same close time.
		for i, x := range []domain.Trade{
			trade(1, t0.Add(-5*time.Hour), true), trade(2, t0.Add(-4*time.Hour), false), trade(3, t0.Add(-3*time.Hour), true),
			trade(4, t0.Add(-2*time.Hour), false), trade(5, t0.Add(-2*time.Hour), true),
		} {
			if err := r.Upsert(ctx, x); err != nil {
				t.Fatalf("trade %d: %v", i, err)
			}
		}
		ids := func(ts []domain.Trade) []int64 {
			out := []int64{}
			for _, x := range ts {
				out = append(out, x.PositionID)
			}
			return out
		}
		for name, c := range map[string]struct {
			f    ports.TradeFilter
			want []int64
		}{
			"all, newest first":      {ports.TradeFilter{Limit: 100}, []int64{9_000_000_001, 5, 4, 3, 2, 1}},
			"limit":                  {ports.TradeFilter{Limit: 2}, []int64{9_000_000_001, 5}},
			"limit 0":                {ports.TradeFilter{}, []int64{}},
			"before is exclusive":    {ports.TradeFilter{Before: t0.Add(-2 * time.Hour), Limit: 100}, []int64{3, 2, 1}},
			"from inclusive, to not": {ports.TradeFilter{From: t0.Add(-4 * time.Hour), To: t0.Add(-2 * time.Hour), Limit: 100}, []int64{3, 2}},
			"ours":                   {ports.TradeFilter{Ours: &yes, Limit: 100}, []int64{5, 3, 1}},
			"not ours":               {ports.TradeFilter{Ours: &no, Limit: 100}, []int64{9_000_000_001, 4, 2}},
			"ours in a range":        {ports.TradeFilter{Ours: &yes, From: t0.Add(-3 * time.Hour), Limit: 100}, []int64{5, 3}},
		} {
			got, err := r.List(ctx, acct.ID, c.f)
			if err != nil || !reflect.DeepEqual(ids(got), c.want) {
				t.Errorf("%s: %v %v, want %v", name, ids(got), err, c.want)
			}
		}
		if got, err := r.List(ctx, other.ID, ports.TradeFilter{Limit: 100}); err != nil || len(got) != 0 {
			t.Fatalf("another account's trades: %+v %v", got, err)
		}
		if last, err := r.LastClosedAt(ctx, acct.ID); err != nil || !last.Equal(t0.Add(-time.Hour)) {
			t.Fatalf("LastClosedAt = %s %v", last, err)
		}
		if last, err := r.LastClosedAt(ctx, other.ID); err != nil || !last.IsZero() {
			t.Fatalf("LastClosedAt of another account = %s %v", last, err)
		}
		// The same position id on another account is another row.
		o := trade(1, t0, false)
		o.AccountID, o.Mode = other.ID, domain.ModeReal
		if err := r.Upsert(ctx, o); err != nil {
			t.Fatal(err)
		}
		if got, _ := r.List(ctx, acct.ID, ports.TradeFilter{Limit: 100}); len(got) != 6 {
			t.Fatalf("another account's trade changed this one's: %d", len(got))
		}
	})

	t.Run("trades_cascade", func(t *testing.T) {
		if err := s.Accounts().Delete(ctx, acct.ID); err != nil {
			t.Fatal(err)
		}
		if got, err := r.List(ctx, acct.ID, ports.TradeFilter{Limit: 100}); err != nil || len(got) != 0 {
			t.Fatalf("trades survived their account: %d %v", len(got), err)
		}
		if got, _ := r.List(ctx, other.ID, ports.TradeFilter{Limit: 100}); len(got) != 1 {
			t.Fatalf("another account's trades were deleted: %+v", got)
		}
	})
}
