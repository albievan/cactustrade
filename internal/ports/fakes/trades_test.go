package fakes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// TestFakeTrades checks the in-memory trades follow the port contract that the shared SQL
// suite (storetest) checks for SQLite and MariaDB.
func TestFakeTrades(t *testing.T) {
	ctx := context.Background()
	s := NewStore()
	_ = s.Users().Create(ctx, domain.User{ID: "u", Username: "u", Role: domain.RoleAdmin})
	for _, id := range []string{"a", "b"} {
		_ = s.Accounts().Create(ctx, domain.Account{ID: id, Name: id, OwnerID: "u", Mode: domain.ModeDemo})
	}
	r := s.Trades()
	tr := func(acct string, id int64, closed time.Time, ours bool) domain.Trade {
		return domain.Trade{AccountID: acct, Mode: domain.ModeDemo, Ours: ours, ClosedTrade: domain.ClosedTrade{PositionID: id, ClosedAt: closed}}
	}
	if last, err := r.LastClosedAt(ctx, "a"); err != nil || !last.IsZero() {
		t.Fatalf("LastClosedAt = %s %v", last, err)
	}
	for _, x := range []domain.Trade{tr("a", 1, simT0, true), tr("a", 2, simT0.Add(time.Hour), false), tr("a", 3, simT0.Add(time.Hour), true), tr("b", 1, simT0, false)} {
		if err := r.Upsert(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	again := tr("a", 1, simT0, true)
	again.NetProfit = 7
	if err := r.Upsert(ctx, again); err != nil {
		t.Fatal(err)
	}
	if err := r.Upsert(ctx, tr("zz", 1, simT0, true)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown account")
	}
	if err := r.Upsert(ctx, tr("a", 0, simT0, true)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid trade")
	}
	yes := true
	got, _ := r.List(ctx, "a", ports.TradeFilter{Limit: 10})
	if len(got) != 3 || got[0].PositionID != 3 || got[1].PositionID != 2 || got[2].NetProfit != 7 {
		t.Fatalf("List = %+v", got)
	}
	if got, _ := r.List(ctx, "a", ports.TradeFilter{Ours: &yes, Before: simT0.Add(time.Hour), Limit: 10}); len(got) != 1 || got[0].PositionID != 1 {
		t.Fatalf("filtered = %+v", got)
	}
	if got, _ := r.List(ctx, "a", ports.TradeFilter{From: simT0.Add(time.Minute), To: simT0.Add(2 * time.Hour), Limit: 1}); len(got) != 1 || got[0].PositionID != 3 {
		t.Fatalf("range = %+v", got)
	}
	if last, _ := r.LastClosedAt(ctx, "a"); !last.Equal(simT0.Add(time.Hour)) {
		t.Fatalf("LastClosedAt = %s", last)
	}
	_ = s.Accounts().Delete(ctx, "a")
	if got, _ := r.List(ctx, "a", ports.TradeFilter{Limit: 10}); len(got) != 0 {
		t.Fatal("trades survived their account")
	}
	s.TradesErr = errors.New("boom")
	if _, err := r.List(ctx, "b", ports.TradeFilter{Limit: 10}); err == nil {
		t.Fatal("TradesErr")
	}
	if _, err := r.LastClosedAt(ctx, "b"); err == nil {
		t.Fatal("TradesErr")
	}
	if err := r.Upsert(ctx, tr("b", 9, simT0, true)); err == nil {
		t.Fatal("TradesErr")
	}
}
