package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOrderStatusConforms(t *testing.T) {
	exec := func(open, stop Micros) []PositionExecution {
		return []PositionExecution{{PositionID: 7, Units: 1_000_000, Amount: 100_00, OpenRate: open, StopLossRate: stop}}
	}
	long := OrderStatus{State: OrderFilled, Settlement: SettlementReal, Leverage: 1, Side: Long, Executions: exec(100_000_000, 95_000_000)}
	short := OrderStatus{State: OrderFilled, Settlement: SettlementCFD, Leverage: 1, Side: Short, Executions: exec(100_000_000, 105_000_000)}
	tests := []struct {
		name   string
		s      OrderStatus
		d      Direction
		ok     bool
		reason string
	}{
		{"long real x1", long, Long, true, ""},
		{"short cfd x1", short, Short, true, ""},
		{"no executions yet", OrderStatus{Settlement: SettlementReal, Leverage: 1, Side: Long}, Long, true, ""},
		{"unknown direction", long, Direction("sideways"), false, "unknown direction"},
		{"long asked, short opened", short, Long, false, "side"},
		{"cfd long", func() OrderStatus { s := long; s.Settlement = SettlementCFD; return s }(), Long, false, "settled"},
		{"real short", func() OrderStatus { s := short; s.Settlement = SettlementReal; return s }(), Short, false, "settled"},
		{"margin trade", func() OrderStatus { s := long; s.Settlement = "marginTrade"; return s }(), Long, false, "settled"},
		{"leverage 2", func() OrderStatus { s := long; s.Leverage = 2; return s }(), Long, false, "leverage 2"},
		{"leverage unknown", func() OrderStatus { s := long; s.Leverage = 0; return s }(), Long, false, "leverage 0"},
		{"side missing", func() OrderStatus { s := long; s.Side = ""; return s }(), Long, false, "side"},
		{"no stop", func() OrderStatus { s := long; s.Executions = exec(100_000_000, 0); return s }(), Long, false, "no stop-loss"},
		{"no open rate", func() OrderStatus { s := long; s.Executions = exec(0, 1); return s }(), Long, false, "no open rate"},
		{"long stop above", func() OrderStatus { s := long; s.Executions = exec(100_000_000, 100_000_000); return s }(), Long, false, "wrong side"},
		{"short stop below", func() OrderStatus { s := short; s.Executions = exec(100_000_000, 99_000_000); return s }(), Short, false, "wrong side"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, why := tt.s.Conforms(tt.d)
			if ok != tt.ok || !strings.Contains(why, tt.reason) {
				t.Fatalf("Conforms = %v %q, want %v %q", ok, why, tt.ok, tt.reason)
			}
		})
	}
}

func TestBrokerErrors(t *testing.T) {
	if !errors.Is(ErrOutcomeUnknown, ErrUnavailable) || !errors.Is(ErrNotAccepted, ErrUnavailable) {
		t.Fatal("broker errors must fail closed as unavailable")
	}
	if !errors.Is(ErrRealModeGated, ErrForbidden) {
		t.Fatal("real mode gated must be forbidden")
	}
	if !errors.Is(ErrWrongPassword, ErrForbidden) || errors.Is(ErrWrongPassword, ErrInvalid) {
		t.Fatal("a wrong confirmation password must be forbidden (403), not invalid input")
	}
	tests := []struct {
		name string
		err  *NotAcceptedError
		text []string
		is   []error
	}{
		{"429", &NotAcceptedError{RateLimited: true, RetryAfter: 5 * time.Second, Reason: "slow down"}, []string{"rate limited", "5s", "slow down"}, []error{ErrNotAccepted, ErrUnavailable}},
		{"404 position", &NotAcceptedError{Reason: "no such position", Err: ErrNotFound}, []string{"no such position"}, []error{ErrNotAccepted, ErrNotFound}},
		{"bare", &NotAcceptedError{}, []string{"not accepted by the broker"}, []error{ErrNotAccepted}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error = tt.err
			for _, s := range tt.text {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("%q lacks %q", err.Error(), s)
				}
			}
			for _, target := range tt.is {
				if !errors.Is(err, target) {
					t.Errorf("not %v", target)
				}
			}
			if errors.Is(err, ErrOutcomeUnknown) {
				t.Error("a refusal is never an unknown outcome")
			}
			if got, ok := errors.AsType[*NotAcceptedError](err); !ok || got.RateLimited != tt.err.RateLimited {
				t.Error("AsType")
			}
		})
	}
}

func TestAgentPositionOpen(t *testing.T) {
	p := AgentPosition{PositionID: 1}
	if !p.Open() {
		t.Fatal("open")
	}
	p.ClosedAt = time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)
	if p.Open() {
		t.Fatal("closed")
	}
}
