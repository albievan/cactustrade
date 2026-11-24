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
	if _, err := b.Lookup(ctx, "r1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("lookup within the lag = %v, want not found", err)
	}
	now = now.Add(time.Second)
	if st, err := b.Lookup(ctx, "r1"); err != nil || st.State != domain.OrderFilled {
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
		if st, err := b.Lookup(ctx, "r1"); err != nil || st.State != domain.OrderFilled || !st.Final || len(st.Executions) != 0 {
			t.Fatalf("lookup %d = %+v %v", i, st, err)
		}
	}
	if st, _ := b.Lookup(ctx, "r1"); len(st.Executions) != 1 {
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
	if st, err := b.Lookup(ctx, "r1"); err != nil || st.State != domain.OrderFilled || len(b.Actual().Positions) != 1 {
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
