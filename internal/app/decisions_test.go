package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// decisions returns every decision of the agent's account, newest first.
func (ae *agentEnv) decisions(t *testing.T) []domain.Decision {
	t.Helper()
	ds, err := ae.store.Decisions().List(ae.ctx, ae.a.ID, time.Time{}, "", 1_000)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

// pricedReviewing switches reviewing on with a profile that has prices: $3 / $15 per
// million input / output tokens.
func (ae *agentEnv) pricedReviewing(t *testing.T, answer func(context.Context, ports.ReviewInput) (domain.Verdict, error)) *agentReviewer {
	t.Helper()
	r := ae.reviewing(t, answer)
	st, err := ae.svc.LLMSetup(ae.ctx, ae.admin)
	if err != nil {
		t.Fatal(err)
	}
	pr := st.Profiles[0].LLMProfile
	pr.InputPrice, pr.OutputPrice = 3_000_000, 15_000_000
	if _, err := ae.svc.SaveLLMProfile(ae.ctx, ae.admin, pr, nil); err != nil {
		t.Fatal(err)
	}
	return r
}

// TestAgentTickDecisionOutcomes: every way a candidate can end writes exactly one decision
// with the right outcome; a tick without a candidate writes none.
func TestAgentTickDecisionOutcomes(t *testing.T) {
	approve := verdict(domain.Verdict{Kind: domain.VerdictApprove}, nil)
	tests := []struct {
		name    string
		setup   func(t *testing.T, ae *agentEnv)
		want    domain.DecisionOutcome // "" = no decision
		opens   int
		verdict domain.VerdictKind
		call    bool   // the decision links a logged LLM call
		risk    string // in the risk reason
		order   bool   // the decision carries the order's request id
	}{
		{name: "ordered, reviewing off", want: domain.DecisionOrdered, opens: 1, risk: "allowed $2000.00", order: true},
		{name: "ordered, approved", setup: func(t *testing.T, ae *agentEnv) { ae.pricedReviewing(t, approve) }, want: domain.DecisionOrdered, opens: 1, verdict: domain.VerdictApprove, call: true, order: true},
		{name: "ordered, rejected by eToro", setup: func(t *testing.T, ae *agentEnv) {
			ae.b.OpenScript = []fakes.SimOutcome{fakes.SimReject}
		}, want: domain.DecisionOrdered, opens: 1, order: true},
		{name: "no candidate: no decision", setup: func(t *testing.T, ae *agentEnv) {
			ae.setCand(func(domain.InstrumentID) *domain.Candidate { return nil })
		}},
		{name: "market closed: no decision", setup: func(t *testing.T, ae *agentEnv) { ae.svc.d.Calendar = fixedCalendar{} }},
		{name: "costs", setup: func(t *testing.T, ae *agentEnv) {
			ae.svc.d.Broker = &costsBroker{SimBroker: ae.b, err: domain.ErrUnavailable}
		}, want: domain.DecisionRejectedCost, risk: "costs could not be read"},
		{name: "costs again after the engine shrank the size", setup: func(t *testing.T, ae *agentEnv) {
			ae.b.Positions = append(ae.b.Positions, domain.Position{ID: 77, Instrument: 999, Amount: 9_500_00, OpenRate: 1_000_000, StopLossRate: 2_000_000, CFD: true, Leverage: 1})
			ae.svc.d.Broker = &costsBroker{SimBroker: ae.b, items: []domain.CostItem{{Type: "fee", Value: 50_000_000, Currency: "USD"}}}
		}, want: domain.DecisionRejectedCost, risk: "allowed $500.00"},
		{name: "reviewer rejects", setup: func(t *testing.T, ae *agentEnv) {
			ae.pricedReviewing(t, verdict(domain.Verdict{Kind: domain.VerdictReject, Reason: "too extended"}, nil))
		}, want: domain.DecisionRejectedReviewer, verdict: domain.VerdictReject, call: true},
		{name: "reviewer fails", setup: func(t *testing.T, ae *agentEnv) {
			ae.reviewing(t, verdict(domain.Verdict{Kind: domain.VerdictApprove}, errors.New("HTTP 529")))
		}, want: domain.DecisionRejectedReviewer, verdict: domain.VerdictReject, call: true},
		{name: "reviewer garbage", setup: func(t *testing.T, ae *agentEnv) {
			ae.reviewing(t, verdict(domain.Verdict{Kind: "increase", SizeBp: 30_000}, nil))
		}, want: domain.DecisionRejectedReviewer, verdict: domain.VerdictReject, call: true},
		{name: "reviewing on without a reviewer", setup: func(t *testing.T, ae *agentEnv) {
			ae.reviewing(t, approve)
			ae.svc.d.LLM = nil
		}, want: domain.DecisionRejectedReviewer, verdict: domain.VerdictReject},
		{name: "the LLM call cannot be logged", setup: func(t *testing.T, ae *agentEnv) {
			ae.reviewing(t, approve)
			ae.store.LLMCallsErr = errors.New("disk full")
		}, want: domain.DecisionRejectedReviewer, verdict: domain.VerdictReject},
		{name: "stale quote", setup: func(t *testing.T, ae *agentEnv) { ae.m.QuoteAge = time.Minute }, want: domain.DecisionRejectedRisk, risk: "stale"},
		{name: "price passed the stop", setup: func(t *testing.T, ae *agentEnv) { ae.m.setQuote(instNYSE, 96_000_000, 96_500_000) }, want: domain.DecisionRejectedRisk, risk: "passed the stop"},
		{name: "order in flight", setup: func(t *testing.T, ae *agentEnv) {
			ae.storedOrder(t, instNYSE, domain.Long, 100_00, domain.OrderPending)
		}, want: domain.DecisionRejectedRisk},
		{name: "drawdown: kill", setup: func(t *testing.T, ae *agentEnv) {
			st := domain.AccountRiskState{AccountID: ae.a.ID, State: domain.RiskState{Day: domain.RiskDay(ae.clock.Now()), StartOfDayEquity: 1_000_000_00, HighWaterMark: 2_000_000_00}}
			if _, err := ae.store.RiskStates().Put(ae.ctx, st); err != nil {
				t.Fatal(err)
			}
		}, want: domain.DecisionRejectedRisk, risk: "kill switch"},
		{name: "paused before the order", setup: func(t *testing.T, ae *agentEnv) {
			ae.reviewing(t, func(context.Context, ports.ReviewInput) (domain.Verdict, error) {
				ae.setAgent(t, domain.AgentPaused)
				return domain.Verdict{Kind: domain.VerdictApprove}, nil
			})
		}, want: domain.DecisionOrderFailed, verdict: domain.VerdictApprove, call: true, risk: "the agent became paused"},
		{name: "the order row cannot be stored", setup: func(t *testing.T, ae *agentEnv) {
			ae.store.OrdersErr = errors.New("disk full")
		}, want: domain.DecisionOrderFailed, risk: "storing the order"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t)
			if tt.setup != nil {
				tt.setup(t, ae)
			}
			tr := ae.tick(t)
			ds := ae.decisions(t)
			if ae.b.Opens != tt.opens {
				t.Fatalf("opens = %d, want %d; trace %+v", ae.b.Opens, tt.opens, tr)
			}
			noViolations(t, ae.b)
			if tt.want == "" {
				if len(ds) != 0 {
					t.Fatalf("decisions without a candidate: %+v", ds)
				}
				return
			}
			if len(ds) != 1 {
				t.Fatalf("%d decisions, want 1: %+v; trace %+v", len(ds), ds, tr)
			}
			d := ds[0]
			if d.Outcome != tt.want || tr.Instruments[0].DecisionID != d.ID || len(tr.Errors) > 0 {
				t.Fatalf("decision %+v, want %s; trace %+v", d, tt.want, tr)
			}
			if d.Signal.Instrument != instNYSE || d.Signal.Symbol != "AAPL" || d.Signal.Playbook != "trend_breakout" || d.Signal.Entry != 100_000_000 ||
				d.Signal.StopLoss != 97_000_000 || d.Signal.SuggestedSize != 2_000_00 || !d.At.Equal(agentStart) {
				t.Fatalf("signal %+v at %s", d.Signal, d.At)
			}
			if d.Verdict.Kind != tt.verdict || (d.Verdict.LLMCallID != "") != tt.call {
				t.Fatalf("verdict %+v, want %q (call %v)", d.Verdict, tt.verdict, tt.call)
			}
			if !strings.Contains(d.Risk.Reason, tt.risk) {
				t.Fatalf("risk %+v, want %q", d.Risk, tt.risk)
			}
			if tt.order != (d.OrderRequestID != "") {
				t.Fatalf("request id %q", d.OrderRequestID)
			}
			if tt.order {
				if in := ae.b.Intents[0]; in.RequestID != d.OrderRequestID || !d.Risk.Allowed || d.Risk.Size != in.Amount {
					t.Fatalf("decision %+v for intent %+v", d, in)
				}
			}
			if tt.call {
				c, err := ae.store.LLMCalls().Get(ae.ctx, d.Verdict.LLMCallID)
				if err != nil || c.Purpose != domain.LLMPurposeAgent || c.AccountID != ae.a.ID || c.Model != "served-gpt" || c.InputTokens != 1_000 || c.Prompt == "" {
					t.Fatalf("logged call %+v %v", c, err)
				}
			}
		})
	}
}

// TestAgentTickDecisionCost: the call's cost comes from the profile's prices (OD13), and is
// unknown without them.
func TestAgentTickDecisionCost(t *testing.T) {
	for _, priced := range []bool{true, false} {
		t.Run(fmt.Sprint("priced=", priced), func(t *testing.T) {
			ae := newAgentEnv(t)
			if priced {
				ae.pricedReviewing(t, verdict(domain.Verdict{Kind: domain.VerdictReduce, SizeBp: 5_000, Reason: "half"}, nil))
			} else {
				ae.reviewing(t, verdict(domain.Verdict{Kind: domain.VerdictReduce, SizeBp: 5_000, Reason: "half"}, nil))
			}
			ae.tick(t)
			ds := ae.decisions(t)
			if len(ds) != 1 || ds[0].Verdict.Kind != domain.VerdictReduce || ds[0].Verdict.SizeBp != 5_000 || ds[0].Risk.Size != 1_000_00 {
				t.Fatalf("decisions %+v", ds)
			}
			c, err := ae.store.LLMCalls().Get(ae.ctx, ds[0].Verdict.LLMCallID)
			// 1 000 × $3 + 100 × $15 per million = $0.0045 = 4 500 micros
			want := domain.Micros(4_500)
			if !priced {
				want = domain.CostUnknown
			}
			if err != nil || c.CostMicros != want || c.Latency != 1500*time.Millisecond || !c.At.Equal(agentStart) {
				t.Fatalf("call %+v %v, want cost %d", c, err, want)
			}
		})
	}
}

// TestAgentTickDecisionStoreFails: when the pending decision cannot be written, nothing is
// sent and the trace says so (fail closed).
func TestAgentTickDecisionStoreFails(t *testing.T) {
	ae := newAgentEnv(t)
	ae.store.DecisionsErr = errors.New("disk full")
	tr := ae.tick(t)
	if ae.b.Opens != 0 || tr.Entries != 0 {
		t.Fatalf("opened without a decision: %+v", ae.b.Intents)
	}
	if c := lastCheck(tr.Instruments[0]); c.Pass || c.Step != StepOrder || !strings.Contains(c.Detail, "storing the decision failed") {
		t.Fatalf("last check %+v", c)
	}
	if len(tr.Errors) != 1 || !strings.Contains(tr.Errors[0], "disk full") {
		t.Fatalf("errors %v", tr.Errors)
	}
	if os, _ := ae.store.Orders().List(ae.ctx, ae.a.ID, 10); len(os) != 0 {
		t.Fatalf("an order row without its decision: %+v", os)
	}
	// with the store back, the next tick enters and logs
	ae.store.DecisionsErr = nil
	ae.tick(t)
	if ds := ae.decisions(t); ae.b.Opens != 1 || len(ds) != 1 || ds[0].Outcome != domain.DecisionOrdered {
		t.Fatalf("after recovery: opens %d, decisions %+v", ae.b.Opens, ds)
	}
}

// decisionFirstStore fails an open order's Create unless the account's decision log already
// holds its pending decision (PLAN 5.5: the decision is written before the order row).
type decisionFirstStore struct {
	*fakes.Store
	violations *[]string
}

func (s decisionFirstStore) Orders() ports.Orders { return decisionFirstOrders{s.Store.Orders(), s} }

type decisionFirstOrders struct {
	ports.Orders
	s decisionFirstStore
}

func (o decisionFirstOrders) Create(ctx context.Context, ord domain.Order) error {
	if ord.Kind == domain.OrderOpen {
		if err := fakes.RequireDecision(o.s.Decisions(), ord.AccountID)(ctx, domain.OrderIntent{
			RequestID: ord.RequestID, Direction: ord.Direction, Instrument: ord.Instrument, Amount: ord.Amount,
		}); err != nil {
			*o.s.violations = append(*o.s.violations, err.Error())
		}
	}
	return o.Orders.Create(ctx, ord)
}

func TestAgentTickDecisionBeforeOrderRow(t *testing.T) {
	ae := newAgentEnv(t)
	ae.addNYSE(t, instMSFT, "MSFT")
	var violations []string
	ae.svc.d.Store = decisionFirstStore{Store: ae.store, violations: &violations}
	tr := ae.tick(t)
	if tr.Entries != 2 || ae.b.Opens != 2 || len(violations) != 0 {
		t.Fatalf("entries %d, violations %v", tr.Entries, violations)
	}
	noViolations(t, ae.b)
	for _, d := range ae.decisions(t) {
		if d.Outcome != domain.DecisionOrdered || ae.order(t, d.OrderRequestID).Instrument != d.Signal.Instrument {
			t.Fatalf("decision %+v", d)
		}
	}
}

// TestDecisionLogViews: the list, its filter and bounds, the detail with the LLM call, and
// another account's decision.
func TestDecisionLogViews(t *testing.T) {
	ae := newAgentEnv(t)
	ae.addNYSE(t, instMSFT, "MSFT")
	ae.pricedReviewing(t, func(_ context.Context, in ports.ReviewInput) (domain.Verdict, error) {
		if in.Candidate.Instrument == instMSFT {
			return domain.Verdict{Kind: domain.VerdictReject, Reason: "no"}, nil
		}
		return domain.Verdict{Kind: domain.VerdictApprove}, nil
	})
	ae.tick(t)
	all, err := ae.svc.Decisions(ae.ctx, ae.admin, "", DecisionQuery{})
	if err != nil || len(all) != 2 {
		t.Fatalf("Decisions = %+v %v", all, err)
	}
	var ordered DecisionView
	for _, d := range all {
		if d.Outcome == domain.DecisionOrdered {
			ordered = d
		}
	}
	if ordered.OrderState != domain.OrderFilled {
		t.Fatalf("ordered decision %+v", ordered)
	}
	rej, err := ae.svc.Decisions(ae.ctx, ae.admin, ae.a.ID, DecisionQuery{Outcome: domain.DecisionRejectedReviewer, Limit: 5})
	if err != nil || len(rej) != 1 || rej[0].OrderState != "" || rej[0].Signal.Symbol != "MSFT" {
		t.Fatalf("filtered = %+v %v", rej, err)
	}
	if got, _ := ae.svc.Decisions(ae.ctx, ae.admin, "", DecisionQuery{Before: agentStart}); len(got) != 0 {
		t.Fatalf("before = %+v", got)
	}
	for _, q := range []DecisionQuery{{Limit: -1}, {Limit: 501}, {Outcome: "won"}} {
		if _, err := ae.svc.Decisions(ae.ctx, ae.admin, "", q); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("query %+v: %v", q, err)
		}
	}
	// the detail: the call with its prompt and response; a viewer with view access sees it
	viewer := ae.shared(t, "vic", domain.RoleViewer, domain.AccessView)
	det, err := ae.svc.Decision(ae.ctx, viewer, ae.a.ID, rej[0].ID)
	if err != nil || det.Call == nil || det.Call.Prompt == "" || det.Call.Response == "" || det.Call.CostMicros != 4_500 || det.Verdict.Reason != "no" {
		t.Fatalf("Decision = %+v %v", det, err)
	}
	// another account: not found, by id or by account
	other := ae.account(t, "Other", domain.ModeDemo)
	if _, err := ae.svc.Decision(ae.ctx, ae.admin, other.ID, rej[0].ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another account's decision: %v", err)
	}
	if _, err := ae.svc.Decision(ae.ctx, viewer, other.ID, rej[0].ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an account not shared: %v", err)
	}
	if _, err := ae.svc.Decisions(ae.ctx, viewer, other.ID, DecisionQuery{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("list of an account not shared: %v", err)
	}
	if _, err := ae.svc.Decision(ae.ctx, Principal{}, ae.a.ID, rej[0].ID); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("anonymous: %v", err)
	}
	if _, err := ae.svc.Decisions(ae.ctx, Principal{}, "", DecisionQuery{}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("anonymous list: %v", err)
	}
	// a call purged (or of another account) is left out, not an error
	if _, err := ae.store.LLMCalls().DeleteBefore(ae.ctx, agentStart.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if det, err := ae.svc.Decision(ae.ctx, ae.admin, "", rej[0].ID); err != nil || det.Call != nil {
		t.Fatalf("purged call: %+v %v", det, err)
	}
}

func TestLLMUsage(t *testing.T) {
	ae := newAgentEnv(t)
	ae.addNYSE(t, instMSFT, "MSFT")
	ae.pricedReviewing(t, verdict(domain.Verdict{Kind: domain.VerdictReject, Reason: "no"}, nil))
	ae.tick(t)
	// a call of a deleted profile, 10 days ago, with no price
	old := domain.LLMCall{ID: "old", Purpose: domain.LLMPurposeTest, ProfileID: "gone", InputTokens: 5, CostMicros: domain.CostUnknown, At: agentStart.Add(-10 * 24 * time.Hour)}
	if err := ae.store.LLMCalls().Add(ae.ctx, old); err != nil {
		t.Fatal(err)
	}
	rep, err := ae.svc.LLMUsage(ae.ctx, ae.admin, 0)
	if err != nil || rep.Days != 30 || len(rep.Rows) != 2 {
		t.Fatalf("usage = %+v %v", rep, err)
	}
	var desk LLMUsageRow
	for _, r := range rep.Rows {
		if r.ProfileName == "Desk" {
			desk = r
		}
	}
	if desk.Calls != 2 || desk.Purpose != domain.LLMPurposeAgent || desk.InputTokens != 2_000 || desk.Cost != 9_000 || !desk.CostKnown() {
		t.Fatalf("desk = %+v", desk)
	}
	if rep.Total.Calls != 3 || rep.Total.CostKnown() || rep.Total.Cost != 9_000 {
		t.Fatalf("total = %+v", rep.Total)
	}
	if rep, err := ae.svc.LLMUsage(ae.ctx, ae.admin, 7); err != nil || len(rep.Rows) != 1 || !rep.Since.Equal(agentStart.Add(-7*24*time.Hour)) {
		t.Fatalf("7 days = %+v %v", rep, err)
	}
	for _, days := range []int{-1, domain.LLMRetentionDays + 1} {
		if _, err := ae.svc.LLMUsage(ae.ctx, ae.admin, days); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("days %d: %v", days, err)
		}
	}
	op := ae.shared(t, "olga", domain.RoleOperator, domain.AccessTrade)
	if _, err := ae.svc.LLMUsage(ae.ctx, op, 30); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("operator: %v", err)
	}
}

// TestPurgeDecisionLog: decisions and calls older than 400 days go (OD12), newer ones stay.
func TestPurgeDecisionLog(t *testing.T) {
	ae := newAgentEnv(t)
	ae.reviewing(t, verdict(domain.Verdict{Kind: domain.VerdictApprove}, nil))
	ae.tick(t)
	if n, m, err := ae.svc.PurgeDecisionLog(ae.ctx); err != nil || n != 0 || m != 0 {
		t.Fatalf("purge now = %d %d %v", n, m, err)
	}
	ae.clock.Advance(domain.LLMRetentionDays*24*time.Hour + time.Second)
	if n, m, err := ae.svc.PurgeDecisionLog(ae.ctx); err != nil || n != 1 || m != 1 {
		t.Fatalf("purge later = %d %d %v", n, m, err)
	}
}

// TestLLMCallsLoggedByScannerAndTest: the scanner's and the admin test's calls are logged with
// their purpose; a failed log write does not change their result.
func TestLLMCallsLoggedByScannerAndTest(t *testing.T) {
	ae := newAgentEnv(t)
	ae.reviewing(t, verdict(domain.Verdict{Kind: domain.VerdictApprove, Reason: "fine"}, nil))
	st, _ := ae.svc.LLMSetup(ae.ctx, ae.admin)
	res, err := ae.svc.TestLLM(ae.ctx, ae.admin, st.Profiles[0].ID)
	if err != nil || !res.OK || res.Model != "served-gpt" || res.InputTokens != 1_000 || res.OutputTokens != 100 || res.CallID == "" || res.Cost != domain.CostUnknown {
		t.Fatalf("TestLLM = %+v %v", res, err)
	}
	c, err := ae.store.LLMCalls().Get(ae.ctx, res.CallID)
	if err != nil || c.Purpose != domain.LLMPurposeTest || c.AccountID != "" || c.ProfileID != st.Profiles[0].ID {
		t.Fatalf("test call %+v %v", c, err)
	}
	rc, ok := ae.svc.llmCheck(ae.ctx, ae.a.ID, "", *longCand(instNYSE), nil)
	if !ok || !rc.Pass {
		t.Fatalf("llmCheck = %+v %v", rc, ok)
	}
	us, _ := ae.store.LLMCalls().Usage(ae.ctx, time.Time{})
	if len(us) != 2 || us[0].Purpose != domain.LLMPurposeScanner || us[1].Purpose != domain.LLMPurposeTest {
		t.Fatalf("usage %+v", us)
	}
	// logging fails: the results stand
	ae.store.LLMCallsErr = errors.New("disk full")
	if res, err := ae.svc.TestLLM(ae.ctx, ae.admin, st.Profiles[0].ID); err != nil || !res.OK || res.CallID != "" {
		t.Fatalf("TestLLM without a log = %+v %v", res, err)
	}
	if rc, ok := ae.svc.llmCheck(ae.ctx, ae.a.ID, "", *longCand(instNYSE), nil); !ok || !rc.Pass {
		t.Fatalf("llmCheck without a log = %+v %v", rc, ok)
	}
}
