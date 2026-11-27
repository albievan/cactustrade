package fakes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

var simT0 = time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)

func demoCtx() context.Context {
	return ports.WithAccount(context.Background(), domain.Account{ID: "acc-1", Name: "Demo", Mode: domain.ModeDemo})
}

func longIntent(id string) domain.OrderIntent {
	return domain.OrderIntent{RequestID: id, Direction: domain.Long, Instrument: 1001, Amount: 100_00, StopLossRate: 95_000_000, TakeProfitRate: 110_000_000}
}

func shortIntent(id string) domain.OrderIntent {
	return domain.OrderIntent{RequestID: id, Direction: domain.Short, Instrument: 2002, Amount: 50_00, StopLossRate: 105_000_000}
}

func TestSimBrokerOpenOutcomes(t *testing.T) {
	tests := []struct {
		name      string
		outcome   SimOutcome
		intent    domain.OrderIntent
		openErr   error
		rateLimit bool
		state     domain.OrderState // first lookup
		final     bool
		lookupErr error
		conforms  bool
		positions int
	}{
		{"fill long", SimFill, longIntent("r1"), nil, false, domain.OrderFilled, true, nil, true, 1},
		{"fill short", SimFill, shortIntent("r1"), nil, false, domain.OrderFilled, true, nil, true, 1},
		{"pending then fill", SimPendingThenFill, longIntent("r1"), nil, false, domain.OrderPending, false, nil, true, 0},
		{"pending", SimPending, longIntent("r1"), nil, false, domain.OrderPending, false, nil, true, 0},
		{"rejected", SimReject, longIntent("r1"), nil, false, domain.OrderRejected, true, nil, true, 0},
		{"partial", SimPartial, longIntent("r1"), nil, false, domain.OrderPartial, false, nil, true, 1},
		{"lost answer", SimLostAnswer, longIntent("r1"), domain.ErrOutcomeUnknown, false, domain.OrderFilled, true, nil, true, 1},
		{"429", SimRateLimited, longIntent("r1"), domain.ErrNotAccepted, true, domain.OrderUnknown, false, domain.ErrNotFound, true, 0},
		{"5xx", SimServerError, longIntent("r1"), domain.ErrOutcomeUnknown, false, domain.OrderUnknown, false, domain.ErrNotFound, true, 0},
		{"4xx", SimRefused, longIntent("r1"), domain.ErrNotAccepted, false, domain.OrderUnknown, false, domain.ErrNotFound, true, 0},
		{"leverage 2", SimLeverage2, longIntent("r1"), nil, false, domain.OrderFilled, true, nil, false, 1},
		{"cfd long", SimWrongSettlement, longIntent("r1"), nil, false, domain.OrderFilled, true, nil, false, 1},
		{"real short", SimWrongSettlement, shortIntent("r1"), nil, false, domain.OrderFilled, true, nil, false, 1},
		{"missing stop", SimNoStop, longIntent("r1"), nil, false, domain.OrderFilled, true, nil, false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &SimBroker{Now: func() time.Time { return simT0 }, Cash: 1_000_00, OpenScript: []SimOutcome{tt.outcome}}
			ctx := demoCtx()
			st, err := b.Open(ctx, tt.intent)
			if !errors.Is(err, tt.openErr) || (tt.openErr == nil && err != nil) {
				t.Fatalf("Open = %+v %v, want %v", st, err, tt.openErr)
			}
			if na, ok := errors.AsType[*domain.NotAcceptedError](err); ok && (na.RateLimited != tt.rateLimit || (tt.rateLimit && na.RetryAfter != SimRetryAfter)) {
				t.Fatalf("NotAccepted = %+v", na)
			}
			if err == nil && (st.State != domain.OrderPending || st.OrderID == 0 || st.RequestID != tt.intent.RequestID) {
				t.Fatalf("accepted = %+v", st)
			}
			got, err := b.Lookup(ctx, tt.intent.RequestID, 0)
			if !errors.Is(err, tt.lookupErr) || got.State != tt.state || got.Final != tt.final {
				t.Fatalf("Lookup = %+v %v", got, err)
			}
			pf, _ := b.Portfolio(ctx)
			if len(pf.Positions) != tt.positions {
				t.Fatalf("positions = %+v", pf.Positions)
			}
			if len(got.Executions) > 0 {
				if ok, why := got.Conforms(tt.intent.Direction); ok != tt.conforms {
					t.Fatalf("Conforms = %v %s", ok, why)
				}
				// a missing stop is not a shape problem: ExitsDue closes it (ExitNoStop)
				if pf.Positions[0].Conforms() != (tt.conforms || tt.outcome == SimNoStop) {
					t.Fatalf("position conforms = %v", pf.Positions[0])
				}
			}
			if len(b.Intents) != 1 || b.Intents[0] != tt.intent || b.Opens != 1 || len(b.Violations) != 0 {
				t.Fatalf("records: %+v %d %v", b.Intents, b.Opens, b.Violations)
			}
		})
	}
}

func TestSimBrokerLifecycle(t *testing.T) {
	b := &SimBroker{
		Now: func() time.Time { return simT0 }, Cash: 1_000_00, PendingLookups: 2,
		OpenScript: []SimOutcome{SimRateLimited, SimPendingThenFill, SimPending, SimPartial},
		Prices:     map[domain.InstrumentID]domain.Micros{1001: 100_000_000},
	}
	ctx := demoCtx()
	// a 429, then the same id again is processed (the broker never recorded it)
	if _, err := b.Open(ctx, longIntent("r1")); !errors.Is(err, domain.ErrNotAccepted) {
		t.Fatal(err)
	}
	if st, err := b.Open(ctx, longIntent("r1")); err != nil || st.OrderID == 0 {
		t.Fatal(st, err)
	}
	for i, want := range []domain.OrderState{domain.OrderPending, domain.OrderPending, domain.OrderFilled} {
		if got, _ := b.Lookup(ctx, "r1", 0); got.State != want {
			t.Fatalf("lookup %d = %s", i, got.State)
		}
	}
	got, _ := b.Lookup(ctx, "r1", 0)
	if e := got.Executions[0]; e.OpenRate != 100_000_000 || e.Units != 1_000_000 || e.Amount != 100_00 || e.StopLossRate != 95_000_000 {
		t.Fatalf("execution = %+v", e)
	}
	// resending an id the broker has is a violation
	if _, err := b.Open(ctx, longIntent("r1")); !errors.Is(err, domain.ErrNotAccepted) || len(b.Violations) != 1 {
		t.Fatalf("duplicate: %v %v", err, b.Violations)
	}
	// pending → cancelled; reservation shows as pending cash until then
	p, _ := b.Open(ctx, shortIntent("r2"))
	if pf, _ := b.Portfolio(ctx); pf.PendingOpen != 50_00 || pf.AvailableCash != 900_00 || !pf.AsOf.Equal(simT0) {
		t.Fatalf("portfolio = %+v", pf)
	}
	if err := b.Cancel(ctx, p.OrderID, "c1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.Lookup(ctx, "r2", 0); got.State != domain.OrderCancelled || !got.Final {
		t.Fatalf("cancelled = %+v", got)
	}
	if err := b.Cancel(ctx, p.OrderID, "c2"); err != nil { // idempotent on a final order
		t.Fatal(err)
	}
	if err := b.Cancel(ctx, 1, "c3"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown order: %v", err)
	}
	// partial → cancel keeps the fill, final
	pp, _ := b.Open(ctx, longIntent("r3"))
	if pf, _ := b.Portfolio(ctx); pf.PendingOpen != 50_00 {
		t.Fatalf("partial pending = %v", pf.PendingOpen)
	}
	_ = b.Cancel(ctx, pp.OrderID, "c4")
	if got, _ := b.Lookup(ctx, "r3", 0); got.State != domain.OrderPartial || !got.Final || len(got.Executions) != 1 || got.Executions[0].Amount != 50_00 {
		t.Fatalf("partial cancelled = %+v", got)
	}
	if b.Cancels != 4 || strings.Join(b.CancelRequests, ",") != "c1,c2,c3,c4" {
		t.Fatalf("cancels %d %v", b.Cancels, b.CancelRequests)
	}
}

func TestSimBrokerClose(t *testing.T) {
	b := &SimBroker{
		Now: func() time.Time { return simT0 }, Cash: 0,
		Positions:   []domain.Position{{ID: 1, Instrument: 10, Amount: 100_00, UnrealisedPnL: 5_00, Long: true, RealAsset: true, Leverage: 1}, {ID: 2, Instrument: 20, Amount: 10_00}, {ID: 3, Instrument: 30, Amount: 1}},
		CloseScript: []SimOutcome{SimServerError, SimRateLimited, SimRefused, SimLostAnswer, SimFill, SimPending},
	}
	ctx := demoCtx()
	if _, err := b.Close(ctx, 1, 10, "k1"); !errors.Is(err, domain.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	if _, err := b.Close(ctx, 1, 10, "k1"); !errors.Is(err, domain.ErrNotAccepted) {
		t.Fatal(err)
	}
	if _, err := b.Close(ctx, 1, 10, "k1"); !errors.Is(err, domain.ErrNotAccepted) {
		t.Fatal(err)
	}
	if pf, _ := b.Portfolio(ctx); len(pf.Positions) != 3 {
		t.Fatal("closed by a failed request")
	}
	if _, err := b.Close(ctx, 1, 10, "k1"); !errors.Is(err, domain.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	if pf, _ := b.Portfolio(ctx); len(pf.Positions) != 2 || pf.AvailableCash != 105_00 {
		t.Fatalf("lost close answer must still close: %+v", pf)
	}
	st, err := b.Close(ctx, 2, 20, "k2")
	if err != nil || st.OrderID == 0 || st.State != domain.OrderPending {
		t.Fatalf("close = %+v %v", st, err)
	}
	co, err := b.CloseOrder(ctx, st.OrderID)
	if err != nil || co.State != domain.OrderFilled || !co.Final || co.Executions[0].PositionID != 2 {
		t.Fatalf("close order = %+v %v", co, err)
	}
	pend, _ := b.Close(ctx, 3, 30, "k3")
	if co, _ := b.CloseOrder(ctx, pend.OrderID); co.State != domain.OrderPending || co.Final {
		t.Fatalf("pending close = %+v", co)
	}
	if _, err := b.Close(ctx, 99, 10, "k4"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown position: %v", err)
	}
	if _, err := b.CloseOrder(ctx, 12345); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown close order")
	}
	if b.Closes != 7 || b.CloseRequests[0] != "k1" || b.CloseRequests[3] != "k1" {
		t.Fatalf("records %d %v", b.Closes, b.CloseRequests)
	}
}

// TestSimBrokerRealPermits (PLAN 7.20, S4): the simulated broker mirrors the eToro client: a
// real-money open needs an entry permit for the account valid at its clock; a close or cancel
// an exit or entry permit for the account.
func TestSimBrokerRealPermits(t *testing.T) {
	now := simT0
	acct := domain.Account{ID: "r", Name: "Live", Mode: domain.ModeReal}
	b := &SimBroker{Now: func() time.Time { return now }, Cash: 1_000_00}
	gate := domain.LockedGate("r").Arm("d", "u", domain.GateEvidence{}, now.Add(-time.Hour))
	entry, err := domain.PermitEntry(gate, acct, now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	exit, err := domain.PermitExit(acct, true)
	if err != nil {
		t.Fatal(err)
	}
	other, err := domain.PermitExit(domain.Account{ID: "r2", Mode: domain.ModeReal}, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := func(p domain.RealPermit) context.Context {
		return ports.WithPermit(ports.WithAccount(context.Background(), acct), p)
	}
	if _, err := b.Open(ctx(exit), longIntent("x1")); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("open with an exit permit: %v", err)
	}
	if _, err := b.Close(ctx(other), 1, 1, "c0"); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("close with another account's permit: %v", err)
	}
	st, err := b.Open(ctx(entry), longIntent("r1"))
	if err != nil || st.OrderID == 0 {
		t.Fatalf("open with an entry permit: %+v %v", st, err)
	}
	if err := b.Cancel(ctx(exit), st.OrderID, "c1"); err != nil {
		t.Fatalf("cancel with an exit permit: %v", err)
	}
	now = now.Add(domain.RealPermitTTL + time.Second)
	if _, err := b.Open(ctx(entry), longIntent("r2")); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("open with an expired entry permit: %v", err)
	}
	if len(b.Violations) != 3 {
		t.Fatalf("violations %v", b.Violations)
	}
}

func TestSimBrokerGatesAndReads(t *testing.T) {
	b := &SimBroker{}
	realCtx := ports.WithAccount(context.Background(), domain.Account{ID: "r", Mode: domain.ModeReal})
	if _, err := b.Open(realCtx, longIntent("r1")); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("real open: %v", err)
	}
	if _, err := b.Close(realCtx, 1, 1, "k"); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatal("real close")
	}
	if err := b.Cancel(realCtx, 1, "c"); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatal("real cancel")
	}
	if _, err := b.Portfolio(realCtx); err != nil { // reads of a real account are allowed
		t.Fatal(err)
	}
	if _, err := b.Open(context.Background(), longIntent("r2")); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("no account")
	}
	t212 := ports.WithAccount(context.Background(), domain.Account{ID: "t", Mode: domain.ModeDemo, Platform: domain.PlatformTrading212})
	if _, err := b.Lookup(t212, "x", 0); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("trading 212 account")
	}
	if len(b.Violations) != 5 || b.Opens != 2 {
		t.Fatalf("violations %v", b.Violations)
	}
	noMode := ports.WithAccount(context.Background(), domain.Account{ID: "n"})
	if _, err := b.Portfolio(noMode); err != nil || len(b.Violations) != 6 {
		t.Fatalf("an account without a mode: %v %v", err, b.Violations)
	}
	if _, err := b.Open(demoCtx(), domain.OrderIntent{RequestID: "bad", Direction: domain.Long, Instrument: 1, Amount: 1}); !errors.Is(err, domain.ErrNotAccepted) {
		t.Fatal("an order without a stop must be refused")
	}

	boom := errors.New("boom")
	b = &SimBroker{ReadErr: boom}
	ctx := demoCtx()
	if _, err := b.Portfolio(ctx); !errors.Is(err, boom) {
		t.Fatal("portfolio")
	}
	if _, err := b.Lookup(ctx, "x", 0); !errors.Is(err, boom) {
		t.Fatal("lookup")
	}
	if _, err := b.CloseOrder(ctx, 1); !errors.Is(err, boom) {
		t.Fatal("close order")
	}
	if _, err := b.Eligibility(ctx, []domain.InstrumentID{1}); !errors.Is(err, boom) {
		t.Fatal("eligibility")
	}
	if _, err := b.Costs(ctx, 1, domain.Long, 1); !errors.Is(err, boom) {
		t.Fatal("costs")
	}
	if _, err := b.TradeHistory(ctx, simT0); !errors.Is(err, boom) {
		t.Fatal("history")
	}
	b = &SimBroker{PortfolioErr: boom}
	if _, err := b.Portfolio(ctx); !errors.Is(err, boom) {
		t.Fatal("portfolio only")
	}
	els, err := b.Eligibility(ctx, []domain.InstrumentID{1, 2})
	if err != nil || len(els) != 2 || !els[1].RealLong || !els[1].ShortCFD || els[0].CheckedAt.IsZero() {
		t.Fatalf("eligibility = %+v %v", els, err)
	}
	if c, err := b.Costs(ctx, 1, domain.Short, 1); err != nil || len(c) != 1 {
		t.Fatal("costs")
	}
}

func TestRequireOrderRow(t *testing.T) {
	s := NewStore()
	ctx := demoCtx()
	_ = s.Users().Create(ctx, domain.User{ID: "u", Username: "u", Role: domain.RoleAdmin})
	_ = s.Accounts().Create(ctx, domain.Account{ID: "acc-1", Name: "Demo", OwnerID: "u", Mode: domain.ModeDemo})
	b := &SimBroker{OnOpen: RequireOrderRow(s.Orders())}

	// no row → violation
	if _, err := b.Open(ctx, longIntent("r1")); err != nil {
		t.Fatal(err)
	}
	if len(b.Violations) != 1 || !strings.Contains(b.Violations[0], "no order row") {
		t.Fatalf("violations = %v", b.Violations)
	}
	in := longIntent("r2")
	o, err := domain.NewOrder(domain.Order{
		RequestID: in.RequestID, AccountID: "acc-1", Mode: domain.ModeDemo, Kind: domain.OrderOpen,
		Direction: in.Direction, Instrument: in.Instrument, Amount: in.Amount, Entry: 100_000_000, StopLoss: in.StopLossRate, TakeProfit: in.TakeProfitRate,
	}, simT0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Orders().Create(ctx, o); err != nil {
		t.Fatal(err)
	}
	// created, not sent → violation
	_, _ = b.Open(ctx, in)
	if len(b.Violations) != 2 || !strings.Contains(b.Violations[1], "not a sent open") {
		t.Fatalf("violations = %v", b.Violations)
	}
	sent, _ := o.Transition(domain.OrderSent, simT0, "")
	if err := s.Orders().Update(ctx, o, sent); err != nil {
		t.Fatal(err)
	}
	hook := RequireOrderRow(s.Orders())
	if err := hook(ctx, in); err != nil {
		t.Fatalf("persisted and sent: %v", err)
	}
	other := in
	other.Amount++
	if err := hook(ctx, other); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("enlarged intent: %v", err)
	}
}
