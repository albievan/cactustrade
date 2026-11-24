package fakes

import (
	"errors"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// The eToro risks the simulator can play (review M4): lookup lag (U2), portfolio lag of fills
// (U11), a fill reported without executions, a 429 that was processed (U7), a partial close,
// and a repeated request id that silently fills again (U1).

func TestSimBrokerLookupLag(t *testing.T) {
	now := simT0
	b := &SimBroker{Now: func() time.Time { return now }, Cash: 1_000_00, LookupLag: 3 * time.Second}
	ctx := demoCtx()
	if _, err := b.Open(ctx, longIntent("r1")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if _, err := b.Lookup(ctx, "r1", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("lookup within the lag = %v, want not found", err)
	}
	now = now.Add(time.Second)
	if st, err := b.Lookup(ctx, "r1", 0); err != nil || st.State != domain.OrderFilled {
		t.Fatalf("lookup after the lag = %+v %v", st, err)
	}
	if b.LaggedLookups != 1 {
		t.Fatalf("lagged lookups = %d", b.LaggedLookups)
	}
}

func TestSimBrokerPortfolioLag(t *testing.T) {
	now := simT0
	b := &SimBroker{Now: func() time.Time { return now }, Cash: 1_000_00, PortfolioLag: 10 * time.Second}
	ctx := demoCtx()
	if _, err := b.Open(ctx, longIntent("r1")); err != nil {
		t.Fatal(err)
	}
	pf, _ := b.Portfolio(ctx)
	if len(pf.Positions) != 0 || pf.AvailableCash != 1_000_00 || pf.Equity() != 1_000_00 {
		t.Fatalf("lagged portfolio = %+v: the fill must not show yet and the cash not be paid", pf)
	}
	if act := b.Actual(); len(act.Positions) != 1 || act.AvailableCash != 900_00 {
		t.Fatalf("actual = %+v", act)
	}
	if b.HiddenFills != 1 {
		t.Fatalf("hidden fills = %d", b.HiddenFills)
	}
	now = now.Add(10 * time.Second)
	if pf, _ := b.Portfolio(ctx); len(pf.Positions) != 1 || pf.AvailableCash != 900_00 {
		t.Fatalf("after the lag = %+v", pf)
	}
}

func TestSimBrokerFilledWithoutExecutions(t *testing.T) {
	b := &SimBroker{Now: func() time.Time { return simT0 }, Cash: 1_000_00, OpenScript: []SimOutcome{SimFilledNoExecutions}, NoExecLookups: 2}
	ctx := demoCtx()
	if _, err := b.Open(ctx, longIntent("r1")); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if st, err := b.Lookup(ctx, "r1", 0); err != nil || st.State != domain.OrderFilled || !st.Final || len(st.Executions) != 0 {
			t.Fatalf("lookup %d = %+v %v", i, st, err)
		}
	}
	if st, _ := b.Lookup(ctx, "r1", 0); len(st.Executions) != 1 {
		t.Fatalf("executions must show in the end: %+v", st)
	}
	if b.Outcomes[SimFilledNoExecutions] != 1 {
		t.Fatalf("outcomes = %v", b.Outcomes)
	}
}

func TestSimBrokerRateLimitedButProcessed(t *testing.T) {
	b := &SimBroker{Now: func() time.Time { return simT0 }, Cash: 1_000_00, OpenScript: []SimOutcome{SimRateLimitedProcessed}}
	ctx := demoCtx()
	_, err := b.Open(ctx, longIntent("r1"))
	if na, ok := errors.AsType[*domain.NotAcceptedError](err); !ok || !na.RateLimited {
		t.Fatalf("open = %v, want a 429", err)
	}
	if st, err := b.Lookup(ctx, "r1", 0); err != nil || st.State != domain.OrderFilled || len(b.Actual().Positions) != 1 {
		t.Fatalf("a processed 429 must exist: %+v %v", st, err)
	}
}

func TestSimBrokerPartialClose(t *testing.T) {
	b := &SimBroker{
		Now: func() time.Time { return simT0 }, Positions: []domain.Position{{ID: 1, Instrument: 10, Amount: 100_00, Units: 2_000_000, Long: true, RealAsset: true, Leverage: 1}},
		CloseScript: []SimOutcome{SimPartialClose},
	}
	ctx := demoCtx()
	st, err := b.Close(ctx, 1, 10, "k1")
	if err != nil {
		t.Fatal(err)
	}
	co, err := b.CloseOrder(ctx, st.OrderID)
	if err != nil || co.State != domain.OrderPartial || !co.Final {
		t.Fatalf("close order = %+v %v", co, err)
	}
	pf, _ := b.Portfolio(ctx)
	if len(pf.Positions) != 1 || pf.Positions[0].Amount != 50_00 || pf.Positions[0].Units != 1_000_000 || pf.AvailableCash != 50_00 {
		t.Fatalf("after a partial close = %+v", pf)
	}
	if b.Outcomes[SimPartialClose] != 1 {
		t.Fatalf("outcomes = %v", b.Outcomes)
	}
}

func TestSimBrokerDuplicateFills(t *testing.T) {
	b := &SimBroker{Now: func() time.Time { return simT0 }, Cash: 1_000_00, DuplicateFills: true}
	ctx := demoCtx()
	for range 2 {
		if _, err := b.Open(ctx, longIntent("r1")); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.Actual().Positions) != 2 || b.Duplicates != 1 || len(b.Violations) != 1 {
		t.Fatalf("a repeated id must fill again silently and be recorded: positions %d duplicates %d violations %v",
			len(b.Actual().Positions), b.Duplicates, b.Violations)
	}
}

// TestSimBrokerRefLookupNotFound plays eToro's live answer of 2026-11-24: a lookup by our
// request id is always 404, a lookup by eToro's order id finds the order.
func TestSimBrokerRefLookupNotFound(t *testing.T) {
	b := &SimBroker{Now: func() time.Time { return simT0 }, Cash: 1_000_00, RefLookupNotFound: true}
	ctx := demoCtx()
	st, err := b.Open(ctx, longIntent("r1"))
	if err != nil || st.OrderID == 0 {
		t.Fatalf("open = %+v %v", st, err)
	}
	if _, err := b.Lookup(ctx, "r1", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("lookup by request id = %v, want not found", err)
	}
	got, err := b.Lookup(ctx, "r1", st.OrderID)
	if err != nil || got.State != domain.OrderFilled || got.OrderID != st.OrderID || len(got.Executions) != 1 {
		t.Fatalf("lookup by order id = %+v %v", got, err)
	}
	if _, err := b.Lookup(ctx, "r1", st.OrderID+1_000); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("lookup of an order id the broker never gave = %v", err)
	}
	if b.RefNotFound != 1 {
		t.Fatalf("ref lookups answered 404 = %d", b.RefNotFound)
	}
}

// TestSimBrokerIgnoredCancels: a cancel is accepted (200) but the order is left as it is,
// as eToro did for orders that had already filled (2026-11-24).
func TestSimBrokerIgnoredCancels(t *testing.T) {
	b := &SimBroker{Now: func() time.Time { return simT0 }, Cash: 1_000_00, OpenScript: []SimOutcome{SimPending}, IgnoreCancels: true}
	ctx := demoCtx()
	st, err := b.Open(ctx, longIntent("r1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Cancel(ctx, st.OrderID, "c1"); err != nil {
		t.Fatalf("cancel = %v", err)
	}
	if got, _ := b.Lookup(ctx, "r1", st.OrderID); got.State != domain.OrderPending || got.Final {
		t.Fatalf("after an ignored cancel = %+v", got)
	}
	if b.Cancels != 1 {
		t.Fatalf("cancels = %d", b.Cancels)
	}
}
