package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// runExecution covers the phase-4 repositories (migration 00009): orders with compare-and-set
// updates, agent positions, risk state with versions, instrument blocks and the
// single-writer lease.
func runExecution(t *testing.T, s ports.Store, ownerID string, t0 time.Time) {
	t.Helper()
	ctx := context.Background()
	acct := domain.Account{ID: "acc-exec", Name: "Execution", OwnerID: ownerID, Mode: domain.ModeDemo, CreatedAt: t0, UpdatedAt: t0}
	other := domain.Account{ID: "acc-exec-2", Name: "Execution 2", OwnerID: ownerID, Mode: domain.ModeDemo, CreatedAt: t0, UpdatedAt: t0}
	for _, a := range []domain.Account{acct, other} {
		if err := s.Accounts().Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("orders", func(t *testing.T) { runOrders(t, s, acct.ID, other.ID, t0) })
	t.Run("agent_positions", func(t *testing.T) { runAgentPositions(t, s, acct.ID, t0) })
	t.Run("risk_state", func(t *testing.T) { runRiskState(t, s, acct.ID, t0) })
	t.Run("instrument_blocks", func(t *testing.T) { runBlocks(t, s, acct.ID, t0) })
	t.Run("agent_leases", func(t *testing.T) { runLeases(t, s, acct.ID, t0) })

	t.Run("execution_cascade", func(t *testing.T) {
		if err := s.Accounts().Delete(ctx, acct.ID); err != nil {
			t.Fatal(err)
		}
		if l, err := s.Orders().List(ctx, acct.ID, 100); err != nil || len(l) != 0 {
			t.Fatalf("orders survived their account: %d %v", len(l), err)
		}
		if l, _ := s.AgentPositions().ListOpen(ctx, acct.ID); len(l) != 0 {
			t.Fatal("agent positions survived")
		}
		if _, err := s.RiskStates().Get(ctx, acct.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("risk state survived")
		}
		if l, _ := s.InstrumentBlocks().List(ctx, acct.ID); len(l) != 0 {
			t.Fatal("blocks survived")
		}
		if _, err := s.AgentLeases().Get(ctx, acct.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("lease survived")
		}
		if l, _ := s.Orders().List(ctx, other.ID, 100); len(l) != 1 {
			t.Fatalf("another account's orders were deleted: %+v", l)
		}
	})
}

func newOpen(t *testing.T, id, acct string, d domain.Direction, at time.Time) domain.Order {
	t.Helper()
	entry, stop, tp := domain.Micros(189_123_456), domain.Micros(185_000_001), domain.Micros(199_999_999)
	if d == domain.Short {
		stop, tp = 193_000_000, 0
	}
	o, err := domain.NewOrder(domain.Order{
		RequestID: id, AccountID: acct, Mode: domain.ModeDemo, Kind: domain.OrderOpen, Direction: d,
		Instrument: 1001, Amount: 9_000_000_000_01, Entry: entry, StopLoss: stop, TakeProfit: tp,
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func runOrders(t *testing.T, s ports.Store, acct, other string, t0 time.Time) {
	ctx := context.Background()
	or := s.Orders()
	o := newOpen(t, "5f7ec7be-c156-4248-b4e3-8a5836d58eaa", acct, domain.Long, t0)
	if err := or.Create(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := or.Create(ctx, o); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate request id: %v", err)
	}
	ghost := o
	ghost.RequestID, ghost.AccountID = "ghost", "no-such-account"
	if err := or.Create(ctx, ghost); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
	got, err := or.Get(ctx, o.RequestID)
	if err != nil || got != o {
		t.Fatalf("Get = %+v %v\nwant %+v", got, err, o)
	}
	if _, err := or.Get(ctx, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get unknown: %v", err)
	}

	// created → sent, then a stale writer (still holding "created") loses
	sent, err := o.Transition(domain.OrderSent, t0.Add(time.Second), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := or.Update(ctx, o, sent); err != nil {
		t.Fatal(err)
	}
	staleAbandon, _ := o.Transition(domain.OrderAbandoned, t0.Add(2*time.Second), "stale")
	if err := or.Update(ctx, o, staleAbandon); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	if g, _ := or.Get(ctx, o.RequestID); g != sent {
		t.Fatalf("a stale update changed the row: %+v", g)
	}
	if err := or.Update(ctx, ghost, ghost); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("update unknown: %v", err)
	}
	moved := sent
	moved.RequestID = "other-id"
	if err := or.Update(ctx, sent, moved); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("request id change: %v", err)
	}

	// concurrent writers from the same version: exactly one transition wins
	targets := []domain.OrderState{domain.OrderPending, domain.OrderFilled, domain.OrderRejected, domain.OrderUnknown, domain.OrderSent, domain.OrderPending, domain.OrderCancelled, domain.OrderPartial}
	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, to := range targets {
		wg.Go(func() {
			next, err := sent.Transition(to, t0.Add(3*time.Second), fmt.Sprintf("writer %d", i))
			if err != nil {
				errs[i] = err
				return
			}
			errs[i] = or.Update(ctx, sent, next)
		})
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, domain.ErrConflict):
			t.Fatalf("concurrent update: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d concurrent transitions won, want exactly 1", wins)
	}
	cur, _ := or.Get(ctx, o.RequestID)
	if !strings.HasPrefix(cur.Reason, "writer ") {
		t.Fatalf("after the race = %+v", cur)
	}

	// final orders are not open; broker ids and a long reason round-trip
	filled := newOpen(t, "f-1", acct, domain.Short, t0.Add(time.Minute))
	_ = or.Create(ctx, filled)
	fs, _ := filled.Transition(domain.OrderSent, t0.Add(61*time.Second), "")
	_ = or.Update(ctx, filled, fs)
	ff, _ := fs.Transition(domain.OrderFilled, t0.Add(62*time.Second), strings.Repeat("é", 400))
	ff.PositionID, ff.EtoroOrderID = 9_000_000_001, 13_902_598
	if err := or.Update(ctx, fs, ff); err != nil {
		t.Fatal(err)
	}
	g, _ := or.Get(ctx, ff.RequestID)
	if g.State != domain.OrderFilled || g.PositionID != ff.PositionID || g.EtoroOrderID != ff.EtoroOrderID || len(g.Reason) != 500 || g.Sends != 1 || !g.SentAt.Equal(fs.SentAt) {
		t.Fatalf("filled = %+v (reason %d bytes)", g, len(g.Reason))
	}
	closeO, err := domain.NewOrder(domain.Order{
		RequestID: "c-1", AccountID: acct, Mode: domain.ModeDemo, Kind: domain.OrderClose, Direction: domain.Short,
		Instrument: 1001, PositionID: 9_000_000_001,
	}, t0.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	cancelO, err := domain.NewOrder(domain.Order{RequestID: "x-1", AccountID: acct, Mode: domain.ModeDemo, Kind: domain.OrderCancel, TargetOrderID: 13_902_599}, t0.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []domain.Order{closeO, cancelO, newOpen(t, "o-other", other, domain.Long, t0)} {
		if err := or.Create(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	if g, _ := or.Get(ctx, "x-1"); g != cancelO {
		t.Fatalf("cancel order = %+v", g)
	}
	// the first order is open unless the race above ended in a final state
	want := []string{"c-1", "x-1"}
	if !cur.State.Final() {
		want = append([]string{o.RequestID}, want...)
	}
	open, err := or.ListOpen(ctx, acct)
	var ids []string
	for _, x := range open {
		ids = append(ids, x.RequestID)
		if x.State.Final() || x.AccountID != acct {
			t.Fatalf("ListOpen returned %+v", x)
		}
	}
	if err != nil || strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("ListOpen = %v %v, want %v", ids, err, want)
	}
	all, err := or.List(ctx, acct, 2)
	if err != nil || len(all) != 2 || all[0].RequestID != "x-1" || all[1].RequestID != "c-1" {
		t.Fatalf("List = %+v %v", all, err)
	}
	if all, _ := or.List(ctx, acct, 100); len(all) != 4 {
		t.Fatalf("List all = %d", len(all))
	}
}

func runAgentPositions(t *testing.T, s ports.Store, acct string, t0 time.Time) {
	ctx := context.Background()
	ap := s.AgentPositions()
	p1 := domain.AgentPosition{AccountID: acct, PositionID: 9_000_000_001, OpenRequestID: "f-1", Instrument: 1001, Direction: domain.Short, OpenedAt: t0}
	p2 := domain.AgentPosition{AccountID: acct, PositionID: 2, OpenRequestID: "f-2", Instrument: 1002, Direction: domain.Long, OpenedAt: t0.Add(time.Minute)}
	for _, p := range []domain.AgentPosition{p2, p1} {
		if err := ap.Add(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := ap.Add(ctx, p1); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	if err := ap.Add(ctx, domain.AgentPosition{AccountID: "nope", PositionID: 1, OpenedAt: t0}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
	if g, err := ap.Get(ctx, acct, p1.PositionID); err != nil || g != p1 {
		t.Fatalf("Get = %+v %v", g, err)
	}
	if _, err := ap.Get(ctx, acct, 3); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("Get unknown")
	}
	open, err := ap.ListOpen(ctx, acct)
	if err != nil || len(open) != 2 || open[0].PositionID != p1.PositionID || !open[0].Open() {
		t.Fatalf("ListOpen = %+v %v", open, err)
	}
	p1.CloseRequestID = "c-1"
	if err := ap.Update(ctx, p1); err != nil {
		t.Fatal(err)
	}
	if open, _ := ap.ListOpen(ctx, acct); len(open) != 2 || open[0].CloseRequestID != "c-1" {
		t.Fatal("a close in flight is still open")
	}
	p1.ClosedAt = t0.Add(time.Hour)
	p1.Instrument = 7 // never changes
	if err := ap.Update(ctx, p1); err != nil {
		t.Fatal(err)
	}
	if g, _ := ap.Get(ctx, acct, p1.PositionID); g.Open() || !g.ClosedAt.Equal(p1.ClosedAt) || g.Instrument != 1001 {
		t.Fatalf("closed = %+v", g)
	}
	if open, _ := ap.ListOpen(ctx, acct); len(open) != 1 || open[0].PositionID != 2 {
		t.Fatalf("ListOpen after close = %+v", open)
	}
	if err := ap.Update(ctx, domain.AgentPosition{AccountID: acct, PositionID: 3}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("update unknown: %v", err)
	}
}

func runRiskState(t *testing.T, s ports.Store, acct string, t0 time.Time) {
	ctx := context.Background()
	rs := s.RiskStates()
	if _, err := rs.Get(ctx, acct); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("no state yet: %v", err)
	}
	st := domain.AccountRiskState{AccountID: acct, UpdatedAt: t0, State: domain.RiskState{
		Day: domain.RiskDay(t0), StartOfDayEquity: 10_000_00, HighWaterMark: 12_000_00, Reserved: 5, ReservedOrders: 1, ReservedShort: 2,
	}}
	v1, err := rs.Put(ctx, st)
	if err != nil || v1.Version != 1 || v1.State.Reserved != 0 || v1.State.ReservedOrders != 0 {
		t.Fatalf("insert = %+v %v", v1, err)
	}
	if _, err := rs.Put(ctx, st); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second insert (upsert in Go, version 0): %v", err)
	}
	if g, err := rs.Get(ctx, acct); err != nil || g != v1 {
		t.Fatalf("Get = %+v %v\nwant %+v", g, err, v1)
	}
	// the supervisor observes, a kill arrives from an older read: the kill write must not
	// overwrite the observation silently (and vice versa)
	observed := v1
	observed.State.DayStopped, observed.State.StartOfDayEquity, observed.UpdatedAt = true, 9_000_00, t0.Add(time.Second)
	v2, err := rs.Put(ctx, observed)
	if err != nil || v2.Version != 2 {
		t.Fatalf("update = %+v %v", v2, err)
	}
	killed := v1
	killed.State.Killed, killed.KillReason, killed.KilledAt = true, "drawdown", t0.Add(2*time.Second)
	if _, err := rs.Put(ctx, killed); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale version: %v", err)
	}
	killed.Version = v2.Version
	killed.State = v2.State
	killed.State.Killed = true
	v3, err := rs.Put(ctx, killed)
	if err != nil {
		t.Fatal(err)
	}
	g, _ := rs.Get(ctx, acct)
	if g != v3 || !g.State.Killed || !g.State.DayStopped || g.KillReason != "drawdown" || !g.KilledAt.Equal(killed.KilledAt) || g.Version != 3 {
		t.Fatalf("after kill = %+v", g)
	}
	// concurrent writers of one version: exactly one wins
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() {
			x := v3
			x.State.HighWaterMark += domain.Cents(i + 1)
			_, errs[i] = rs.Put(ctx, x)
		})
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, domain.ErrConflict):
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d concurrent risk-state writers won", wins)
	}
	if _, err := rs.Put(ctx, domain.AccountRiskState{AccountID: "nope", UpdatedAt: t0}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
}

func runBlocks(t *testing.T, s ports.Store, acct string, t0 time.Time) {
	ctx := context.Background()
	bl := s.InstrumentBlocks()
	for _, b := range []domain.InstrumentBlock{
		{AccountID: acct, Instrument: 2002, Reason: "leverage 2 fill", At: t0},
		{AccountID: acct, Instrument: 1001, Reason: "no stop", At: t0},
		{AccountID: acct, Instrument: 2002, Reason: "cfd long fill", At: t0.Add(time.Minute)}, // refreshed
	} {
		if err := bl.Block(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if err := bl.Block(ctx, domain.InstrumentBlock{AccountID: "nope", Instrument: 1, At: t0}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
	l, err := bl.List(ctx, acct)
	if err != nil || len(l) != 2 || l[0].Instrument != 1001 || l[1].Reason != "cfd long fill" || !l[1].At.Equal(t0.Add(time.Minute)) {
		t.Fatalf("List = %+v %v", l, err)
	}
	if err := bl.Unblock(ctx, acct, 1001); err != nil {
		t.Fatal(err)
	}
	if err := bl.Unblock(ctx, acct, 1001); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unblock twice: %v", err)
	}
	if l, _ := bl.List(ctx, "acc-exec-2"); len(l) != 0 {
		t.Fatal("blocks leak across accounts")
	}
}

func runLeases(t *testing.T, s ports.Store, acct string, t0 time.Time) {
	ctx := context.Background()
	ls := s.AgentLeases()
	if _, err := ls.Get(ctx, acct); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("no lease yet: %v", err)
	}
	until := t0.Add(domain.LeaseTTL)
	holders := make([]string, 8)
	errs := make([]error, len(holders))
	var wg sync.WaitGroup
	for i := range holders {
		holders[i] = fmt.Sprintf("proc-%d", i)
		wg.Go(func() { errs[i] = ls.Acquire(ctx, acct, holders[i], t0, until) })
	}
	wg.Wait()
	winner := ""
	for i, err := range errs {
		switch {
		case err == nil && winner == "":
			winner = holders[i]
		case err == nil:
			t.Fatalf("two lease holders: %s and %s", winner, holders[i])
		case !errors.Is(err, domain.ErrConflict):
			t.Fatal(err)
		}
	}
	if winner == "" {
		t.Fatal("nobody won the lease")
	}
	if l, err := ls.Get(ctx, acct); err != nil || l.Holder != winner || !l.ExpiresAt.Equal(until) {
		t.Fatalf("Get = %+v %v", l, err)
	}
	// the holder renews; another process cannot take a live lease
	if err := ls.Acquire(ctx, acct, winner, t0.Add(10*time.Second), t0.Add(40*time.Second)); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if err := ls.Acquire(ctx, acct, "intruder", t0.Add(39*time.Second), t0.Add(69*time.Second)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("live lease taken: %v", err)
	}
	// expired at now: taken over
	if err := ls.Acquire(ctx, acct, "successor", t0.Add(40*time.Second), t0.Add(70*time.Second)); err != nil {
		t.Fatalf("expired lease not taken over: %v", err)
	}
	if err := ls.Acquire(ctx, acct, winner, t0.Add(41*time.Second), t0.Add(71*time.Second)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("old holder came back: %v", err)
	}
	if err := ls.Release(ctx, acct, winner); err != nil { // not the holder: no effect
		t.Fatal(err)
	}
	if l, _ := ls.Get(ctx, acct); l.Holder != "successor" {
		t.Fatalf("a non-holder released the lease: %+v", l)
	}
	if err := ls.Release(ctx, acct, "successor"); err != nil {
		t.Fatal(err)
	}
	if _, err := ls.Get(ctx, acct); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("released lease still there")
	}
	if err := ls.Acquire(ctx, "nope", "x", t0, until); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
	if err := ls.Acquire(ctx, acct, "again", t0, until); err != nil {
		t.Fatal(err)
	}
}
