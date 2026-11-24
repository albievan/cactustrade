package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// runRealGates covers migration 00012 (PLAN 7.12): the real-mode gate with versions (a stale
// write never overwrites a newer one: a disarm is never undone by an older arm), the cascade
// with its account, and the evidence windows of orders and agent positions (ListSince).
func runRealGates(t *testing.T, s ports.Store, ownerID string, t0 time.Time) {
	t.Helper()
	ctx := context.Background()
	real := domain.Account{ID: "acc-real-gate", Name: "Live", OwnerID: ownerID, Mode: domain.ModeReal, CreatedAt: t0, UpdatedAt: t0}
	demo := domain.Account{ID: "acc-real-gate-demo", Name: "Practice", OwnerID: ownerID, Mode: domain.ModeDemo, CreatedAt: t0, UpdatedAt: t0}
	for _, a := range []domain.Account{real, demo} {
		if err := s.Accounts().Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("real_gates", func(t *testing.T) {
		rg := s.RealGates()
		if _, err := rg.Get(ctx, real.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Get before any Put: %v", err)
		}
		ev := domain.GateEvidence{Days: []domain.RiskDate{"2026-10-01", "2026-11-20"}, Count: 2, First: "2026-10-01", Last: "2026-11-20"}
		armed := domain.LockedGate(real.ID).Arm(demo.ID, ownerID, ev, t0)
		if _, err := rg.Put(ctx, domain.RealGate{AccountID: "nope", State: domain.GateLocked, UpdatedAt: t0}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown account: %v", err)
		}
		if _, err := rg.Put(ctx, domain.RealGate{AccountID: real.ID, State: "open", UpdatedAt: t0}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid gate: %v", err)
		}
		v1, err := rg.Put(ctx, armed)
		if err != nil || v1.Version != 1 {
			t.Fatalf("Put armed = %+v %v", v1, err)
		}
		got, err := rg.Get(ctx, real.ID)
		if err != nil || got.State != domain.GateArmed || got.DemoAccountID != demo.ID || got.ArmedBy != ownerID ||
			!got.ArmedAt.Equal(t0) || !got.ExpiresAt.Equal(t0.Add(domain.RealGateTTL)) || got.Version != 1 ||
			!slices.Equal(got.Evidence.Days, ev.Days) || got.Evidence.Count != 2 || got.Evidence.First != ev.First || got.Evidence.Last != ev.Last ||
			!got.DisarmedAt.IsZero() || got.DisarmReason != "" || !got.UpdatedAt.Equal(t0) {
			t.Fatalf("Get = %+v %v", got, err)
		}
		if _, err := rg.Put(ctx, armed); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("a second first version: %v", err)
		}
		// A disarm (from version 1) wins; an arm still holding version 1 then fails.
		disarmed, err := rg.Put(ctx, got.Disarm("u-op", "manual: drill", t0.Add(time.Minute)))
		if err != nil || disarmed.Version != 2 {
			t.Fatalf("Put disarm = %+v %v", disarmed, err)
		}
		stale := got.Arm(demo.ID, ownerID, ev, t0.Add(2*time.Minute))
		if _, err := rg.Put(ctx, stale); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("a stale arm overwrote the disarm: %v", err)
		}
		got, err = rg.Get(ctx, real.ID)
		if err != nil || got.State != domain.GateLocked || got.DisarmedBy != "u-op" || got.DisarmReason != "manual: drill" ||
			!got.DisarmedAt.Equal(t0.Add(time.Minute)) || got.ArmedBy != ownerID || got.Version != 2 {
			t.Fatalf("after the stale arm = %+v %v", got, err)
		}
		// A gate without evidence reads back with none.
		none, err := rg.Put(ctx, domain.LockedGate(demo.ID).Disarm("", domain.DisarmRestore, t0))
		if err != nil || none.Version != 1 {
			t.Fatalf("Put without evidence = %+v %v", none, err)
		}
		if g, err := rg.Get(ctx, demo.ID); err != nil || len(g.Evidence.Days) != 0 || g.Evidence.Count != 0 || g.DisarmedBy != "" {
			t.Fatalf("Get without evidence = %+v %v", g, err)
		}
	})

	t.Run("evidence_windows", func(t *testing.T) {
		or, ap := s.Orders(), s.AgentPositions()
		for i, at := range []time.Time{t0.Add(-time.Hour), t0, t0.Add(time.Hour)} {
			o := newOpen(t, "ev-"+string(rune('a'+i)), demo.ID, domain.Long, at)
			if err := or.Create(ctx, o); err != nil {
				t.Fatal(err)
			}
			p := domain.AgentPosition{AccountID: demo.ID, PositionID: int64(100 - i), OpenRequestID: o.RequestID, Instrument: 1001, Direction: domain.Long, OpenedAt: at}
			if err := ap.Add(ctx, p); err != nil {
				t.Fatal(err)
			}
		}
		// Another account's rows are never listed.
		if err := or.Create(ctx, newOpen(t, "ev-other", real.ID, domain.Long, t0)); err != nil {
			t.Fatal(err)
		}
		if err := ap.Add(ctx, domain.AgentPosition{AccountID: real.ID, PositionID: 1, OpenRequestID: "ev-other", Instrument: 1, Direction: domain.Long, OpenedAt: t0}); err != nil {
			t.Fatal(err)
		}
		os, err := or.ListSince(ctx, demo.ID, t0)
		if err != nil || len(os) != 2 || os[0].RequestID != "ev-b" || os[1].RequestID != "ev-c" || !os[0].CreatedAt.Equal(t0) {
			t.Fatalf("Orders.ListSince = %+v %v", os, err)
		}
		if os, _ := or.ListSince(ctx, demo.ID, t0.Add(time.Hour+time.Microsecond)); len(os) != 0 {
			t.Fatalf("ListSince after the last = %+v", os)
		}
		ps, err := ap.ListSince(ctx, demo.ID, t0)
		if err != nil || len(ps) != 2 || ps[0].PositionID != 99 || ps[1].PositionID != 98 || !ps[0].OpenedAt.Equal(t0) {
			t.Fatalf("AgentPositions.ListSince = %+v %v", ps, err)
		}
		// Closed positions are listed too.
		closed := ps[0]
		closed.ClosedAt = t0.Add(30 * time.Minute)
		if err := ap.Update(ctx, closed); err != nil {
			t.Fatal(err)
		}
		if ps, _ := ap.ListSince(ctx, demo.ID, t0); len(ps) != 2 || ps[0].Open() {
			t.Fatalf("ListSince with a closed position = %+v", ps)
		}
		if ps, _ := ap.ListSince(ctx, "nope", time.Time{}); len(ps) != 0 {
			t.Fatalf("unknown account = %+v", ps)
		}
	})

	t.Run("real_gates_cascade", func(t *testing.T) {
		if err := s.Accounts().Delete(ctx, real.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.RealGates().Get(ctx, real.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("the gate survived its account: %v", err)
		}
		if _, err := s.RealGates().Get(ctx, demo.ID); err != nil {
			t.Fatalf("another account's gate was deleted: %v", err)
		}
		if os, _ := s.Orders().ListSince(ctx, demo.ID, time.Time{}); len(os) != 3 {
			t.Fatalf("another account's orders were deleted: %+v", os)
		}
	})
}
