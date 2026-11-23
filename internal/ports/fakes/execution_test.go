package fakes

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// TestFakeExecutionRepos checks the in-memory repositories follow the port contracts that the
// shared SQL suite (storetest) checks for SQLite and MariaDB.
func TestFakeExecutionRepos(t *testing.T) {
	ctx := context.Background()
	s := NewStore()
	_ = s.Users().Create(ctx, domain.User{ID: "u", Username: "u", Role: domain.RoleAdmin})
	_ = s.Accounts().Create(ctx, domain.Account{ID: "a", Name: "A", OwnerID: "u", Mode: domain.ModeDemo})

	o, _ := domain.NewOrder(domain.Order{
		RequestID: "r1", AccountID: "a", Mode: domain.ModeDemo, Kind: domain.OrderOpen,
		Direction: domain.Long, Instrument: 1, Amount: 100, Entry: 10_000_000, StopLoss: 9_000_000,
	}, simT0)
	or := s.Orders()
	if err := or.Create(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := or.Create(ctx, o); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("duplicate")
	}
	bad := o
	bad.RequestID, bad.AccountID = "r9", "nope"
	if err := or.Create(ctx, bad); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown account")
	}
	sent, _ := o.Transition(domain.OrderSent, simT0.Add(time.Second), "")
	if err := or.Update(ctx, o, sent); err != nil {
		t.Fatal(err)
	}
	if err := or.Update(ctx, o, sent); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale update")
	}
	if err := or.Update(ctx, bad, bad); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown order")
	}
	filled, _ := sent.Transition(domain.OrderFilled, simT0.Add(2*time.Second), "done")
	filled.PositionID, filled.EtoroOrderID = 7, 8
	if err := or.Update(ctx, sent, filled); err != nil {
		t.Fatal(err)
	}
	if g, _ := or.Get(ctx, "r1"); g.State != domain.OrderFilled || g.PositionID != 7 || g.Sends != 1 {
		t.Fatalf("Get = %+v", g)
	}
	if _, err := or.Get(ctx, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("Get unknown")
	}
	o2 := o
	o2.RequestID, o2.CreatedAt = "r2", simT0.Add(time.Minute)
	_ = or.Create(ctx, o2)
	if open, _ := or.ListOpen(ctx, "a"); len(open) != 1 || open[0].RequestID != "r2" {
		t.Fatalf("ListOpen = %+v", open)
	}
	if all, _ := or.List(ctx, "a", 1); len(all) != 1 || all[0].RequestID != "r2" {
		t.Fatalf("List = %+v", all)
	}
	s.OrdersErr = errors.New("disk full")
	if err := or.Create(ctx, o); err == nil || or.Update(ctx, o2, o2) == nil {
		t.Fatal("OrdersErr")
	}
	s.OrdersErr = nil

	ap := s.AgentPositions()
	p := domain.AgentPosition{AccountID: "a", PositionID: 7, OpenRequestID: "r1", Instrument: 1, Direction: domain.Long, OpenedAt: simT0}
	if err := ap.Add(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := ap.Add(ctx, p); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("duplicate position")
	}
	if err := ap.Add(ctx, domain.AgentPosition{AccountID: "nope"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown account")
	}
	if open, _ := ap.ListOpen(ctx, "a"); len(open) != 1 {
		t.Fatal("ListOpen")
	}
	p.ClosedAt, p.CloseRequestID, p.Instrument = simT0.Add(time.Hour), "k1", 99
	if err := ap.Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if g, _ := ap.Get(ctx, "a", 7); g.Open() || g.CloseRequestID != "k1" || g.Instrument != 1 {
		t.Fatalf("Get = %+v", g)
	}
	if err := ap.Update(ctx, domain.AgentPosition{AccountID: "a", PositionID: 8}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("update unknown")
	}
	if _, err := ap.Get(ctx, "a", 8); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("get unknown")
	}
	if open, _ := ap.ListOpen(ctx, "a"); len(open) != 0 {
		t.Fatal("closed listed as open")
	}

	rs := s.RiskStates()
	if _, err := rs.Get(ctx, "a"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("no state yet")
	}
	st := domain.AccountRiskState{AccountID: "a", State: domain.RiskState{Day: "2026-11-23", StartOfDayEquity: 100, HighWaterMark: 100, Reserved: 5}}
	v1, err := rs.Put(ctx, st)
	if err != nil || v1.Version != 1 || v1.State.Reserved != 0 {
		t.Fatalf("Put = %+v %v", v1, err)
	}
	if _, err := rs.Put(ctx, st); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("second insert")
	}
	v1.State.Killed = true
	if v2, err := rs.Put(ctx, v1); err != nil || v2.Version != 2 {
		t.Fatal(err)
	}
	if _, err := rs.Put(ctx, v1); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale put")
	}
	if _, err := rs.Put(ctx, domain.AccountRiskState{AccountID: "nope"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown account")
	}

	bl := s.InstrumentBlocks()
	for _, id := range []domain.InstrumentID{5, 3} {
		if err := bl.Block(ctx, domain.InstrumentBlock{AccountID: "a", Instrument: id, Reason: "x", At: simT0}); err != nil {
			t.Fatal(err)
		}
	}
	if err := bl.Block(ctx, domain.InstrumentBlock{AccountID: "nope"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown account")
	}
	if l, _ := bl.List(ctx, "a"); len(l) != 2 || l[0].Instrument != 3 {
		t.Fatalf("List = %+v", l)
	}
	if err := bl.Unblock(ctx, "a", 3); err != nil || bl.Unblock(ctx, "a", 3) == nil {
		t.Fatal("Unblock")
	}

	ls := s.AgentLeases()
	var wg sync.WaitGroup
	wins := make(chan string, 8)
	for i := range 8 {
		wg.Go(func() {
			h := string(rune('A' + i))
			if ls.Acquire(ctx, "a", h, simT0, simT0.Add(domain.LeaseTTL)) == nil {
				wins <- h
			}
		})
	}
	wg.Wait()
	close(wins)
	if len(wins) != 1 {
		t.Fatalf("%d lease winners", len(wins))
	}
	winner := <-wins
	if err := ls.Acquire(ctx, "a", winner, simT0.Add(time.Second), simT0.Add(31*time.Second)); err != nil {
		t.Fatal("renew")
	}
	if err := ls.Acquire(ctx, "a", "other", simT0.Add(31*time.Second), simT0.Add(61*time.Second)); err != nil {
		t.Fatal("expired lease not taken over")
	}
	_ = ls.Release(ctx, "a", winner) // not the holder any more: nothing happens
	if l, _ := ls.Get(ctx, "a"); l.Holder != "other" {
		t.Fatalf("lease = %+v", l)
	}
	_ = ls.Release(ctx, "a", "other")
	if _, err := ls.Get(ctx, "a"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("released")
	}
	if err := ls.Acquire(ctx, "nope", "x", simT0, simT0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown account")
	}

	// deleting the account cascades
	_ = ls.Acquire(ctx, "a", "x", simT0, simT0.Add(time.Minute))
	if err := s.Accounts().Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if l, _ := or.List(ctx, "a", 10); len(l) != 0 {
		t.Fatal("orders survived")
	}
	if _, err := rs.Get(ctx, "a"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("risk state survived")
	}
}
