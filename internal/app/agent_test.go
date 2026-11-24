package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
	"github.com/albievan/cactustrade/internal/strategy"
)

// agentMarket is the market data of the agent tests: 5-minute bars up to the bar forming at
// the clock's time (so the agent must drop it), and quotes stamped QuoteAge before now.
type agentMarket struct {
	mu       sync.Mutex
	clock    *fakes.Clock
	quotes   map[domain.InstrumentID]domain.Quote
	QuoteAge time.Duration
	Err      error
}

var _ ports.MarketData = (*agentMarket)(nil)

func (m *agentMarket) setQuote(id domain.InstrumentID, bid, ask domain.Micros) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.quotes == nil {
		m.quotes = map[domain.InstrumentID]domain.Quote{}
	}
	m.quotes[id] = domain.Quote{Instrument: id, Bid: bid, Ask: ask}
}

func (m *agentMarket) Quotes(_ context.Context, ids []domain.InstrumentID) ([]domain.Quote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	var out []domain.Quote
	for _, id := range ids {
		if q, ok := m.quotes[id]; ok {
			q.At = m.clock.Now().Add(-m.QuoteAge)
			out = append(out, q)
		}
	}
	return out, nil
}

func (m *agentMarket) Candles(_ context.Context, id domain.InstrumentID, iv time.Duration, n int) ([]domain.Candle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	last := m.clock.Now().Truncate(iv) // still forming
	out := make([]domain.Candle, n)
	for i := range out {
		p := domain.Micros(100_000_000 + int64(id)*1_000 + int64(i%5)*10_000)
		out[i] = domain.Candle{Start: last.Add(-time.Duration(n-1-i) * iv), Open: p, High: p, Low: p, Close: p, Volume: 100}
	}
	return out, nil
}

func (m *agentMarket) Search(context.Context, string) ([]domain.Instrument, error) { return nil, nil }

func (m *agentMarket) Instruments(context.Context, []domain.InstrumentID) ([]domain.Instrument, error) {
	return nil, nil
}

// agentReviewer answers with a function of the call (safe for concurrent use).
type agentReviewer struct {
	mu     sync.Mutex
	calls  int
	answer func(ctx context.Context, in ports.ReviewInput) (domain.Verdict, error)
	seen   []ports.ReviewInput
}

func (r *agentReviewer) Review(ctx context.Context, _ domain.LLMConfig, _ string, in ports.ReviewInput) (domain.Verdict, error) {
	r.mu.Lock()
	r.calls++
	r.seen = append(r.seen, in)
	f := r.answer
	r.mu.Unlock()
	return f(ctx, in)
}

func (r *agentReviewer) Models(context.Context, domain.LLMConfig, string) ([]string, error) {
	return nil, nil
}

// agentEnv is a running agent on a reconciled demo account at 15:00:05 UTC (NYSE in session),
// with AAPL (NYSE), BARC.L (LSE) and CBA.ASX on the watchlist, a $10 000 budget and a
// strategy that proposes cand for every instrument.
type agentEnv struct {
	*execEnv
	m    *agentMarket
	cand func(id domain.InstrumentID) *domain.Candidate
	bars [][]domain.Candle // what the strategy saw
	mu   sync.Mutex
}

var agentStart = time.Date(2026, 11, 23, 15, 0, 5, 0, time.UTC)

func longCand(id domain.InstrumentID) *domain.Candidate {
	return &domain.Candidate{
		Instrument: id, Direction: domain.Long, Playbook: "trend_breakout", Reason: "test",
		Entry: 100_000_000, StopLossRate: 97_000_000, TakeProfitRate: 106_000_000, SuggestedSize: 2_000_00,
	}
}

func shortCand(id domain.InstrumentID) *domain.Candidate {
	return &domain.Candidate{
		Instrument: id, Direction: domain.Short, Playbook: "trend_breakdown_short", Reason: "test",
		Entry: 100_000_000, StopLossRate: 103_000_000, TakeProfitRate: 94_000_000, SuggestedSize: 2_000_00,
	}
}

func newAgentEnv(t *testing.T, watch ...domain.InstrumentID) *agentEnv {
	t.Helper()
	x := supEnv(t, agentStart)
	ae := &agentEnv{execEnv: x, m: &agentMarket{clock: x.clock}, cand: longCand}
	x.svc.d.Market = ae.m
	for _, id := range []domain.InstrumentID{instNYSE, instLSE, instASX} {
		ae.m.setQuote(id, 99_990_000, 100_000_000)
	}
	if len(watch) == 0 {
		watch = []domain.InstrumentID{instNYSE}
	}
	for _, id := range watch {
		if err := x.store.Watchlist().Add(x.ctx, x.a.ID, id, "test", x.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	x.svc.evalOverride = func(id domain.InstrumentID, bs []domain.Candle) strategy.Decision {
		ae.mu.Lock()
		ae.bars = append(ae.bars, bs)
		f := ae.cand
		ae.mu.Unlock()
		if c := f(id); c != nil {
			return strategy.Decision{Candidate: c}
		}
		return strategy.Decision{Reason: "no signal (test)"}
	}
	ae.limits(t, func(l *domain.RiskLimits) {})
	if _, err := x.svc.StartAgent(x.ctx, x.admin, x.a.ID); err != nil {
		t.Fatal(err)
	}
	return ae
}

// limits sets the default limits with a $10 000 budget, changed by edit (agent stopped).
func (ae *agentEnv) limits(t *testing.T, edit func(*domain.RiskLimits)) {
	t.Helper()
	l := domain.DefaultRiskLimits
	l.TradingBudget = 10_000_00
	edit(&l)
	if _, err := ae.svc.SetRiskLimits(ae.ctx, ae.admin, l); err != nil {
		t.Fatal(err)
	}
}

func (ae *agentEnv) setCand(f func(domain.InstrumentID) *domain.Candidate) {
	ae.mu.Lock()
	defer ae.mu.Unlock()
	ae.cand = f
}

func (ae *agentEnv) tick(t *testing.T) AgentTrace {
	t.Helper()
	tr, err := ae.svc.AgentTick(ae.ctx, ae.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// reviewing switches the LLM reviewer on with answer.
func (ae *agentEnv) reviewing(t *testing.T, answer func(context.Context, ports.ReviewInput) (domain.Verdict, error)) *agentReviewer {
	t.Helper()
	r := &agentReviewer{answer: answer}
	ae.svc.d.Reviewer = r
	pr, err := ae.svc.SaveLLMProfile(ae.ctx, ae.admin, domain.LLMProfile{Name: "Desk", LLMConfig: domain.LLMConfig{Provider: "openai", Model: "gpt"}}, sp("k-desk"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ae.svc.SetLLMSettings(ae.ctx, ae.admin, domain.LLMSettings{Enabled: true, Active: pr.ID}); err != nil {
		t.Fatal(err)
	}
	return r
}

func verdict(v domain.Verdict, err error) func(context.Context, ports.ReviewInput) (domain.Verdict, error) {
	return func(context.Context, ports.ReviewInput) (domain.Verdict, error) { return v, err }
}

// checkIntents asserts every intent sent is long or short with a stop on the losing side of
// its order's entry, and its request id was persisted first (the RequireOrderRow hook).
func (ae *agentEnv) checkIntents(t *testing.T) {
	t.Helper()
	noViolations(t, ae.b)
	for _, in := range ae.b.Intents {
		o := ae.order(t, in.RequestID)
		switch {
		case in.Direction == domain.Long && in.StopLossRate >= o.Entry:
			t.Fatalf("long intent %+v: stop not below the entry %s", in, o.Entry)
		case in.Direction == domain.Short && in.StopLossRate <= o.Entry:
			t.Fatalf("short intent %+v: stop not above the entry %s", in, o.Entry)
		case !in.Direction.Valid():
			t.Fatalf("intent direction %q", in.Direction)
		}
	}
}

func lastCheck(it InstrumentTrace) TraceCheck {
	if len(it.Checks) == 0 {
		return TraceCheck{}
	}
	return it.Checks[len(it.Checks)-1]
}

func TestAgentTickEntersLong(t *testing.T) {
	ae := newAgentEnv(t)
	tr := ae.tick(t)
	if tr.Skipped != "" || tr.Entries != 1 || ae.b.Opens != 1 || len(tr.Instruments) != 1 {
		t.Fatalf("trace = %+v, opens %d", tr, ae.b.Opens)
	}
	it := tr.Instruments[0]
	var steps []TraceStep
	for _, c := range it.Checks {
		if !c.Pass {
			t.Fatalf("failed step %+v", c)
		}
		steps = append(steps, c.Step)
	}
	want := []TraceStep{StepSession, StepCandles, StepSignal, StepCosts, StepReview, StepPrices, StepReservations, StepRisk, StepOrder}
	if !slices.Equal(steps, want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
	in := ae.b.Intents[0]
	if in.Amount != 2_000_00 || in.Direction != domain.Long || it.OrderState != domain.OrderFilled || !it.Entered {
		t.Fatalf("intent %+v, trace %+v", in, it)
	}
	if o := ae.order(t, in.RequestID); o.Entry != 100_000_000 { // the ask, not the candidate's price
		t.Fatalf("entry = %s", o.Entry)
	}
	// the strategy never saw the forming bar
	for _, bs := range ae.bars {
		if last := bs[len(bs)-1]; last.Start.Add(AgentBarInterval).After(ae.clock.Now()) {
			t.Fatalf("forming bar %s given to the strategy at %s", last.Start, ae.clock.Now())
		}
	}
	ae.checkIntents(t)
	// a second tick: a position in AAPL is open, no second entry
	tr = ae.tick(t)
	if ae.b.Opens != 1 || tr.Entries != 0 || !strings.Contains(lastCheck(tr.Instruments[0]).Detail, "already open") {
		t.Fatalf("second tick = %+v", tr)
	}
}

func TestAgentTickSizeCaps(t *testing.T) {
	tests := []struct {
		name   string
		budget domain.Cents
		cash   domain.Cents // SimBroker cash (equity follows)
		held   domain.Cents // an open short position elsewhere (foreign)
		review func(context.Context, ports.ReviewInput) (domain.Verdict, error)
		want   domain.Cents // 0 = no Open
	}{
		{name: "approve: the playbook's size", budget: 10_000_00, cash: 1_000_000_00, want: 2_000_00},
		{name: "approve: capped by the remaining budget", budget: 10_000_00, cash: 1_000_000_00, held: 9_000_00, want: 1_000_00},
		{name: "approve: capped by fresh cash", budget: 10_000_00, cash: 1_500_00, held: 8_500_00, want: 1_500_00},
		{name: "reduce 0.5: halved", budget: 10_000_00, cash: 1_000_000_00, review: verdict(domain.Verdict{Kind: domain.VerdictReduce, SizeBp: 5_000}, nil), want: 1_000_00},
		{name: "reduce 0.5 then capped by the budget", budget: 10_000_00, cash: 1_000_000_00, held: 9_500_00, review: verdict(domain.Verdict{Kind: domain.VerdictReduce, SizeBp: 5_000}, nil), want: 500_00},
		{name: "multiplier 3 never enlarges", budget: 10_000_00, cash: 1_000_000_00, review: verdict(domain.Verdict{Kind: domain.VerdictReduce, SizeBp: 30_000}, nil), want: 2_000_00},
		{name: "approve with a size of 3 never enlarges", budget: 10_000_00, cash: 1_000_000_00, review: verdict(domain.Verdict{Kind: domain.VerdictApprove, SizeBp: 30_000}, nil), want: 2_000_00},
		{name: "increase is a reject", budget: 10_000_00, cash: 1_000_000_00, review: verdict(domain.Verdict{Kind: "increase", SizeBp: 20_000}, nil)},
		{name: "reject", budget: 10_000_00, cash: 1_000_000_00, review: verdict(domain.Verdict{Kind: domain.VerdictReject, Reason: "no"}, nil)},
		{name: "LLM error", budget: 10_000_00, cash: 1_000_000_00, review: verdict(domain.Verdict{Kind: domain.VerdictApprove}, errors.New("provider down"))},
		{name: "LLM timeout", budget: 10_000_00, cash: 1_000_000_00, review: verdict(domain.Verdict{}, context.DeadlineExceeded)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t)
			ae.b.Cash = tt.cash
			if tt.held > 0 {
				ae.b.Positions = append(ae.b.Positions, domain.Position{ID: 77, Instrument: 999, Amount: tt.held, OpenRate: 1_000_000, StopLossRate: 2_000_000, CFD: true, Leverage: 1})
			}
			if tt.review != nil {
				ae.reviewing(t, tt.review)
			}
			tr := ae.tick(t)
			if tt.want == 0 {
				if ae.b.Opens != 0 || tr.Entries != 0 {
					t.Fatalf("opened: %+v", ae.b.Intents)
				}
				return
			}
			if ae.b.Opens != 1 || ae.b.Intents[0].Amount != tt.want {
				t.Fatalf("intents %+v, want amount %s; trace %+v", ae.b.Intents, tt.want, tr)
			}
			ae.checkIntents(t)
		})
	}
}

// costsBroker overrides the SimBroker's costs.
type costsBroker struct {
	*fakes.SimBroker
	items []domain.CostItem
	err   error
}

func (c *costsBroker) Costs(context.Context, domain.InstrumentID, domain.Direction, domain.Cents) ([]domain.CostItem, error) {
	return c.items, c.err
}

// instMSFT is a second NYSE share of the agent tests.
const instMSFT domain.InstrumentID = 104

func (ae *agentEnv) addNYSE(t *testing.T, id domain.InstrumentID, sym string) {
	t.Helper()
	in := domain.Instrument{ID: id, Symbol: sym, Type: "Stocks", Market: domain.MarketNYSE}
	if err := ae.store.Instruments().Upsert(ae.ctx, in, domain.Eligibility{Instrument: id, RealLong: true, ShortCFD: true}, ae.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ae.store.Watchlist().Add(ae.ctx, ae.a.ID, id, "test", ae.clock.Now()); err != nil {
		t.Fatal(err)
	}
	ae.m.setQuote(id, 99_990_000, 100_000_000)
}

// storedOrder stores a non-final open order of ours (in flight).
func (ae *agentEnv) storedOrder(t *testing.T, id domain.InstrumentID, d domain.Direction, amount domain.Cents, st domain.OrderState) domain.Order {
	t.Helper()
	o := domain.Order{
		RequestID: fmt.Sprintf("00000000-0000-4000-8000-%012d", len(ae.openOrders(t))+1), AccountID: ae.a.ID, Mode: domain.ModeDemo,
		Kind: domain.OrderOpen, Direction: d, Instrument: id, Amount: amount, Entry: 100_000_000, StopLoss: 97_000_000,
	}
	if d == domain.Short {
		o.StopLoss = 103_000_000
	}
	o, err := domain.NewOrder(o, ae.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := ae.store.Orders().Create(ae.ctx, o); err != nil {
		t.Fatal(err)
	}
	if st != domain.OrderCreated {
		next, err := o.Transition(domain.OrderSent, ae.clock.Now(), "")
		if err == nil && st != domain.OrderSent {
			next, err = next.Transition(st, ae.clock.Now(), "")
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := ae.store.Orders().Update(ae.ctx, o, next); err != nil {
			t.Fatal(err)
		}
		o = next
	}
	return o
}

func TestAgentTickRefusals(t *testing.T) {
	tests := []struct {
		name  string
		watch []domain.InstrumentID
		setup func(t *testing.T, ae *agentEnv)
		skip  string // the tick's Skipped, or "" when an instrument step refuses
		step  TraceStep
	}{
		{name: "stopped", setup: func(t *testing.T, ae *agentEnv) { ae.setAgent(t, domain.AgentStopped) }, skip: "the agent is stopped"},
		{name: "paused", setup: func(t *testing.T, ae *agentEnv) { ae.setAgent(t, domain.AgentPaused) }, skip: "the agent is paused"},
		{name: "killed", setup: func(t *testing.T, ae *agentEnv) {
			if err := ae.svc.trip(ae.ctx, ae.a, "test"); err != nil {
				t.Fatal(err)
			}
		}, skip: "the agent is killed"},
		{name: "kill switch in the risk state only", setup: func(t *testing.T, ae *agentEnv) {
			if _, err := ae.svc.persistKill(ae.ctx, ae.a.ID, "test", ae.clock.Now()); err != nil {
				t.Fatal(err)
			}
		}, skip: "the kill switch is on"},
		{name: "unreconciled", setup: func(t *testing.T, ae *agentEnv) { ae.svc.setReconciled(ae.ctx, ae.a, false, "test mismatch") }, skip: "not reconciled"},
		{name: "real account", setup: func(t *testing.T, ae *agentEnv) {
			a := ae.a
			a.Mode = domain.ModeReal
			if err := ae.store.Accounts().Update(ae.ctx, a); err != nil {
				t.Fatal(err)
			}
		}, skip: "real mode is gated"},
		{name: "no calendar", setup: func(t *testing.T, ae *agentEnv) { ae.svc.d.Calendar = nil }, skip: "no market calendar"},
		{name: "no market data", setup: func(t *testing.T, ae *agentEnv) { ae.svc.d.Market = nil }, skip: "not configured"},
		{name: "budget 0", setup: func(t *testing.T, ae *agentEnv) {
			ae.setAgent(t, domain.AgentStopped)
			ae.limits(t, func(l *domain.RiskLimits) { l.TradingBudget = 0 })
			ae.setAgent(t, domain.AgentRunning)
		}, skip: "no trading budget"},
		{name: "lease held by another process", setup: func(t *testing.T, ae *agentEnv) {
			_ = ae.store.AgentLeases().Release(ae.ctx, ae.a.ID, ae.svc.leaseHolder())
			if err := ae.store.AgentLeases().Acquire(ae.ctx, ae.a.ID, "other", ae.clock.Now(), ae.clock.Now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
		}, skip: "another process holds the lease"},
		{name: "empty watchlist", setup: func(t *testing.T, ae *agentEnv) {
			if err := ae.store.Watchlist().Remove(ae.ctx, ae.a.ID, instNYSE); err != nil {
				t.Fatal(err)
			}
		}, skip: "the watchlist is empty"},
		{name: "portfolio unreadable", setup: func(t *testing.T, ae *agentEnv) { ae.b.PortfolioErr = domain.ErrUnavailable }, skip: "reading the portfolio failed"},
		{name: "LSE entries off (OD9)", watch: []domain.InstrumentID{instLSE}, step: StepSession},
		{name: "ASX not in session", watch: []domain.InstrumentID{instASX}, step: StepSession},
		{name: "within 30 min of the close", setup: func(t *testing.T, ae *agentEnv) {
			ae.clock.Advance(time.Date(2026, 11, 23, 20, 35, 5, 0, time.UTC).Sub(ae.clock.Now()))
		}, step: StepSession},
		{name: "calendar error", setup: func(t *testing.T, ae *agentEnv) { ae.svc.d.Calendar = fixedCalendar{} }, step: StepSession},
		{name: "candles unreadable", setup: func(t *testing.T, ae *agentEnv) { ae.m.Err = domain.ErrUnavailable }, step: StepCandles},
		{name: "no signal", setup: func(t *testing.T, ae *agentEnv) {
			ae.setCand(func(domain.InstrumentID) *domain.Candidate { return nil })
		}, step: StepSignal},
		{name: "candidate for another instrument", setup: func(t *testing.T, ae *agentEnv) {
			ae.setCand(func(domain.InstrumentID) *domain.Candidate { return longCand(instASX) })
		}, step: StepSignal},
		{name: "candidate stop on the wrong side", setup: func(t *testing.T, ae *agentEnv) {
			ae.setCand(func(id domain.InstrumentID) *domain.Candidate {
				c := longCand(id)
				c.StopLossRate = 101_000_000
				return c
			})
		}, step: StepSignal},
		{name: "short with shorts off", setup: func(t *testing.T, ae *agentEnv) {
			ae.setAgent(t, domain.AgentStopped)
			ae.limits(t, func(l *domain.RiskLimits) { l.MaxShort = 0 })
			ae.setAgent(t, domain.AgentRunning)
			ae.setCand(shortCand)
		}, step: StepSignal},
		{name: "costs call fails (OD8)", setup: func(t *testing.T, ae *agentEnv) {
			ae.svc.d.Broker = &costsBroker{SimBroker: ae.b, err: domain.ErrUnavailable}
		}, step: StepCosts},
		{name: "costs at the gross gain (OD8)", setup: func(t *testing.T, ae *agentEnv) {
			ae.svc.d.Broker = &costsBroker{SimBroker: ae.b, items: []domain.CostItem{{Type: "markup", Value: 120_000_000, Currency: "USD"}}}
		}, step: StepCosts},
		{name: "no take-profit (OD8)", setup: func(t *testing.T, ae *agentEnv) {
			ae.setCand(func(id domain.InstrumentID) *domain.Candidate { c := longCand(id); c.TakeProfitRate = 0; return c })
		}, step: StepCosts},
		{name: "reviewing on without a reviewer", setup: func(t *testing.T, ae *agentEnv) {
			ae.reviewing(t, verdict(domain.Verdict{Kind: domain.VerdictApprove}, nil))
			ae.svc.d.Reviewer = nil
		}, step: StepReview},
		{name: "stale quote", setup: func(t *testing.T, ae *agentEnv) { ae.m.QuoteAge = time.Minute }, step: StepPrices},
		{name: "price passed the stop", setup: func(t *testing.T, ae *agentEnv) { ae.m.setQuote(instNYSE, 96_000_000, 96_500_000) }, step: StepPrices},
		{name: "price passed the target", setup: func(t *testing.T, ae *agentEnv) { ae.m.setQuote(instNYSE, 106_000_000, 106_500_000) }, step: StepPrices},
		{name: "stale portfolio", setup: func(t *testing.T, ae *agentEnv) {
			ae.svc.d.Broker = &hookBroker{SimBroker: ae.b, pf: func(ctx context.Context) (domain.Portfolio, error) {
				p, err := ae.b.Portfolio(ctx)
				p.AsOf = p.AsOf.Add(-10 * time.Second)
				return p, err
			}}
		}, step: StepRisk},
		{name: "order in flight for the instrument", setup: func(t *testing.T, ae *agentEnv) {
			ae.storedOrder(t, instNYSE, domain.Long, 100_00, domain.OrderPending)
		}, step: StepRisk},
		{name: "an order with an unknown outcome", setup: func(t *testing.T, ae *agentEnv) {
			ae.storedOrder(t, instASX, domain.Long, 100_00, domain.OrderUnknown)
		}, step: StepRisk},
		{name: "instrument blocked", setup: func(t *testing.T, ae *agentEnv) {
			if err := ae.store.InstrumentBlocks().Block(ae.ctx, domain.InstrumentBlock{AccountID: ae.a.ID, Instrument: instNYSE, Reason: "test", At: ae.clock.Now()}); err != nil {
				t.Fatal(err)
			}
		}, step: StepRisk},
		{name: "daily latch", setup: func(t *testing.T, ae *agentEnv) {
			st := domain.AccountRiskState{AccountID: ae.a.ID, State: domain.RiskState{Day: domain.RiskDay(ae.clock.Now()), StartOfDayEquity: 1_000_000_00, HighWaterMark: 1_000_000_00, DayStopped: true}}
			if _, err := ae.store.RiskStates().Put(ae.ctx, st); err != nil {
				t.Fatal(err)
			}
		}, step: StepRisk},
		{name: "budget used up by an open position", setup: func(t *testing.T, ae *agentEnv) {
			ae.b.Positions = append(ae.b.Positions, domain.Position{ID: 77, Instrument: 999, Amount: 10_000_00, OpenRate: 1_000_000, StopLossRate: 900_000, RealAsset: true, Long: true, Leverage: 1})
		}, step: StepRisk},
		{name: "a foreign position that does not conform", setup: func(t *testing.T, ae *agentEnv) {
			ae.b.Positions = append(ae.b.Positions, domain.Position{ID: 78, Instrument: 999, Amount: 100_00, OpenRate: 1_000_000, Long: true, CFD: true, Leverage: 2})
		}, step: StepRisk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t, tt.watch...)
			if tt.setup != nil {
				tt.setup(t, ae)
			}
			tr := ae.tick(t)
			if ae.b.Opens != 0 || tr.Entries != 0 {
				t.Fatalf("opened %+v; trace %+v", ae.b.Intents, tr)
			}
			if tt.skip != "" {
				if !strings.Contains(tr.Skipped, tt.skip) {
					t.Fatalf("skipped = %q, want %q", tr.Skipped, tt.skip)
				}
				return
			}
			if tr.Skipped != "" || len(tr.Instruments) == 0 {
				t.Fatalf("trace = %+v", tr)
			}
			for _, it := range tr.Instruments {
				if c := lastCheck(it); c.Pass || c.Step != tt.step {
					t.Fatalf("last check = %+v, want a failed %s; trace %+v", c, tt.step, it)
				}
			}
			noViolations(t, ae.b)
		})
	}
}

func TestAgentTickShorts(t *testing.T) {
	t.Run("short stop above the entry, capped at the short limit", func(t *testing.T) {
		ae := newAgentEnv(t)
		ae.addNYSE(t, instMSFT, "MSFT")
		ae.setCand(shortCand)
		tr := ae.tick(t)
		// 25 % of the $10 000 budget: $2 000 for AAPL, then $500 left for MSFT
		if tr.Entries != 2 || ae.b.Opens != 2 {
			t.Fatalf("trace %+v", tr)
		}
		var total domain.Cents
		for _, in := range ae.b.Intents {
			if in.Direction != domain.Short || in.StopLossRate != 103_000_000 {
				t.Fatalf("intent %+v", in)
			}
			total += in.Amount
		}
		if total != 2_500_00 || ae.b.Intents[1].Amount != 500_00 {
			t.Fatalf("short amounts = %+v", ae.b.Intents)
		}
		if o := ae.order(t, ae.b.Intents[0].RequestID); o.Entry != 99_990_000 { // the bid
			t.Fatalf("short entry = %s", o.Entry)
		}
		ae.checkIntents(t)
	})
	t.Run("a pending short order counts against the short limit", func(t *testing.T) {
		ae := newAgentEnv(t)
		ae.addNYSE(t, instMSFT, "MSFT")
		ae.storedOrder(t, instMSFT, domain.Short, 2_200_00, domain.OrderPending)
		ae.setCand(shortCand)
		ae.tick(t)
		if ae.b.Opens != 1 || ae.b.Intents[0].Amount != 300_00 || ae.b.Intents[0].Instrument != instNYSE {
			t.Fatalf("intents %+v", ae.b.Intents)
		}
		ae.checkIntents(t)
	})
	t.Run("lower short limit", func(t *testing.T) {
		ae := newAgentEnv(t)
		ae.setAgent(t, domain.AgentStopped)
		ae.limits(t, func(l *domain.RiskLimits) { l.MaxShort = 1_000 })
		ae.setAgent(t, domain.AgentRunning)
		ae.setCand(shortCand)
		ae.tick(t)
		if ae.b.Opens != 1 || ae.b.Intents[0].Amount != 1_000_00 {
			t.Fatalf("intents %+v", ae.b.Intents)
		}
	})
}

func TestAgentTickDrawdownKills(t *testing.T) {
	ae := newAgentEnv(t)
	st := domain.AccountRiskState{AccountID: ae.a.ID, State: domain.RiskState{Day: domain.RiskDay(ae.clock.Now()), StartOfDayEquity: 1_000_000_00, HighWaterMark: 2_000_000_00}}
	if _, err := ae.store.RiskStates().Put(ae.ctx, st); err != nil {
		t.Fatal(err)
	}
	tr := ae.tick(t)
	if !tr.Killed || ae.b.Opens != 0 || ae.svc.agentState(ae.ctx, ae.a.ID) != domain.AgentKilled {
		t.Fatalf("trace %+v state %s", tr, ae.svc.agentState(ae.ctx, ae.a.ID))
	}
	es, _ := ae.store.Events().List(ae.ctx, []string{ae.a.ID}, 50)
	i := slices.IndexFunc(es, func(e domain.Event) bool { return e.Kind == "agent.killed" })
	if i < 0 || !strings.Contains(es[i].Detail, "positions opened outside cactusTrade are left alone") {
		t.Fatalf("kill event = %+v", es)
	}
	if tr := ae.tick(t); !strings.Contains(tr.Skipped, "killed") || ae.b.Opens != 0 {
		t.Fatalf("tick after the kill = %+v", tr)
	}
}

func TestAgentTickOneAtATime(t *testing.T) {
	ae := newAgentEnv(t)
	locks := ae.svc.rt.agent(ae.a.ID)
	locks.entry.Lock()
	tr := ae.tick(t)
	locks.entry.Unlock()
	if tr.Skipped != "the previous tick is still running" || ae.b.Opens != 0 {
		t.Fatalf("trace %+v", tr)
	}
	if _, err := ae.svc.AgentTick(ae.ctx, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
}

func TestAgentTickStopsWhenPausedMidRound(t *testing.T) {
	ae := newAgentEnv(t)
	ae.addNYSE(t, instMSFT, "MSFT")
	// the reviewer pauses the agent while it reviews the first instrument
	ae.reviewing(t, func(context.Context, ports.ReviewInput) (domain.Verdict, error) {
		ae.setAgent(t, domain.AgentPaused)
		return domain.Verdict{Kind: domain.VerdictApprove}, nil
	})
	tr := ae.tick(t)
	if ae.b.Opens != 0 || len(tr.Instruments) != 1 || lastCheck(tr.Instruments[0]).Step != StepOrder {
		t.Fatalf("trace %+v", tr)
	}
}

func TestAgentLoop(t *testing.T) {
	ae := newAgentEnv(t)
	ae.clock.Advance(-3 * time.Second) // 15:00:02: the first tick is due at 15:00:05
	ctx, cancel := context.WithCancel(ae.ctx)
	defer cancel()
	decided := make(chan time.Time, 1)
	ae.setCand(func(domain.InstrumentID) *domain.Candidate {
		select {
		case decided <- ae.clock.Now():
		default:
		}
		return nil
	})
	// The first wait moves the fake clock to the decision time; the next blocks until the
	// test ends the loop (the tick itself never waits: it places nothing).
	var mu sync.Mutex
	sleeps := 0
	ae.svc.d.Sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		sleeps++
		n := sleeps
		mu.Unlock()
		if n == 1 {
			ae.clock.Advance(d)
			return ctx.Err()
		}
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan struct{})
	go func() {
		ae.svc.AgentLoop(ctx)
		close(done)
	}()
	var at time.Time
	select {
	case at = <-decided:
	case <-time.After(10 * time.Second):
		t.Fatal("no decision from the loop")
	}
	cancel()
	<-done
	if !at.Equal(agentStart) {
		t.Fatalf("decision at %s, want %s (bar close + 5 s)", at, agentStart)
	}
	// a stopped agent is not ticked
	ae.setAgent(t, domain.AgentStopped)
	var wg sync.WaitGroup
	ae.svc.agentTickAll(ae.ctx, &wg)
	wg.Wait()
	select {
	case <-decided:
		t.Fatal("a stopped agent was ticked")
	default:
	}
}

func TestPlaceEntryCancelledBeforeSend(t *testing.T) {
	x := newExecEnv(t)
	ctx, cancel := context.WithCancel(x.ctx)
	cancel()
	o, err := x.svc.placeEntry(ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
	if !errors.Is(err, context.Canceled) || x.b.Opens != 0 {
		t.Fatalf("placeEntry = %+v %v, opens %d", o, err, x.b.Opens)
	}
	if st := x.order(t, o.RequestID); st.State != domain.OrderAbandoned {
		t.Fatalf("state = %s", st.State)
	}
}
