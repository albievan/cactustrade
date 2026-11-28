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

// The Trading 212 sequence property test (PLAN R45.7): seeded random scenarios in which the
// supervisor and the agent run tick by tick against the simulated Trading 212 practice
// account, with random prices, fills, rejections, lost answers, 429s, stops firing, fills far
// from the eToro quote, LLM verdicts (garbage included), limits, kills and resets. Every order
// is checked when it reaches Trading 212, and the account after every tick.

const (
	t212PropScenarios  = 300
	t212PropTicks      = 200
	t212PropMinEntries = 300
)

type t212PropStats struct {
	entries, stopFailures, nonConforming, lost, stopHits, kills, trips, resets, partials int
}

func (p *t212PropStats) add(o t212PropStats) {
	p.entries += o.entries
	p.stopFailures += o.stopFailures
	p.nonConforming += o.nonConforming
	p.lost += o.lost
	p.stopHits += o.stopHits
	p.kills += o.kills
	p.trips += o.trips
	p.resets += o.resets
	p.partials += o.partials
}

func (p t212PropStats) unexercised() []string {
	var out []string
	for name, n := range map[string]int{
		"stop failures": p.stopFailures, "non-conforming fills": p.nonConforming, "lost answers": p.lost, "stops fired": p.stopHits,
		"kills": p.kills, "drawdown trips": p.trips, "resets": p.resets, "partial fills": p.partials,
	} {
		if n == 0 {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func TestT212SequenceProperties(t *testing.T) {
	var (
		mu    sync.Mutex
		total t212PropStats
	)
	t.Run("scenarios", func(t *testing.T) {
		for seed := range uint64(t212PropScenarios) {
			t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
				t.Parallel()
				st := runT212Scenario(t, seed, t212PropTicks)
				mu.Lock()
				total.add(st)
				mu.Unlock()
			})
		}
	})
	t.Logf("exercised: %+v", total)
	switch missing := total.unexercised(); {
	case total.entries < t212PropMinEntries:
		t.Fatalf("only %d entries exercised, want at least %d", total.entries, t212PropMinEntries)
	case len(missing) > 0:
		t.Fatalf("never exercised: %s (%+v)", strings.Join(missing, ", "), total)
	}
}

// t212Scenario is one seeded scenario.
type t212Scenario struct {
	t      *testing.T
	seed   uint64
	tick   int
	r      *rand.Rand
	ag     *t212Agent
	ids    []domain.InstrumentID
	tick2  map[domain.InstrumentID]string // eToro instrument → Trading 212 ticker
	price  map[domain.InstrumentID]domain.Micros
	cands  map[domain.InstrumentID]*domain.Candidate
	stats  t212PropStats
	limits domain.RiskLimits
}

func (s *t212Scenario) fail(format string, args ...any) {
	s.t.Helper()
	s.t.Fatalf("seed %d tick %d (%s): %s", s.seed, s.tick, s.ag.clock.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func runT212Scenario(t *testing.T, seed uint64, ticks int) t212PropStats {
	t.Helper()
	ag := newT212Agent(t)
	s := &t212Scenario{
		t: t, seed: seed, r: rand.New(rand.NewPCG(seed, seed*104_729+3)), ag: ag,
		tick2: map[domain.InstrumentID]string{1: barc}, price: map[domain.InstrumentID]domain.Micros{}, cands: map[domain.InstrumentID]*domain.Candidate{},
	}
	ag.addShares(t, 3)
	s.ids = []domain.InstrumentID{1, 100, 101, 102}
	for i, id := range s.ids[1:] {
		s.tick2[id] = "S" + string(rune('A'+i)) + "l_EQ"
	}
	for _, id := range s.ids {
		s.price[id] = domain.Micros(50_000_000 + s.r.Int64N(900_000_000))
	}
	if _, err := ag.svc.StopAgent(ag.ctx, ag.owner, ag.a.ID); err != nil {
		t.Fatal(err)
	}
	ag.sim.Cash = domain.Cents(200_000 + s.r.Int64N(5_000_000))
	// The owner's own shares (never touched): their price swings, now and then a crash that
	// takes the account past its drawdown limit.
	own := domain.Micros(100_000_000 + s.r.Int64N(2_000_000_000))
	ag.sim.Prices["OWNl_EQ"] = own
	ag.sim.Held["OWNl_EQ"] = domain.T212Position{Ticker: "OWNl_EQ", Quantity: 1_000_000_000, QuantityAvailable: 1_000_000_000, Cost: domain.Cents(int64(own) / 1_000)}
	if err := ag.svc.ShareAccount(ag.ctx, ag.owner, ag.a.ID, "admin", domain.AccessTrade); err != nil {
		t.Fatal(err)
	}
	if s.r.IntN(2) == 0 {
		r := &agentReviewer{answer: s.verdict}
		ag.svc.d.LLM = r
		pr, err := ag.svc.SaveLLMProfile(ag.ctx, ag.admin, domain.LLMProfile{Name: "Desk", LLMConfig: domain.LLMConfig{Provider: "openai", Model: "gpt"}}, sp("k"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ag.svc.SetLLMSettings(ag.ctx, ag.admin, domain.LLMSettings{Enabled: true, Active: pr.ID}); err != nil {
			t.Fatal(err)
		}
	}
	ag.svc.d.Sleep = func(ctx context.Context, d time.Duration) error {
		if d > 0 {
			ag.clock.Advance(d)
		}
		return ctx.Err()
	}
	ag.svc.evalOverride = func(id domain.InstrumentID, _ []domain.Candle) strategy.Decision {
		if c := s.cands[id]; c != nil {
			cp := *c
			return strategy.Decision{Candidate: &cp}
		}
		return strategy.Decision{Reason: "no signal (scenario)"}
	}
	ag.sim.OnPlace = s.checkPlace
	s.setLimits()
	if _, err := ag.svc.StartAgent(ag.ctx, ag.owner, ag.a.ID); err != nil {
		t.Fatal(err)
	}
	for range ticks {
		s.step()
	}
	s.stats.partials = ag.sim.Outcomes[fakes.T212PartialFill]
	s.stats.lost = ag.sim.Outcomes[fakes.T212LostApplied] + ag.sim.Outcomes[fakes.T212LostNotApplied] + ag.sim.Outcomes[fakes.T212RateLimited]
	for _, k := range ag.events(t) {
		switch k {
		case "order.stop_failed":
			s.stats.stopFailures++
		case "order.nonconforming":
			s.stats.nonConforming++
		case "position.stop_hit":
			s.stats.stopHits++
		}
	}
	return s.stats
}

// addShares adds n more LSE shares (eToro 100…, tickers SAl_EQ …) to the catalogue, the
// watchlist and the confirmed matches.
func (ag *t212Agent) addShares(t *testing.T, n int) {
	t.Helper()
	cat := slices.Clone(t212Catalogue)
	for i := range n {
		id := domain.InstrumentID(100 + i)
		sym := "S" + string(rune('A'+i))
		tk := sym + "l_EQ"
		cat = append(cat, domain.T212Instrument{Ticker: tk, ShortName: sym, Name: sym, Currency: "GBX", Type: "STOCK", ISIN: "GB" + sym, MaxOpenQuantity: 1e12})
		ag.sim.Prices[tk] = 200_000_000
		ag.m.setQuote(id, 199_900_000, 200_000_000)
		in := domain.Instrument{ID: id, Symbol: sym + ".L", Type: "Stocks", Market: domain.MarketLSE, Currency: "GBX"}
		if err := ag.store.Instruments().Upsert(ag.ctx, in, domain.Eligibility{Instrument: id, Symbol: in.Symbol}, ag.clock.Now()); err != nil {
			t.Fatal(err)
		}
		if err := ag.store.Watchlist().Add(ag.ctx, ag.a.ID, id, "test", ag.clock.Now()); err != nil {
			t.Fatal(err)
		}
		if err := ag.store.InstrumentMatches().Put(ag.ctx, domain.InstrumentMatch{
			AccountID: ag.a.ID, Instrument: id, Ticker: tk, ISIN: "GB" + sym,
			Method: domain.MatchMethodAuto, ConfirmedBy: ag.owner.UserID, ConfirmedAt: ag.clock.Now(), UpdatedAt: ag.clock.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	ag.sim.Catalogue = cat
	if err := ag.store.T212Instruments().ReplaceAll(ag.ctx, cat, ag.clock.Now()); err != nil {
		t.Fatal(err)
	}
}

func (s *t212Scenario) verdict(context.Context, ports.ReviewInput) (domain.Verdict, error) {
	switch s.r.IntN(8) {
	case 0:
		return domain.Verdict{}, errors.New("provider down")
	case 1:
		return domain.Verdict{}, context.DeadlineExceeded
	case 2:
		return domain.Verdict{Kind: domain.VerdictReject, Reason: "no"}, nil
	case 3:
		return domain.Verdict{Kind: "increase", SizeBp: 30_000}, nil
	case 4:
		return domain.Verdict{Kind: domain.VerdictReduce, SizeBp: domain.BasisPoints(s.r.IntN(40_001) - 10_000)}, nil
	}
	return domain.Verdict{Kind: domain.VerdictApprove, SizeBp: domain.BasisPoints(s.r.IntN(50_000))}, nil
}

// setLimits stores random valid limits with a budget at most the equity (agent stopped).
func (s *t212Scenario) setLimits() {
	ag := s.ag
	acct, err := ag.sim.Account(ports.WithAccount(ag.ctx, ag.a))
	if err != nil {
		s.fail("account: %v", err)
	}
	eq := acct.Equity
	l := t212Limits()
	l.TradingBudget = max(min(eq, domain.Cents(10_000+s.r.Int64N(int64(max(eq, 20_000))))), 1)
	l.MinOrderValue = min(l.TradingBudget, domain.Cents(1_00+s.r.Int64N(30_00)))
	l.DailyLoss = domain.BasisPoints(50 + s.r.IntN(450))
	l.Drawdown = domain.BasisPoints(300 + s.r.IntN(1_200))
	l.MaxPosition = []domain.BasisPoints{0, 1_000, 2_000, 5_000}[s.r.IntN(4)]
	l.MaxOpenPositions = 1 + s.r.IntN(domain.HardMaxOpenPositions)
	l.FlattenBeforeClose = time.Duration(5+s.r.IntN(16)) * time.Minute
	l.NoEntryBeforeClose = l.FlattenBeforeClose + time.Duration(s.r.IntN(31))*time.Minute
	if _, err := ag.svc.SetRiskLimits(ag.ctx, ag.owner, l); err != nil {
		s.fail("limits %+v: %v", l, err)
	}
	s.limits = l
}

func (s *t212Scenario) step() {
	s.tick++
	ag := s.ag
	now := ag.clock.Now()
	ag.clock.Advance(domain.NextDecision(now, AgentBarInterval, AgentDecisionDelay).Sub(now))
	s.control()
	s.movePrices()
	s.propose()
	// The supervisor: no scripted faults (its sells must work), now and then an unreadable
	// account outside the flatten window.
	if s.r.IntN(25) == 0 && !s.nearClose() {
		ag.sim.ReadErr = domain.ErrUnavailable
	}
	rep, err := ag.svc.Supervise(ag.ctx, ag.a.ID)
	ag.sim.ReadErr = nil
	if err != nil {
		s.fail("supervise: %v", err)
	}
	if rep.Tripped {
		s.stats.trips++
	}
	if len(rep.Errors) > 0 {
		s.t.Logf("tick %d supervisor: exits %d, errors %v", s.tick, len(rep.Exits), rep.Errors)
	}
	s.checkAfter("supervisor")
	s.checkFlat()
	// The agent: scripted outcomes, unreadable figures, an eToro outage.
	s.scriptFaults()
	tr, err := ag.svc.AgentTick(ag.ctx, ag.a.ID)
	if err != nil {
		s.fail("agent tick: %v", err)
	}
	if tr.Killed {
		s.stats.trips++
	}
	ag.sim.Script, ag.sim.ReadErr, ag.m.Err = nil, nil, nil
	s.checkAfter("agent")
}

// nearClose reports whether the LSE closes within an hour.
func (s *t212Scenario) nearClose() bool {
	_, closeAt, trading, err := fixedCalendar{}.Session(domain.MarketLSE, s.ag.clock.Now())
	return err != nil || !trading || closeAt.Sub(s.ag.clock.Now()) < time.Hour
}

func (s *t212Scenario) control() {
	ag := s.ag
	st := ag.svc.agentState(ag.ctx, ag.a.ID)
	killed, err := ag.svc.isKilled(ag.ctx, ag.a.ID)
	if err != nil {
		s.fail("kill switch: %v", err)
	}
	start := func() {
		if _, err := ag.svc.StartAgent(ag.ctx, ag.owner, ag.a.ID); err != nil && !errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrInvalid) && !errors.Is(err, domain.ErrUnavailable) {
			s.fail("start: %v", err)
		}
	}
	switch {
	case killed:
		if s.r.IntN(10) == 0 {
			if _, err := ag.svc.ResetKill(ag.ctx, ag.admin, ag.a.ID, ResetPhrase(ag.a.Name)); err != nil {
				s.fail("reset: %v", err)
			}
			s.stats.resets++
			start()
		}
	case s.r.IntN(150) == 0:
		if _, err := ag.svc.Kill(ag.ctx, ag.owner, ag.a.ID, "scenario"); err != nil {
			s.fail("kill: %v", err)
		}
		s.stats.kills++
	case st == domain.AgentRunning && s.r.IntN(80) == 0:
		if _, err := ag.svc.StopAgent(ag.ctx, ag.owner, ag.a.ID); err != nil {
			s.fail("stop: %v", err)
		}
		s.setLimits()
		start()
	case st != domain.AgentRunning && s.r.IntN(4) == 0:
		start()
	}
}

// movePrices walks the eToro prices; Trading 212 fills at the eToro ask, now and then 3 %
// away (a wrong instrument); stops fire when the price falls to them.
func (s *t212Scenario) movePrices() {
	ag := s.ag
	for _, id := range s.ids {
		bp := int64(s.r.IntN(81) - 40)
		if s.r.IntN(60) == 0 {
			bp = -int64(300 + s.r.IntN(700))
		}
		p := max(s.price[id]+s.price[id]*domain.Micros(bp)/10_000, 1_000_000)
		s.price[id] = p
		ag.m.setQuote(id, p-p/1000, p)
		tk := s.tick2[id]
		fill := p
		if s.r.IntN(40) == 0 {
			fill = p + p*3/100
		}
		ag.sim.Prices[tk] = fill
		ag.sim.TriggerStop(tk, p)
	}
	if own := ag.sim.Prices["OWNl_EQ"]; s.r.IntN(120) == 0 {
		ag.sim.Prices["OWNl_EQ"] = own / 2
	} else {
		ag.sim.Prices["OWNl_EQ"] = max(own+own*domain.Micros(s.r.IntN(41)-20)/10_000, 1_000_000)
	}
}

// propose sets this tick's candidates (about half the shares).
func (s *t212Scenario) propose() {
	for _, id := range s.ids {
		if s.r.IntN(2) == 0 {
			delete(s.cands, id)
			continue
		}
		p := s.price[id]
		s.cands[id] = &domain.Candidate{
			Instrument: id, Direction: domain.Long, Playbook: "trend_breakout", Reason: "scenario", Entry: p,
			StopLossRate: p - p*domain.Micros(100+s.r.IntN(300))/10_000, TakeProfitRate: p + p*domain.Micros(200+s.r.IntN(500))/10_000,
			SuggestedSize: domain.Cents(5_00 + s.r.Int64N(3_000_00)),
		}
	}
}

func (s *t212Scenario) scriptFaults() {
	ag := s.ag
	outcomes := []fakes.T212Outcome{
		fakes.T212Fill, fakes.T212Fill, fakes.T212Fill, fakes.T212Pending, fakes.T212Reject,
		fakes.T212PartialFill, fakes.T212Refused, fakes.T212LostApplied, fakes.T212LostNotApplied, fakes.T212RateLimited,
	}
	if s.r.IntN(3) == 0 {
		for range 1 + s.r.IntN(3) {
			ag.sim.Script = append(ag.sim.Script, outcomes[s.r.IntN(len(outcomes))])
		}
	}
	switch s.r.IntN(30) {
	case 0:
		ag.sim.ReadErr = domain.ErrUnavailable
	case 1:
		ag.m.Err = domain.ErrUnavailable
	}
	ag.m.QuoteAge = 0
	if s.r.IntN(30) == 0 {
		ag.m.QuoteAge = time.Hour
	}
}

// checkPlace runs at Trading 212 for every Place: the row was persisted as sent first, a
// market order only in session, and a buy only when everything allows an entry.
func (s *t212Scenario) checkPlace(ctx context.Context, in domain.T212Intent) error {
	ag := s.ag
	if err := fakes.RequireT212OrderRow(ag.store.Orders())(ctx, in); err != nil {
		return err
	}
	now := ag.clock.Now()
	if in.Type == domain.T212Market {
		if open, closeAt, trading, err := (fixedCalendar{}).Session(domain.MarketLSE, now); err != nil || !trading || now.Before(open) || !now.Before(closeAt) {
			return fmt.Errorf("a market order outside the session at %s", now)
		}
	}
	if in.Side != domain.T212Buy {
		return nil
	}
	s.stats.entries++
	if killed, err := ag.svc.isKilled(ag.ctx, ag.a.ID); err != nil || killed {
		return errors.New("a buy while killed")
	}
	if rs, err := ag.store.RiskStates().Get(ag.ctx, ag.a.ID); err == nil && rs.State.DayStopped && rs.State.Day == domain.RiskDay(now) {
		return errors.New("a buy after the daily latch")
	}
	open, err := ag.store.Orders().ListOpen(ag.ctx, ag.a.ID)
	if err != nil {
		return err
	}
	var reserved domain.Cents
	for _, o := range open {
		if o.State == domain.OrderUnknown {
			return fmt.Errorf("a buy while order %s is unknown", o.RequestID)
		}
		if o.Kind == domain.OrderOpen {
			reserved += o.Amount
		}
	}
	ours, err := ag.store.AgentPositions().ListOpen(ag.ctx, ag.a.ID)
	if err != nil {
		return err
	}
	stops := map[string]bool{}
	active, err := ag.sim.OpenOrders(ports.WithAccount(ag.ctx, ag.a))
	if err != nil {
		return err
	}
	for _, o := range active {
		if o.Type == domain.T212StopOrder {
			stops[o.Ticker] = true
		}
	}
	acct, err := ag.sim.Account(ports.WithAccount(ag.ctx, ag.a))
	if err != nil {
		return err
	}
	var invested domain.Cents
	for _, ap := range ours {
		if !stops[ap.Ticker] {
			return fmt.Errorf("a buy while %s has no stop", ap.Ticker)
		}
		invested += ag.sim.Held[ap.Ticker].Cost
	}
	if reserved > ag.sim.Cash {
		return fmt.Errorf("reserved %s (this buy included) above the cash %s", reserved, ag.sim.Cash)
	}
	l := ag.svc.riskLimitsFor(ag.ctx, ag.a)
	if invested+reserved > min(l.TradingBudget, acct.Equity) {
		return fmt.Errorf("invested %s + reserved %s above min(budget %s, equity %s)", invested, reserved, l.TradingBudget, acct.Equity)
	}
	if in.Quantity%1_000_000 != 0 {
		return errors.New("a fraction while locked")
	}
	return nil
}

// checkAfter checks the account after a run: no violation (persisted before Place, sell ≤
// held, never real, never a fraction), each order Placed at most once, only the three
// shapes, nothing held short.
func (s *t212Scenario) checkAfter(what string) {
	ag := s.ag
	if len(ag.sim.Violations) > 0 {
		s.fail("after the %s: violations %v", what, ag.sim.Violations)
	}
	seen := map[string]bool{}
	for _, in := range ag.sim.Intents {
		if seen[in.RequestID] {
			s.fail("order %s Placed twice", in.RequestID)
		}
		seen[in.RequestID] = true
		switch {
		case in.Side == domain.T212Buy && in.Type == domain.T212Market,
			in.Side == domain.T212Sell && in.Type == domain.T212StopOrder,
			in.Side == domain.T212Sell && in.Type == domain.T212Market:
		default:
			s.fail("intent shape %+v", in)
		}
	}
	for tk, p := range ag.sim.Held {
		if p.Quantity < 0 {
			s.fail("short %s", tk)
		}
	}
}

// checkFlat: once the LSE is closed (after the supervisor ran into it), nothing of ours is
// held.
func (s *t212Scenario) checkFlat() {
	ag := s.ag
	now := ag.clock.Now()
	open, closeAt, trading, err := fixedCalendar{}.Session(domain.MarketLSE, now)
	if err == nil && trading && !now.Before(open) && now.Before(closeAt) {
		return
	}
	ours, err := ag.store.AgentPositions().ListOpen(ag.ctx, ag.a.ID)
	if err != nil {
		s.fail("positions: %v", err)
	}
	for _, ap := range ours {
		if _, held := ag.sim.Held[ap.Ticker]; held {
			s.fail("%s held after the close (opened %s)", ap.Ticker, ap.OpenedAt.Format(time.RFC3339))
		}
	}
}
