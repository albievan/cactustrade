package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// The live activity log's bus messages (R26): one agent.tick summary per tick and one
// decision message per decision outcome, published with the right account after the
// decision was stored, and never blocking an order.

// storedCheckBus is a recording bus that checks, as each decision is published, that the
// decision is already stored with the same outcome.
type storedCheckBus struct {
	*fakes.Bus
	store ports.Store
	mu    sync.Mutex
	bad   []string
}

func (b *storedCheckBus) Publish(e ports.Event) {
	if d, ok := e.Data.(DecisionView); ok {
		got, err := b.store.Decisions().Get(context.Background(), d.AccountID, d.ID)
		if err != nil || got.Outcome != d.Outcome || got.OrderRequestID != d.OrderRequestID {
			b.mu.Lock()
			b.bad = append(b.bad, fmt.Sprintf("decision %s published as %s before it was stored (%+v, %v)", d.ID, d.Outcome, got, err))
			b.mu.Unlock()
		}
	}
	b.Bus.Publish(e)
}

func TestPublishTickAndDecisions(t *testing.T) {
	reject := domain.Verdict{Kind: domain.VerdictReject, Reason: "too risky"}
	tests := []struct {
		name         string
		setup        func(t *testing.T, ae *agentEnv)
		wantSkipped  string
		wantChecked  int
		wantIdeas    int
		wantOrders   int
		wantReasons  map[TraceStep]int
		wantOutcomes []domain.DecisionOutcome
	}{
		{
			name:        "an entry",
			wantChecked: 3, wantIdeas: 2, wantOrders: 2,
			wantReasons:  map[TraceStep]int{StepSession: 1},
			wantOutcomes: []domain.DecisionOutcome{domain.DecisionOrdered, domain.DecisionOrdered},
		},
		{
			name:        "the reviewer rejects",
			setup:       func(t *testing.T, ae *agentEnv) { ae.reviewing(t, verdict(reject, nil)) },
			wantChecked: 3, wantIdeas: 2,
			wantReasons:  map[TraceStep]int{StepSession: 1, StepReview: 2},
			wantOutcomes: []domain.DecisionOutcome{domain.DecisionRejectedReviewer, domain.DecisionRejectedReviewer},
		},
		{
			name: "no signal",
			setup: func(_ *testing.T, ae *agentEnv) {
				ae.setCand(func(domain.InstrumentID) *domain.Candidate { return nil })
			},
			wantChecked: 3,
			wantReasons: map[TraceStep]int{StepSession: 1, StepSignal: 2},
		},
		{
			name: "skipped: paused",
			setup: func(t *testing.T, ae *agentEnv) {
				if _, err := ae.svc.PauseAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
					t.Fatal(err)
				}
			},
			wantSkipped: "the agent is paused",
			wantReasons: map[TraceStep]int{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t, instNYSE, instLSE, instASX)
			bus := &storedCheckBus{Bus: &fakes.Bus{}, store: ae.store}
			ae.svc.d.Bus = bus
			if tt.setup != nil {
				tt.setup(t, ae)
			}
			tr := ae.tick(t)
			if len(bus.bad) > 0 {
				t.Fatal(strings.Join(bus.bad, "\n"))
			}
			ticks := bus.OfType(BusTick)
			if len(ticks) != 1 {
				t.Fatalf("ticks = %+v", ticks)
			}
			sum, ok := ticks[0].Data.(TickSummary)
			if !ok || BusAccount(ticks[0]) != ae.a.ID || !sum.At.Equal(tr.At) {
				t.Fatalf("tick = %+v", ticks[0])
			}
			if sum.Skipped != tt.wantSkipped || sum.Checked != tt.wantChecked || sum.Ideas != tt.wantIdeas || sum.Orders != tt.wantOrders || len(sum.Shares) != tt.wantChecked {
				t.Fatalf("summary = %+v", sum)
			}
			if fmt.Sprint(sum.Reasons) != fmt.Sprint(tt.wantReasons) {
				t.Fatalf("reasons = %v, want %v", sum.Reasons, tt.wantReasons)
			}
			// one decision message per stored decision, with its account and order, before the tick
			stored, err := ae.store.Decisions().List(ae.ctx, ae.a.ID, time.Time{}, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			all := bus.Events()
			if all[len(all)-1].Type != BusTick {
				t.Fatal("the tick summary is not the last message")
			}
			var outcomes []domain.DecisionOutcome
			for _, e := range all {
				d, ok := e.Data.(DecisionView)
				if !ok {
					continue
				}
				outcomes = append(outcomes, d.Outcome)
				if e.Type != BusDecision || BusAccount(e) != ae.a.ID {
					t.Fatalf("decision message %+v", e)
				}
				if d.Outcome == domain.DecisionOrdered {
					o := ae.order(t, d.OrderRequestID)
					if d.OrderRequestID == "" || d.OrderState == "" || o.AccountID != ae.a.ID {
						t.Fatalf("ordered decision %+v, order %+v", d, o)
					}
				} else if d.OrderRequestID != "" {
					t.Fatalf("decision without an order names one: %+v", d)
				}
			}
			if fmt.Sprint(outcomes) != fmt.Sprint(tt.wantOutcomes) || len(stored) != len(tt.wantOutcomes) {
				t.Fatalf("outcomes = %v (stored %d), want %v", outcomes, len(stored), tt.wantOutcomes)
			}
			// bus only: no stored event for a tick or a decision
			evs, err := ae.store.Events().List(ae.ctx, []string{ae.a.ID}, 500)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range evs {
				if e.Kind == BusTick || e.Kind == BusDecision {
					t.Fatalf("stored event %+v", e)
				}
			}
		})
	}
}

// TestPublishTickNeverBlocksAnOrder: a subscriber that is full and never read does not hold
// up the tick's order (bus semantics).
func TestPublishTickNeverBlocksAnOrder(t *testing.T) {
	ae := newAgentEnv(t)
	bus := &fakes.Bus{}
	ae.svc.d.Bus = bus
	_, cancel := bus.Subscribe(1)
	defer cancel()
	done := make(chan AgentTrace, 1)
	go func() {
		tr, _ := ae.svc.AgentTick(ae.ctx, ae.a.ID)
		done <- tr
	}()
	select {
	case tr := <-done:
		if tr.Entries != 1 || ae.b.Opens != 1 {
			t.Fatalf("trace %+v, opens %d", tr, ae.b.Opens)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a full subscriber blocked the tick")
	}
	if bus.Dropped() == 0 || len(bus.OfType(BusTick)) != 1 || len(bus.OfType(BusDecision)) != 1 {
		t.Fatalf("dropped %d ticks %d decisions %d", bus.Dropped(), len(bus.OfType(BusTick)), len(bus.OfType(BusDecision)))
	}
}

func TestAgentTraceSummary(t *testing.T) {
	long := strings.Repeat("é", 150) // 300 bytes
	tests := []struct {
		name string
		tr   AgentTrace
		want TickSummary
	}{
		{
			"skipped",
			AgentTrace{AccountID: "a", Skipped: "the watchlist is empty", Errors: []string{"x"}},
			TickSummary{AccountID: "a", Skipped: "the watchlist is empty", Reasons: map[TraceStep]int{}, Shares: []TickShare{}, Errors: 1},
		},
		{"mixed", AgentTrace{AccountID: "a", Entries: 1, Killed: true, Instruments: []InstrumentTrace{
			{Instrument: 1, Symbol: "AAPL", Entered: true, DecisionID: "d1", Checks: []TraceCheck{{Step: StepSignal, Pass: true}, {Step: StepOrder, Pass: true, Detail: "sent"}}},
			{Instrument: 2, Symbol: "MSFT", Checks: []TraceCheck{{Step: StepSignal, Detail: "no signal: flat"}}},
			{Instrument: 3, Symbol: "NVDA", DecisionID: "d2", Checks: []TraceCheck{{Step: StepRisk, Detail: long}}},
			{Instrument: 4, Symbol: "BARC.L", Checks: []TraceCheck{{Step: StepSession, Detail: "LSE is off"}}},
			{Instrument: 5, Symbol: "IBM", Checks: []TraceCheck{{Step: StepSignal, Detail: "no signal"}}},
			{Instrument: 6, Symbol: "EMPTY"},
		}}, TickSummary{
			AccountID: "a", Checked: 6, Ideas: 2, Orders: 1, Killed: true,
			Reasons: map[TraceStep]int{StepSignal: 2, StepRisk: 1, StepSession: 1},
			Shares: []TickShare{
				{Instrument: 1, Symbol: "AAPL", Entered: true, Detail: "sent"},
				{Instrument: 2, Symbol: "MSFT", Step: StepSignal, Detail: "no signal: flat"},
				{Instrument: 3, Symbol: "NVDA", Step: StepRisk, Detail: strings.Repeat("é", 98) + "…"},
				{Instrument: 4, Symbol: "BARC.L", Step: StepSession, Detail: "LSE is off"},
				{Instrument: 5, Symbol: "IBM", Step: StepSignal, Detail: "no signal"},
				{Instrument: 6, Symbol: "EMPTY"},
			},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.tr.Summary()
			if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", tt.want) {
				t.Fatalf("summary\n got %+v\nwant %+v", got, tt.want)
			}
			for _, sh := range got.Shares {
				if len(sh.Detail) > tickDetailMax || !utf8.ValidString(sh.Detail) {
					t.Fatalf("detail %q", sh.Detail)
				}
			}
		})
	}
}

func TestBusAccountActivity(t *testing.T) {
	if got := BusAccount(ports.Event{Data: TickSummary{AccountID: "t"}}); got != "t" {
		t.Fatalf("tick account %q", got)
	}
	if got := BusAccount(ports.Event{Data: DecisionView{Decision: domain.Decision{AccountID: "d"}}}); got != "d" {
		t.Fatalf("decision account %q", got)
	}
}
