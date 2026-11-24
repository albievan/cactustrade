package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
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

// The sequence property test (PLAN 4.12): seeded random scenarios in which the supervisor and
// the agent run tick by tick against the simulated eToro account, with random prices, fills,
// rejections, lost answers, 429s, LLM verdicts (garbage included), limits, shorts, kills and
// resets. Every order the agent sends is checked when it reaches the broker, and the account
// after every tick. A fake clock and a sleep that only moves it keep it fast and
// reproducible: a failure names its seed and tick.

const (
	propScenarios  = 300
	propTicks      = 200
	propMinEntries = 500
	propForeignID  = 424_242
	propForeignIn  = domain.InstrumentID(999)
)

// propInstruments are the shares of every scenario: NYSE, LSE (entries off, OD9) and ASX.
var propInstruments = []struct {
	id  domain.InstrumentID
	sym string
}{
	{instNYSE, "AAPL"}, {instLSE, "BARC.L"}, {instASX, "CBA.ASX"}, {104, "MSFT"}, {105, "NVDA"}, {106, "BHP.ASX"},
}

// propStats counts what the scenarios exercised.
type propStats struct {
	entries, shorts, resends, manualKills, trips, resets, latches, stopOuts, faults, limitChanges int
}

func (p *propStats) add(o propStats) {
	p.entries += o.entries
	p.shorts += o.shorts
	p.resends += o.resends
	p.manualKills += o.manualKills
	p.trips += o.trips
	p.resets += o.resets
	p.latches += o.latches
	p.stopOuts += o.stopOuts
	p.faults += o.faults
	p.limitChanges += o.limitChanges
}

func TestAgentSequenceProperties(t *testing.T) {
	var (
		mu    sync.Mutex
		total propStats
	)
	t.Run("scenarios", func(t *testing.T) {
		for seed := range uint64(propScenarios) {
			t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
				t.Parallel()
				st := runPropScenario(t, seed, propTicks)
				mu.Lock()
				total.add(st)
				mu.Unlock()
			})
		}
	})
	t.Logf("exercised: %+v", total)
	switch {
	case total.entries < propMinEntries:
		t.Fatalf("only %d entries exercised, want at least %d", total.entries, propMinEntries)
	case total.shorts == 0 || total.resends == 0 || total.manualKills == 0 || total.trips == 0 ||
		total.resets == 0 || total.latches == 0 || total.stopOuts == 0 || total.faults == 0 || total.limitChanges == 0:
		t.Fatalf("a behaviour was never exercised: %+v", total)
	}
}

// propScenario is one seeded scenario.
type propScenario struct {
	t     *testing.T
	seed  uint64
	tick  int
	r     *rand.Rand
	ae    *agentEnv
	price map[domain.InstrumentID]domain.Micros
	ids   []domain.InstrumentID // every priced instrument, sorted (deterministic order)
	// stale: this tick's quotes are a minute old.
	stale bool
	cands map[domain.InstrumentID]*domain.Candidate
	// faulty: this tick the portfolio cannot be read (exits fall back, checks wait a tick).
	faulty bool
	// sent are the results of every Open per request id, in order.
	sent    map[string][]error
	foreign bool
	stats   propStats
}

func (s *propScenario) fail(format string, args ...any) {
	s.t.Helper()
	s.t.Fatalf("seed %d tick %d (%s): %s", s.seed, s.tick, s.ae.clock.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func runPropScenario(t *testing.T, seed uint64, ticks int) propStats {
	t.Helper()
	ae := newAgentEnv(t, instNYSE, instLSE, instASX)
	s := &propScenario{
		t: t, seed: seed, r: rand.New(rand.NewPCG(seed, seed*7919+17)), ae: ae,
		price: map[domain.InstrumentID]domain.Micros{}, cands: map[domain.InstrumentID]*domain.Candidate{}, sent: map[string][]error{},
	}
	for _, in := range propInstruments[3:] {
		m, _ := domain.MarketForSymbol(in.sym)
		inst := domain.Instrument{ID: in.id, Symbol: in.sym, Type: "Stocks", Market: m}
		if err := ae.store.Instruments().Upsert(ae.ctx, inst, domain.Eligibility{Instrument: in.id}, ae.clock.Now()); err != nil {
			t.Fatal(err)
		}
		if err := ae.store.Watchlist().Add(ae.ctx, ae.a.ID, in.id, "test", ae.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	for _, in := range propInstruments {
		s.ids = append(s.ids, in.id)
	}
	s.ids = append(s.ids, propForeignIn)
	slices.Sort(s.ids)
	for _, id := range s.ids {
		s.price[id] = domain.Micros(20_000_000 + s.r.Int64N(480_000_000))
	}
	ae.svc.d.Sleep = func(ctx context.Context, d time.Duration) error {
		if d > 0 {
			ae.clock.Advance(d)
		}
		return ctx.Err()
	}
	ae.svc.d.Market = s
	ae.svc.d.Broker = &propBroker{SimBroker: ae.b, s: s}
	ae.svc.evalOverride = func(id domain.InstrumentID, _ []domain.Candle) strategy.Decision {
		if c := s.cands[id]; c != nil {
			cp := *c
			return strategy.Decision{Candidate: &cp}
		}
		return strategy.Decision{Reason: "no signal (scenario)"}
	}
	// The account: cash, perhaps a position the owner opened (OD1: never touched), limits.
	if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	ae.b.Cash = domain.Cents(50_000_00 + s.r.Int64N(1_950_000_00))
	if s.r.IntN(2) == 0 {
		amt := ae.b.Cash * domain.Cents(10+s.r.Int64N(50)) / 100
		ae.b.Cash -= amt
		ae.b.Positions = append(ae.b.Positions, domain.Position{
			ID: propForeignID, Instrument: propForeignIn, OpenRate: s.price[propForeignIn], Units: 1, Amount: amt,
			RealAsset: true, Long: true, Leverage: 1,
		})
		s.foreign = true
	}
	ae.b.Prices = map[domain.InstrumentID]domain.Micros{}
	if s.r.IntN(2) == 0 {
		ae.reviewing(t, s.verdict)
	}
	s.setLimits()
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	for range ticks {
		s.step()
	}
	for _, k := range ae.eventKinds(t) {
		if k == "risk.daily_stop" {
			s.stats.latches++
		}
	}
	return s.stats
}

// setLimits stores random valid limits with a budget at most the current equity (the agent
// must be stopped).
func (s *propScenario) setLimits() {
	pf, err := s.ae.b.Portfolio(ports.WithAccount(s.ae.ctx, s.ae.a))
	if err != nil {
		s.fail("portfolio: %v", err)
	}
	eq := pf.Equity()
	l := domain.DefaultRiskLimits
	l.TradingBudget = min(eq, domain.Cents(1_000_00+s.r.Int64N(int64(max(eq, 2_000_00)))))
	l.DailyLoss = domain.BasisPoints(50 + s.r.IntN(450))
	l.Drawdown = domain.BasisPoints(300 + s.r.IntN(1_200))
	l.MaxPosition = []domain.BasisPoints{0, 500, 1_000, 2_000, 5_000}[s.r.IntN(5)]
	l.MaxOpenPositions = 1 + s.r.IntN(domain.HardMaxOpenPositions)
	l.FlattenBeforeClose = time.Duration(5+s.r.IntN(16)) * time.Minute
	l.NoEntryBeforeClose = l.FlattenBeforeClose + time.Duration(s.r.IntN(31))*time.Minute
	l.MaxShort = domain.BasisPoints(s.r.IntN(int(domain.HardMaxShort) + 1))
	if s.r.IntN(5) == 0 {
		l.MaxShort = 0
	}
	if _, err := s.ae.svc.SetRiskLimits(s.ae.ctx, s.ae.admin, l); err != nil {
		s.fail("limits %+v: %v", l, err)
	}
}

// verdict is a random reviewer answer, garbage included.
func (s *propScenario) verdict(context.Context, ports.ReviewInput) (domain.Verdict, error) {
	switch s.r.IntN(10) {
	case 0:
		return domain.Verdict{Kind: domain.VerdictApprove}, errors.New("provider down")
	case 1:
		return domain.Verdict{}, context.DeadlineExceeded
	case 2:
		return domain.Verdict{Kind: domain.VerdictReject, Reason: "no"}, nil
	case 3:
		return domain.Verdict{Kind: "increase", SizeBp: 30_000}, nil
	case 4:
		return domain.Verdict{Kind: domain.VerdictReduce, SizeBp: domain.BasisPoints(s.r.IntN(40_001) - 10_000)}, nil
	case 5:
		return domain.Verdict{Kind: "", SizeBp: 99_999}, nil
	}
	return domain.Verdict{Kind: domain.VerdictApprove, SizeBp: domain.BasisPoints(s.r.IntN(50_000))}, nil
}

// step is one tick: the clock moves to the next bar close + 5 s, the owner may act, prices
// move, faults are scripted, then the supervisor and the agent run, and the invariants are
// checked.
func (s *propScenario) step() {
	s.tick++
	ae := s.ae
	now := ae.clock.Now()
	ae.clock.Advance(domain.NextDecision(now, AgentBarInterval, AgentDecisionDelay).Sub(now))
	s.control()
	s.movePrices()
	s.scriptFaults()
	s.propose()
	rep, err := ae.svc.Supervise(ae.ctx, ae.a.ID)
	if err != nil {
		s.fail("supervise: %v", err)
	}
	if rep.Tripped {
		s.stats.trips++
	}
	s.checkAfterSupervisor()
	tr, err := ae.svc.AgentTick(ae.ctx, ae.a.ID)
	if err != nil {
		s.fail("agent tick: %v", err)
	}
	if tr.Killed {
		s.stats.trips++
	}
	s.checkAfterTick()
	ae.b.PortfolioErr, s.stale, s.faulty = nil, false, false
}

// control is the owner: a kill now and then, a reset and restart after a kill, a change of
// limits, a pause and a start.
func (s *propScenario) control() {
	ae := s.ae
	st := ae.svc.agentState(ae.ctx, ae.a.ID)
	killed, err := ae.svc.isKilled(ae.ctx, ae.a.ID)
	if err != nil {
		s.fail("kill switch: %v", err)
	}
	start := func() {
		if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID); err != nil && !errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrInvalid) {
			s.fail("start: %v", err)
		}
	}
	switch {
	case killed:
		if s.r.IntN(15) == 0 {
			if _, err := ae.svc.ResetKill(ae.ctx, ae.admin, ae.a.ID, ResetPhrase(ae.a.Name)); err != nil {
				s.fail("reset: %v", err)
			}
			s.stats.resets++
			start()
		}
	case s.r.IntN(250) == 0:
		if _, err := ae.svc.Kill(ae.ctx, ae.admin, ae.a.ID, "scenario"); err != nil {
			s.fail("kill: %v", err)
		}
		s.stats.manualKills++
	case st == domain.AgentRunning:
		switch s.r.IntN(80) {
		case 0:
			if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
				s.fail("stop: %v", err)
			}
			s.setLimits()
			s.stats.limitChanges++
			start()
		case 1:
			if _, err := ae.svc.PauseAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
				s.fail("pause: %v", err)
			}
		}
	case st != domain.AgentRunning && s.r.IntN(6) == 0:
		start()
	}
}

// pnl is a position's unrealised result at price px (cents).
func pnl(p domain.Position, px domain.Micros) domain.Cents {
	if p.OpenRate <= 0 {
		return 0
	}
	diff := int64(px - p.OpenRate)
	if !p.Long {
		diff = -diff
	}
	return domain.Cents(int64(p.Amount) * diff / int64(p.OpenRate))
}

// movePrices walks every price (now and then a shock), lets eToro close positions whose stop
// or take-profit was hit, and marks the rest to market.
func (s *propScenario) movePrices() {
	b := s.ae.b
	for _, id := range s.ids {
		bp := int64(s.r.IntN(81) - 40)
		if id == propForeignIn {
			bp *= 3
		}
		if s.r.IntN(100) == 0 {
			bp = int64(s.r.IntN(2)*2-1) * int64(300+s.r.IntN(900))
		}
		p := s.price[id]
		p = max(p+p*domain.Micros(bp)/10_000, 1_000_000)
		s.price[id] = p
		b.Prices[id] = p
	}
	keep := b.Positions[:0]
	for _, p := range b.Positions {
		px := s.price[p.Instrument]
		stop := p.StopLossRate > 0 && (p.Long && px <= p.StopLossRate || !p.Long && px >= p.StopLossRate)
		target := p.TakeProfitRate > 0 && (p.Long && px >= p.TakeProfitRate || !p.Long && px <= p.TakeProfitRate)
		p.UnrealisedPnL = pnl(p, px)
		if (stop || target) && p.ID != propForeignID {
			b.Cash += p.Amount + p.UnrealisedPnL
			s.stats.stopOuts++
			continue
		}
		keep = append(keep, p)
	}
	b.Positions = keep
}

// Quotes are the scenario's prices with a 1 bp half-spread, stamped now (or a minute ago on a
// stale tick).
func (s *propScenario) Quotes(_ context.Context, ids []domain.InstrumentID) ([]domain.Quote, error) {
	at := s.ae.clock.Now()
	if s.stale {
		at = at.Add(-time.Minute)
	}
	var out []domain.Quote
	for _, id := range ids {
		if p, ok := s.price[id]; ok {
			out = append(out, domain.Quote{Instrument: id, Bid: p - p/10_000, Ask: p + p/10_000, At: at})
		}
	}
	return out, nil
}

// Candles returns 30 closed bars and the forming one.
func (s *propScenario) Candles(_ context.Context, id domain.InstrumentID, iv time.Duration, _ int) ([]domain.Candle, error) {
	last := s.ae.clock.Now().Truncate(iv)
	p := s.price[id]
	out := make([]domain.Candle, 31)
	for i := range out {
		out[i] = domain.Candle{Start: last.Add(-time.Duration(30-i) * iv), Open: p, High: p, Low: p, Close: p, Volume: 100}
	}
	return out, nil
}

// Search finds nothing.
func (s *propScenario) Search(context.Context, string) ([]domain.Instrument, error) { return nil, nil }

// Instruments finds nothing.
func (s *propScenario) Instruments(context.Context, []domain.InstrumentID) ([]domain.Instrument, error) {
	return nil, nil
}

var (
	propOpenOutcomes = []fakes.SimOutcome{
		fakes.SimFill, fakes.SimFill, fakes.SimFill, fakes.SimFill, fakes.SimFill, fakes.SimFill, fakes.SimFill, fakes.SimFill,
		fakes.SimPendingThenFill, fakes.SimPendingThenFill, fakes.SimPending, fakes.SimReject, fakes.SimPartial,
		fakes.SimLostAnswer, fakes.SimRateLimited, fakes.SimRateLimited, fakes.SimServerError, fakes.SimRefused,
		fakes.SimLeverage2, fakes.SimWrongSettlement, fakes.SimNoStop,
	}
	// Closes eToro accepts sooner or later (a close stuck pending or refused is a case for the
	// executor tests, not for the flatten invariant).
	propCloseFailures = []fakes.SimOutcome{fakes.SimLostAnswer, fakes.SimRateLimited, fakes.SimServerError}
)

// scriptFaults scripts the broker's answers for this tick and, now and then, an unreadable
// portfolio or stale quotes.
func (s *propScenario) scriptFaults() {
	b := s.ae.b
	b.OpenScript = b.OpenScript[:0]
	for range 6 {
		b.OpenScript = append(b.OpenScript, propOpenOutcomes[s.r.IntN(len(propOpenOutcomes))])
	}
	b.CloseScript = b.CloseScript[:0]
	run := 0
	for range 40 {
		out := fakes.SimFill
		if run < 3 && s.r.IntN(7) == 0 {
			out = propCloseFailures[s.r.IntN(len(propCloseFailures))]
			run++
		} else {
			run = 0
		}
		b.CloseScript = append(b.CloseScript, out)
	}
	b.PendingLookups = 1 + s.r.IntN(3)
	if s.r.IntN(40) == 0 {
		b.PortfolioErr = fmt.Errorf("scenario: %w", domain.ErrUnavailable)
		s.faulty = true
		s.stats.faults++
	}
	s.stale = s.r.IntN(25) == 0
}

// propose draws this tick's candidates: half the instruments get one, long or short, some
// without a take-profit or with a stop on the wrong side, sized up to the budget.
func (s *propScenario) propose() {
	budget := s.ae.svc.riskLimits(s.ae.ctx, s.ae.a.ID).TradingBudget
	for _, id := range s.ids {
		s.cands[id] = nil
		if id == propForeignIn || s.r.IntN(2) == 0 {
			continue
		}
		mid := s.price[id]
		c := &domain.Candidate{Instrument: id, Direction: domain.Long, Playbook: "scenario", Reason: "random", Entry: mid}
		if s.r.IntN(3) == 0 {
			c.Direction = domain.Short
		}
		sign := domain.Micros(1)
		if c.Direction == domain.Short {
			sign = -1
		}
		stop := mid * domain.Micros(50+s.r.IntN(450)) / 10_000
		c.StopLossRate = mid - sign*stop
		if s.r.IntN(20) == 0 {
			c.StopLossRate = mid + sign*stop // wrong side: refused
		}
		if s.r.IntN(10) != 0 {
			c.TakeProfitRate = mid + sign*mid*domain.Micros(100+s.r.IntN(900))/10_000
		}
		c.SuggestedSize = 1 + domain.Cents(s.r.Int64N(int64(max(budget, 1))))
		s.cands[id] = c
	}
}

// propBroker checks every open order when it reaches the broker.
type propBroker struct {
	*fakes.SimBroker
	s *propScenario
}

// Open checks the order against the invariants, then lets the simulated account answer.
func (b *propBroker) Open(ctx context.Context, in domain.OrderIntent) (domain.OrderStatus, error) {
	b.s.checkOpen(ctx, in)
	st, err := b.SimBroker.Open(ctx, in)
	b.s.sent[in.RequestID] = append(b.s.sent[in.RequestID], err)
	return st, err
}

// checkOpen asserts, for an order about to be sent: one of the two shapes; persisted first as
// a sent open order with this send counted; a resend only after a 429; the agent running and
// not killed, latched or past the drawdown; inside the entry window of a market that takes
// entries; never larger than the candidate; and, with the account as it is now (prices do
// not move within a tick, so this is the account the decision saw), within fresh cash minus
// the other reservations, the budget and the short limit.
func (s *propScenario) checkOpen(ctx context.Context, in domain.OrderIntent) {
	s.t.Helper()
	ae := s.ae
	now := ae.clock.Now()
	if !in.Direction.Valid() || in.StopLossRate <= 0 || in.Amount <= 0 {
		s.fail("intent %+v is not a long or a short with a stop", in)
	}
	o, err := ae.store.Orders().Get(ae.ctx, in.RequestID)
	switch {
	case err != nil:
		s.fail("open %s sent without a stored order: %v", in.RequestID, err)
	case o.Kind != domain.OrderOpen || o.State != domain.OrderSent || o.Sends != len(s.sent[in.RequestID])+1:
		s.fail("open %s: stored as %s/%s with %d sends before send %d", in.RequestID, o.Kind, o.State, o.Sends, len(s.sent[in.RequestID])+1)
	case o.Direction != in.Direction || o.Instrument != in.Instrument || o.Amount != in.Amount || o.StopLoss != in.StopLossRate || o.TakeProfit != in.TakeProfitRate:
		s.fail("open %s differs from its stored order", in.RequestID)
	}
	for i, prev := range s.sent[in.RequestID] {
		if na, ok := errors.AsType[*domain.NotAcceptedError](prev); !ok || !na.RateLimited {
			s.fail("open %s resent after send %d answered %v (only a 429 allows a resend)", in.RequestID, i+1, prev)
		}
	}
	if len(s.sent[in.RequestID]) > 0 {
		s.stats.resends++
	} else {
		s.stats.entries++
		if in.Direction == domain.Short {
			s.stats.shorts++
		}
	}
	// Shape: long stop < entry < short stop, the entry being the ask (long) or bid (short).
	q, _ := s.Quotes(ctx, []domain.InstrumentID{in.Instrument})
	want := q[0].Ask
	if in.Direction == domain.Short {
		want = q[0].Bid
	}
	switch {
	case o.Entry != want:
		s.fail("open %s: entry %s is not the fresh %s price %s", in.RequestID, o.Entry, in.Direction, want)
	case in.Direction == domain.Long && !longShape(o):
		s.fail("long %s: stop %s / entry %s / target %s", in.RequestID, o.StopLoss, o.Entry, o.TakeProfit)
	case in.Direction == domain.Short && !shortShape(o):
		s.fail("short %s: stop %s / entry %s / target %s", in.RequestID, o.StopLoss, o.Entry, o.TakeProfit)
	}
	// The reviewer and the engine only shrink.
	if c := s.cands[in.Instrument]; c == nil || in.Amount > c.SuggestedSize || in.Direction != c.Direction || in.StopLossRate != c.StopLossRate {
		s.fail("open %s: %+v is not a shrunk copy of the candidate %+v", in.RequestID, in, c)
	}
	// The agent's state and the risk state.
	l := ae.svc.riskLimits(ae.ctx, ae.a.ID)
	if st := ae.svc.agentState(ae.ctx, ae.a.ID); st != domain.AgentRunning {
		s.fail("open %s while the agent is %s", in.RequestID, st)
	}
	rs, err := ae.store.RiskStates().Get(ae.ctx, ae.a.ID)
	if err != nil {
		s.fail("open %s without a risk state: %v", in.RequestID, err)
	}
	pfErr := ae.b.PortfolioErr
	ae.b.PortfolioErr = nil
	pf, err := ae.b.Portfolio(ports.WithAccount(ae.ctx, ae.a))
	ae.b.PortfolioErr = pfErr
	if err != nil {
		s.fail("portfolio: %v", err)
	}
	equity := pf.Equity()
	switch {
	case rs.State.Killed:
		s.fail("open %s after the kill switch (%s)", in.RequestID, rs.KillReason)
	case rs.State.Day != domain.RiskDay(now):
		s.fail("open %s on a risk state of %s", in.RequestID, rs.State.Day)
	case rs.State.DayStopped || equity <= domain.DailyFloor(rs.State, l):
		s.fail("open %s after the daily loss limit (equity %s, floor %s)", in.RequestID, equity, domain.DailyFloor(rs.State, l))
	case domain.DrawdownBreached(equity, rs.State, l):
		s.fail("open %s past the drawdown limit (equity %s, high-water mark %s)", in.RequestID, equity, rs.State.HighWaterMark)
	}
	// The market: entries allowed, decided before the no-entry window, sent before flatten.
	inst, _, err := ae.store.Instruments().Get(ae.ctx, in.Instrument)
	if err != nil {
		s.fail("instrument %d: %v", in.Instrument, err)
	}
	if ok, why := domain.EntriesAllowed(inst.Market); !ok {
		s.fail("open %s on %s: %s", in.RequestID, inst.Market, why)
	}
	open, closeAt, trading, err := testCalendar{}.Session(inst.Market, o.CreatedAt)
	if err != nil || !trading || o.CreatedAt.Before(open) || !o.CreatedAt.Before(closeAt.Add(-l.NoEntryBeforeClose)) {
		s.fail("open %s decided at %s outside the entry window of %s (%s-%s)", in.RequestID, o.CreatedAt, inst.Market, open, closeAt)
	}
	if !now.Before(closeAt.Add(-l.FlattenBeforeClose)) {
		s.fail("open %s sent at %s, after %s's flatten time", in.RequestID, now, inst.Market)
	}
	// Money: fresh cash minus the other reservations; budget; shorts.
	orders, err := ae.store.Orders().ListOpen(ae.ctx, ae.a.ID)
	if err != nil {
		s.fail("orders: %v", err)
	}
	others := domain.Reservations(slices.DeleteFunc(orders, func(x domain.Order) bool { return x.RequestID == in.RequestID }))
	var invested, shorts domain.Cents
	for _, p := range pf.Positions {
		invested += p.Amount
		if !p.Long {
			shorts += p.Amount
		}
	}
	budget := min(l.TradingBudget, equity)
	if in.Amount > pf.AvailableCash-others.Amount {
		s.fail("open %s of $%s with fresh cash $%s and $%s reserved", in.RequestID, in.Amount, pf.AvailableCash, others.Amount)
	}
	if sum := invested + pf.PendingOpen + others.Amount + in.Amount; sum > budget {
		s.fail("open %s: invested $%s + pending $%s + reserved $%s + $%s > min(budget, equity) $%s", in.RequestID, invested, pf.PendingOpen, others.Amount, in.Amount, budget)
	}
	if in.Direction == domain.Short {
		if l.MaxShort > domain.HardMaxShort {
			s.fail("short limit %d bp above 25 %%", l.MaxShort)
		}
		if sum := shorts + others.Short + in.Amount; sum*10_000 > budget*domain.Cents(l.MaxShort) {
			s.fail("short %s: shorts $%s + reserved $%s + $%s exceed %d bp of $%s", in.RequestID, shorts, others.Short, in.Amount, l.MaxShort, budget)
		}
	}
	if n := len(pf.Positions) + others.Count; n >= l.MaxOpenPositions {
		s.fail("open %s with %d positions and orders (cap %d)", in.RequestID, n, l.MaxOpenPositions)
	}
}

// longShape: stop < entry < target (no target is allowed).
func longShape(o domain.Order) bool {
	return o.StopLoss < o.Entry && (o.TakeProfit == 0 || o.TakeProfit > o.Entry)
}

// shortShape: target < entry < stop (no target is allowed).
func shortShape(o domain.Order) bool {
	return o.StopLoss > o.Entry && (o.TakeProfit == 0 || o.TakeProfit < o.Entry)
}

// ours returns the broker's positions cactusTrade opened (recorded as agent positions).
func (s *propScenario) ours() []domain.Position {
	var out []domain.Position
	for _, p := range s.ae.b.Positions {
		if _, err := s.ae.store.AgentPositions().Get(s.ae.ctx, s.ae.a.ID, p.ID); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// checkAfterSupervisor: after a pass with a readable portfolio, no position of ours is open
// past its market's close − flatten (or outside its session), none fails to conform, and
// none is open while the kill switch is on.
func (s *propScenario) checkAfterSupervisor() {
	s.t.Helper()
	if s.faulty {
		return
	}
	ae := s.ae
	now := ae.clock.Now()
	l := ae.svc.riskLimits(ae.ctx, ae.a.ID)
	killed, _ := ae.svc.isKilled(ae.ctx, ae.a.ID)
	for _, p := range s.ours() {
		inst, _, _ := ae.store.Instruments().Get(ae.ctx, p.Instrument)
		open, closeAt, trading, err := testCalendar{}.Session(inst.Market, now)
		switch {
		case killed:
			s.fail("position %d (%s) still open while the kill switch is on", p.ID, inst.Symbol)
		case err != nil || !trading || now.Before(open) || !now.Before(closeAt.Add(-l.FlattenBeforeClose)):
			s.fail("position %d (%s) still open at %s, past %s's close − flatten (%s)", p.ID, inst.Symbol, now, inst.Market, closeAt)
		case !p.Conforms():
			s.fail("position %d (%s) does not conform: %+v", p.ID, inst.Symbol, p)
		}
	}
}

// checkAfterTick: the broker recorded no rule broken; every position is ours (recorded) or
// the owner's, which is never closed.
func (s *propScenario) checkAfterTick() {
	s.t.Helper()
	ae := s.ae
	if len(ae.b.Violations) > 0 {
		s.fail("broker violations: %s", strings.Join(ae.b.Violations, "; "))
	}
	foreign := false
	for _, p := range ae.b.Positions {
		if p.ID == propForeignID {
			foreign = true
			continue
		}
		if _, err := ae.store.AgentPositions().Get(ae.ctx, ae.a.ID, p.ID); err != nil {
			s.fail("position %d at the broker is not recorded as ours: %v", p.ID, err)
		}
	}
	if s.foreign && !foreign {
		s.fail("the owner's own position was closed (OD1)")
	}
}
