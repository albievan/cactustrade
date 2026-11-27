package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// The sequence property test of PLAN 4.12, extended by 7.22 to a real-money account: seeded
// random scenarios in which an armed real account's supervisor and agent run tick by tick
// against the simulated broker while random events take a key away — a disarm, a kill, the
// deployment opt-in switched off, the gate expiring, a restore — and give it back later (a
// re-arm, a kill reset, the opt-in on again). The demo invariants are TestAgentSequence-
// Properties' own and run unchanged.
//
// Invariants: every real Open carries an entry permit valid for the account when it reaches
// the broker; no Open is made while a key is missing (after a disarm, kill, opt-in off,
// expiry or restore, until it is given back); no supervisor pass or kill ever opens; and
// exits continue — after a kill and from the flatten time on, every position of ours has a
// close sent, whatever the gate.

const (
	realPropScenarios = 40
	realPropTicks     = 90
)

// realPropStats counts what the scenarios exercised.
type realPropStats struct {
	entries, disarms, kills, optOffs, expiries, restores, recoveries, flattened int
}

func (p *realPropStats) add(o realPropStats) {
	p.entries += o.entries
	p.disarms += o.disarms
	p.kills += o.kills
	p.optOffs += o.optOffs
	p.expiries += o.expiries
	p.restores += o.restores
	p.recoveries += o.recoveries
	p.flattened += o.flattened
}

func TestAgentSequencePropertiesReal(t *testing.T) {
	var (
		mu    sync.Mutex
		total realPropStats
	)
	t.Run("scenarios", func(t *testing.T) {
		for seed := range uint64(realPropScenarios) {
			t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
				t.Parallel()
				st := runRealPropScenario(t, seed)
				mu.Lock()
				total.add(st)
				mu.Unlock()
			})
		}
	})
	t.Logf("exercised: %+v", total)
	for name, n := range map[string]int{
		"entries": total.entries, "disarms": total.disarms, "kills": total.kills, "opt-in off": total.optOffs,
		"expiries": total.expiries, "restores": total.restores, "recoveries": total.recoveries, "flattens": total.flattened,
	} {
		if n == 0 {
			t.Errorf("never exercised: %s (%+v)", name, total)
		}
	}
}

// realPropScenario is one seeded scenario on a real account.
type realPropScenario struct {
	t    *testing.T
	seed uint64
	tick int
	r    *rand.Rand
	ae   *agentEnv
	g    *gateEnv
	// The model: entries are allowed only while the gate is live (armed, not expired, not
	// older than a restore), the opt-in is on, the kill switch is off and the agent runs.
	live, optIn, killed bool
	// noOpens is set while a call that must never open runs (a supervisor pass, a kill).
	noOpens bool
	stats   realPropStats
}

func (s *realPropScenario) fail(format string, args ...any) {
	s.t.Helper()
	s.t.Fatalf("seed %d tick %d (%s): %s", s.seed, s.tick, s.ae.clock.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// allowed is the model's say on an entry now.
func (s *realPropScenario) allowed() bool {
	return s.live && s.optIn && !s.killed && s.ae.svc.agentState(s.ae.ctx, s.g.real.ID) == domain.AgentRunning
}

// realPropBroker checks every real Open against the permit it carries and the model.
type realPropBroker struct {
	*fakes.SimBroker
	s *realPropScenario
}

func (b *realPropBroker) Open(ctx context.Context, in domain.OrderIntent) (domain.OrderStatus, error) {
	s := b.s
	a, _ := ports.AccountFrom(ctx)
	p, _ := ports.PermitFrom(ctx)
	switch {
	case a.ID != s.g.real.ID:
		s.fail("an open for %s", a.ID)
	case !p.AllowsEntry(a.ID, s.ae.clock.Now()):
		s.fail("a real open without a valid entry permit: %+v", p)
	case s.noOpens:
		s.fail("an open from an exit path")
	case !s.allowed():
		s.fail("an open while a key is missing (live %v, opt-in %v, killed %v)", s.live, s.optIn, s.killed)
	}
	s.stats.entries++
	return b.SimBroker.Open(ctx, in)
}

func runRealPropScenario(t *testing.T, seed uint64) realPropStats {
	t.Helper()
	ae, g, _ := realEnv(t, false, true)
	s := &realPropScenario{t: t, seed: seed, r: rand.New(rand.NewPCG(seed, seed*104_729+19)), ae: ae, g: g, optIn: true}
	for _, in := range []struct {
		id  domain.InstrumentID
		sym string
	}{{104, "MSFT"}, {105, "NVDA"}} {
		inst := domain.Instrument{ID: in.id, Symbol: in.sym, Type: "Stocks", Market: domain.MarketNYSE}
		if err := ae.store.Instruments().Upsert(ae.ctx, inst, domain.Eligibility{Instrument: in.id}, ae.clock.Now()); err != nil {
			t.Fatal(err)
		}
		if err := ae.store.Watchlist().Add(ae.ctx, g.real.ID, in.id, "test", ae.clock.Now()); err != nil {
			t.Fatal(err)
		}
		ae.m.setQuote(in.id, 99_990_000, 100_000_000)
	}
	l := domain.DefaultRealRiskLimits
	l.TradingBudget = 10_000_00
	l.MaxShort = []domain.BasisPoints{0, 1_000, domain.HardMaxShort}[s.r.IntN(3)]
	g.setLimits(t, l)
	ae.b.OnOpen = fakes.AllHooks(fakes.RequireOrderRow(ae.store.Orders()), fakes.RequireDecision(ae.store.Decisions(), g.real.ID))
	ae.svc.d.Broker = &realPropBroker{SimBroker: ae.b, s: s}
	g.arm(t)
	s.live = true
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, g.real.ID); err != nil {
		t.Fatal(err)
	}
	for range realPropTicks {
		s.step()
	}
	noViolations(t, ae.b)
	return s.stats
}

// step is one tick: the clock moves to the next bar close + 5 s, an event may happen, the
// candidates change, then the supervisor and the agent run, and the invariants are checked.
func (s *realPropScenario) step() {
	s.tick++
	ae := s.ae
	now := ae.clock.Now()
	ae.clock.Advance(domain.NextDecision(now, AgentBarInterval, AgentDecisionDelay).Sub(now))
	s.event()
	s.propose()
	s.noOpens = true
	rep, err := ae.svc.Supervise(ae.ctx, s.g.real.ID)
	s.noOpens = false
	if err != nil {
		s.fail("supervise: %v", err)
	}
	if rep.Tripped {
		s.killed, s.live = true, false
	}
	s.checkExits(rep)
	if _, err := ae.svc.AgentTick(ae.ctx, s.g.real.ID); err != nil {
		s.fail("agent tick: %v", err)
	}
}

// propose gives each watched instrument a random candidate (or none).
func (s *realPropScenario) propose() {
	cands := map[domain.InstrumentID]*domain.Candidate{}
	for _, id := range []domain.InstrumentID{instNYSE, 104, 105} {
		switch s.r.IntN(4) {
		case 0, 1:
			cands[id] = longCand(id)
		case 2:
			cands[id] = shortCand(id)
		}
	}
	s.ae.setCand(func(id domain.InstrumentID) *domain.Candidate { return cands[id] })
}

// event takes a key away or gives the missing ones back, now and then.
func (s *realPropScenario) event() {
	ae, real := s.ae, s.g.real
	ctx := ae.ctx
	switch s.r.IntN(14) {
	case 0: // a disarm by a person
		if _, err := ae.svc.DisarmReal(ctx, ae.admin, real.ID, "scenario"); err != nil {
			s.fail("disarm: %v", err)
		}
		s.live = false
		s.stats.disarms++
	case 1: // a kill: it disarms too (OD24) and flattens
		s.noOpens = true
		rep, err := ae.svc.Kill(ctx, ae.admin, real.ID, "scenario")
		s.noOpens = false
		if err != nil {
			s.fail("kill: %v", err)
		}
		if len(rep.Errors) > 0 {
			s.fail("kill errors: %v", rep.Errors)
		}
		s.killed, s.live = true, false
		s.stats.kills++
		s.requireClosesSent("after the kill")
	case 2: // the deployment opt-in off (the second key, OD28)
		ae.svc.cfg.RealTrading, s.optIn = false, false
		s.stats.optOffs++
	case 3: // the gate expires
		gate, err := ae.svc.storedGate(ctx, real.ID)
		if err != nil {
			s.fail("gate: %v", err)
		}
		if gate.State != domain.GateArmed || !gate.ArmedAt.Before(ae.clock.Now()) {
			return
		}
		gate.ExpiresAt = gate.ArmedAt.Add(time.Nanosecond)
		if _, err := ae.store.RealGates().Put(ctx, gate); err != nil {
			s.fail("expire: %v", err)
		}
		s.live = false
		s.stats.expiries++
	case 4: // a restore of the database
		if err := ae.store.Settings().Set(ctx, domain.SettingRestoreAt, ae.clock.Now().UTC().Format(time.RFC3339Nano), ae.clock.Now()); err != nil {
			s.fail("restore: %v", err)
		}
		s.live = false
		s.stats.restores++
	case 5, 6, 7: // everything given back: kill reset, opt-in on, re-armed, started
		s.recover()
	}
}

// recover gives every missing key back and starts the agent.
func (s *realPropScenario) recover() {
	ae, real := s.ae, s.g.real
	ctx := ae.ctx
	if s.live && s.optIn && !s.killed {
		return
	}
	if s.killed {
		if _, err := ae.svc.ResetKill(ctx, ae.admin, real.ID, ResetPhrase(real.Name)); err != nil {
			s.fail("reset: %v", err)
		}
		s.killed = false
	}
	ae.svc.cfg.RealTrading, s.optIn = true, true
	if !s.live {
		if st := ae.svc.agentState(ctx, real.ID); st != domain.AgentStopped {
			if _, err := ae.svc.StopAgent(ctx, ae.admin, real.ID); err != nil {
				s.fail("stop: %v", err)
			}
		}
		locks := ae.svc.rt.agent(real.ID)
		locks.ctl.Lock()
		_, err := ae.svc.armLocked(ctx, ae.admin, real, ae.a.ID, domain.GateEvidence{Days: []domain.RiskDate{"2026-11-20"}, Count: 1, First: "2026-11-20", Last: "2026-11-20"})
		locks.ctl.Unlock()
		if err != nil {
			s.fail("re-arm: %v", err)
		}
		if live, _ := ae.svc.gateLive(ctx, mustGate(s)); !live {
			return // armed at the restore's own time: in force from the next tick
		}
		s.live = true
	}
	if ae.svc.agentState(ctx, real.ID) != domain.AgentRunning {
		// Not reconciled yet after a kill: the agent stays stopped (the model follows its state).
		if _, err := ae.svc.StartAgent(ctx, ae.admin, real.ID); err != nil && !errors.Is(err, domain.ErrConflict) {
			s.fail("start after recovery: %v", err)
		}
	}
	s.stats.recoveries++
}

func mustGate(s *realPropScenario) domain.RealGate {
	g, err := s.ae.svc.storedGate(s.ae.ctx, s.g.real.ID)
	if err != nil {
		s.fail("gate: %v", err)
	}
	return g
}

// checkExits: from NYSE's flatten time on, every position of ours has a close sent (the pass
// rep sent them, whatever the gate).
func (s *realPropScenario) checkExits(rep SupervisorReport) {
	now := s.ae.clock.Now()
	_, closeAt, _, _ := testCalendar{}.Session(domain.MarketNYSE, now)
	if now.Before(closeAt.Add(-domain.DefaultRealRiskLimits.FlattenBeforeClose)) {
		return
	}
	s.requireClosesSent("at the flatten time")
	if rep.Closing > 0 {
		s.stats.flattened++
	}
}

// requireClosesSent fails when a position of ours on the real account has no close sent.
func (s *realPropScenario) requireClosesSent(when string) {
	ps, err := s.ae.store.AgentPositions().ListOpen(s.ae.ctx, s.g.real.ID)
	if err != nil {
		s.fail("positions: %v", err)
	}
	if i := slices.IndexFunc(ps, func(p domain.AgentPosition) bool { return p.CloseRequestID == "" }); i >= 0 {
		s.fail("%s: position %d of ours has no close sent", when, ps[i].PositionID)
	}
}
