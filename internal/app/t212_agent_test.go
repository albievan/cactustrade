package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/strategy"
)

// t212Agent is a running agent on the Trading 212 practice account of t212X: BARC.L priced by
// eToro at 199.9/200 p, a £5 000 budget, a £10 minimum order and a strategy that proposes a
// long for every share.
type t212Agent struct {
	*t212X
	m    *agentMarket
	mu   sync.Mutex
	cand func(domain.InstrumentID) *domain.Candidate
}

func barcCand(id domain.InstrumentID) *domain.Candidate {
	return &domain.Candidate{
		Instrument: id, Direction: domain.Long, Playbook: "trend_breakout", Reason: "test",
		Entry: 200_000_000, StopLossRate: 194_000_000, TakeProfitRate: 212_000_000, SuggestedSize: 2_000_00,
	}
}

func newT212Agent(t *testing.T) *t212Agent {
	t.Helper()
	x := newT212X(t)
	ag := &t212Agent{t212X: x, m: &agentMarket{clock: x.clock}, cand: barcCand}
	ag.m.setQuote(1, 199_900_000, 200_000_000)
	x.svc.d.Market = ag.m
	x.svc.evalOverride = func(id domain.InstrumentID, _ []domain.Candle) strategy.Decision {
		ag.mu.Lock()
		f := ag.cand
		ag.mu.Unlock()
		if c := f(id); c != nil {
			return strategy.Decision{Candidate: c}
		}
		return strategy.Decision{Reason: "no signal (test)"}
	}
	ag.limits(t, func(*domain.RiskLimits) {})
	if r, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil || !r.OK {
		t.Fatalf("reconcile = %+v %v", r, err)
	}
	if _, err := x.svc.StartAgent(x.ctx, x.owner, x.a.ID); err != nil {
		t.Fatal(err)
	}
	return ag
}

func t212Limits() domain.RiskLimits {
	l := domain.DefaultRiskLimits
	l.TradingBudget, l.MaxShort, l.MinOrderValue = 5_000_00, 0, 10_00
	return l
}

// limits stores t212Limits changed by edit (the agent must be stopped).
func (ag *t212Agent) limits(t *testing.T, edit func(*domain.RiskLimits)) {
	t.Helper()
	l := t212Limits()
	edit(&l)
	if _, err := ag.svc.SetRiskLimits(ag.ctx, ag.owner, l); err != nil {
		t.Fatal(err)
	}
}

func (ag *t212Agent) tick(t *testing.T) AgentTrace {
	t.Helper()
	tr, err := ag.svc.AgentTick(ag.ctx, ag.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestT212AgentTickEnters(t *testing.T) {
	ag := newT212Agent(t)
	tr := ag.tick(t)
	if tr.Skipped != "" || tr.Entries != 1 || len(tr.Instruments) != 1 {
		t.Fatalf("trace %+v", tr)
	}
	for _, c := range tr.Instruments[0].Checks {
		if !c.Pass {
			t.Fatalf("failed step %+v", c)
		}
	}
	buys := intentsOf(ag.sim, domain.T212Buy, domain.T212Market)
	if len(buys) != 1 || buys[0].Ticker != barc || buys[0].Quantity%1_000_000 != 0 || buys[0].Quantity <= 0 {
		t.Fatalf("buys %+v", buys)
	}
	o := ag.orders(t, domain.OrderOpen)[0]
	if o.Amount != domain.T212Reserve(o.Quantity, 200_000_000) || o.Amount > 2_000_00 || o.State != domain.OrderFilled {
		t.Fatalf("order %+v", o)
	}
	if len(ag.activeStops(t)) != 1 {
		t.Fatal("no stop")
	}
	ds, _ := ag.store.Decisions().List(ag.ctx, ag.a.ID, time.Time{}, "", 10)
	if len(ds) != 1 || ds[0].Outcome != domain.DecisionOrdered || ds[0].OrderRequestID != o.RequestID {
		t.Fatalf("decisions %+v", ds)
	}
	// Held now: no second entry in the same share.
	ag.clock.Advance(AgentBarInterval)
	ag.m.setQuote(1, 199_900_000, 200_000_000)
	if tr := ag.tick(t); tr.Entries != 0 {
		t.Fatalf("entered twice: %+v", tr)
	}
	ag.sound(t)
}

// TestT212AgentGate: every refusal of the gate means zero Places.
func TestT212AgentGate(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, ag *t212Agent)
		want  string
	}{
		{"no price source", func(t *testing.T, ag *t212Agent) {
			if err := ag.store.Settings().Set(ag.ctx, priceSourceKey(ag.a.ID), "", ag.clock.Now()); err != nil {
				t.Fatal(err)
			}
		}, "price source"},
		{"stale catalogue", func(t *testing.T, ag *t212Agent) { ag.clock.Advance(25 * time.Hour) }, "instrument list"},
		{"not reconciled", func(t *testing.T, ag *t212Agent) { ag.svc.unreconcile(ag.a.ID, "test") }, "not reconciled"},
		{"killed", func(t *testing.T, ag *t212Agent) { ag.svc.rt.latchKill(ag.a.ID) }, "kill switch"},
		{"an unknown order", func(t *testing.T, ag *t212Agent) {
			o, _ := domain.NewOrder(domain.Order{
				RequestID: "u1", AccountID: ag.a.ID, Mode: domain.ModeDemo, Kind: domain.OrderOpen, Platform: domain.PlatformTrading212,
				Ticker: barc, Quantity: 1_000_000, Direction: domain.Long, Instrument: 1, Amount: 300, Entry: 200_000_000, StopLoss: 190_000_000,
			}, ag.clock.Now())
			if err := ag.store.Orders().Create(ag.ctx, o); err != nil {
				t.Fatal(err)
			}
			s, _ := o.Transition(domain.OrderSent, ag.clock.Now(), "")
			u, _ := s.Transition(domain.OrderUnknown, ag.clock.Now(), "lost")
			_ = ag.store.Orders().Update(ag.ctx, o, s)
			_ = ag.store.Orders().Update(ag.ctx, s, u)
		}, "unknown outcome"},
		{"a position without its stop", func(t *testing.T, ag *t212Agent) {
			if err := ag.store.AgentPositions().Add(ag.ctx, domain.AgentPosition{AccountID: ag.a.ID, PositionID: 1234, Ticker: "VODl_EQ", Quantity: 1_000_000, Instrument: 4, Direction: domain.Long, OpenedAt: ag.clock.Now()}); err != nil {
				t.Fatal(err)
			}
		}, "no confirmed stop"},
		{"the agent is paused", func(t *testing.T, ag *t212Agent) {
			if _, err := ag.svc.PauseAgent(ag.ctx, ag.owner, ag.a.ID); err != nil {
				t.Fatal(err)
			}
		}, "paused"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ag := newT212Agent(t)
			tc.setup(t, ag)
			tr := ag.tick(t)
			if !strings.Contains(tr.Skipped, tc.want) || len(ag.sim.Intents) != 0 {
				t.Fatalf("skipped %q, intents %v", tr.Skipped, ag.sim.Intents)
			}
		})
	}
}

// TestT212AgentRefusals: steps that refuse an entry (no Place).
func TestT212AgentRefusals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, ag *t212Agent)
		step  TraceStep
	}{
		{"eToro candles fail (price source down)", func(t *testing.T, ag *t212Agent) { ag.m.Err = domain.ErrUnavailable }, StepCandles},
		{"the share is held by the owner", func(t *testing.T, ag *t212Agent) {
			ag.sim.Held[barc] = domain.T212Position{Ticker: barc, Quantity: 2_000_000, QuantityAvailable: 2_000_000, Cost: 400}
		}, StepSession},
		{"a short candidate", func(t *testing.T, ag *t212Agent) {
			ag.cand = shortCand
		}, StepSignal},
		{"costs eat the gain", func(t *testing.T, ag *t212Agent) {
			ag.cand = func(id domain.InstrumentID) *domain.Candidate {
				c := barcCand(id)
				c.TakeProfitRate = 200_200_000
				return c
			}
		}, StepCosts},
		{"the reviewer rejects", func(t *testing.T, ag *t212Agent) {
			r := &agentReviewer{answer: verdict(domain.Verdict{}, errors.New("LLM down"))}
			ag.svc.d.LLM = r
			pr, err := ag.svc.SaveLLMProfile(ag.ctx, ag.admin, domain.LLMProfile{Name: "Desk", LLMConfig: domain.LLMConfig{Provider: "openai", Model: "gpt"}}, sp("k"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ag.svc.SetLLMSettings(ag.ctx, ag.admin, domain.LLMSettings{Enabled: true, Active: pr.ID}); err != nil {
				t.Fatal(err)
			}
		}, StepReview},
		{"the owner buys the share during the review (review c)", func(t *testing.T, ag *t212Agent) {
			r := &agentReviewer{answer: func(context.Context, ports.ReviewInput) (domain.Verdict, error) {
				ag.sim.Held[barc] = domain.T212Position{Ticker: barc, Quantity: 2_000_000, QuantityAvailable: 2_000_000, Cost: 400}
				return domain.Verdict{Kind: domain.VerdictApprove, SizeBp: 10_000}, nil
			}}
			ag.svc.d.LLM = r
			pr, err := ag.svc.SaveLLMProfile(ag.ctx, ag.admin, domain.LLMProfile{Name: "Desk", LLMConfig: domain.LLMConfig{Provider: "openai", Model: "gpt"}}, sp("k"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ag.svc.SetLLMSettings(ag.ctx, ag.admin, domain.LLMSettings{Enabled: true, Active: pr.ID}); err != nil {
				t.Fatal(err)
			}
		}, StepPrices},
		{"a stale eToro quote", func(t *testing.T, ag *t212Agent) { ag.m.QuoteAge = time.Hour }, StepCosts},
		{"the size is under the minimum order value", func(t *testing.T, ag *t212Agent) {
			ag.cand = func(id domain.InstrumentID) *domain.Candidate {
				c := barcCand(id)
				c.SuggestedSize = 5_00
				return c
			}
		}, StepRisk},
		{"blocked instrument", func(t *testing.T, ag *t212Agent) { ag.blockT212Now(t) }, StepRisk},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ag := newT212Agent(t)
			tc.setup(t, ag)
			tr := ag.tick(t)
			if tr.Entries != 0 || len(ag.sim.Intents) != 0 || len(tr.Instruments) != 1 {
				t.Fatalf("trace %+v intents %v", tr, ag.sim.Intents)
			}
			if c := lastCheck(tr.Instruments[0]); c.Step != tc.step || c.Pass {
				t.Fatalf("last check %+v, want a failed %s", c, tc.step)
			}
		})
	}
}

func (ag *t212Agent) blockT212Now(t *testing.T) {
	t.Helper()
	if err := ag.store.InstrumentBlocks().Block(ag.ctx, domain.InstrumentBlock{AccountID: ag.a.ID, Instrument: 1, Reason: "test", At: ag.clock.Now()}); err != nil {
		t.Fatal(err)
	}
}

// TestT212RealAccountNeverTicks: a real Trading 212 account is refused everywhere: no tick, no
// start, no supervisor pass, no reconcile; zero broker calls.
func TestT212RealAccountNeverTicks(t *testing.T) {
	ag := newT212Agent(t)
	rl := ag.t212Rl.Account
	ag.sim.Reads = 0
	tr, err := ag.svc.AgentTick(ag.ctx, rl.ID)
	if err != nil || tr.Skipped == "" || tr.Entries != 0 {
		t.Fatalf("tick = %+v %v", tr, err)
	}
	if _, err := ag.svc.StartAgent(ag.ctx, ag.owner, rl.ID); !errors.Is(err, domain.ErrViewOnly) {
		t.Fatalf("start: %v", err)
	}
	if rep, err := ag.svc.Supervise(ag.ctx, rl.ID); err != nil || rep.Skipped == "" {
		t.Fatalf("supervise = %+v %v", rep, err)
	}
	if _, err := ag.svc.Reconcile(ag.ctx, rl.ID); !errors.Is(err, domain.ErrViewOnly) {
		t.Fatalf("reconcile: %v", err)
	}
	if len(ag.sim.Intents) != 0 || ag.sim.Reads != 0 || len(ag.sim.Violations) != 0 {
		t.Fatalf("broker called: intents %v reads %d violations %v", ag.sim.Intents, ag.sim.Reads, ag.sim.Violations)
	}
}

// TestT212AgentTickBeforeReconcile: an agent tick before the account was reconciled sends
// nothing.
func TestT212AgentTickBeforeReconcile(t *testing.T) {
	ag := newT212Agent(t)
	ag.svc.rt.mu.Lock()
	delete(ag.svc.rt.reconciled, ag.a.ID)
	ag.svc.rt.mu.Unlock()
	if tr := ag.tick(t); tr.Entries != 0 || len(ag.sim.Intents) != 0 || !strings.Contains(tr.Skipped, "reconciled") {
		t.Fatalf("trace %+v", tr)
	}
}

// enterOne makes one entry and returns its position.
func (ag *t212Agent) enterOne(t *testing.T) domain.AgentPosition {
	t.Helper()
	if tr := ag.tick(t); tr.Entries != 1 {
		t.Fatalf("no entry: %+v", tr)
	}
	ours, err := ag.store.AgentPositions().ListOpen(ag.ctx, ag.a.ID)
	if err != nil || len(ours) != 1 {
		t.Fatalf("positions %+v %v", ours, err)
	}
	return ours[0]
}

func TestT212Supervisor(t *testing.T) {
	t.Run("flatten before the close: stop cancelled, sold", func(t *testing.T) {
		ag := newT212Agent(t)
		ap := ag.enterOne(t)
		ag.clock.T = time.Date(2026, 11, 23, 16, 20, 0, 0, time.UTC) // LSE closes 16:30
		rep, err := ag.svc.Supervise(ag.ctx, ag.a.ID)
		if err != nil || rep.Closing != 1 || len(rep.Exits) != 1 || rep.Exits[0].Reason != domain.ExitFlatten {
			t.Fatalf("rep %+v %v", rep, err)
		}
		if ag.position(t, ap.PositionID).Open() || len(ag.sim.Held) != 0 || len(ag.activeStops(t)) != 0 {
			t.Fatal("still held")
		}
		ag.sound(t)
	})
	t.Run("stop fired: settled from the history", func(t *testing.T) {
		ag := newT212Agent(t)
		ap := ag.enterOne(t)
		ag.sim.TriggerStop(barc, 190_000_000)
		ag.clock.Advance(time.Minute)
		if _, err := ag.svc.Supervise(ag.ctx, ag.a.ID); err != nil {
			t.Fatal(err)
		}
		if ag.position(t, ap.PositionID).Open() || !slices.Contains(ag.events(t), "position.stop_hit") {
			t.Fatalf("not settled: %v", ag.events(t))
		}
		if len(intentsOf(ag.sim, domain.T212Sell, domain.T212Market)) != 0 {
			t.Fatal("sold after the stop fired")
		}
		ag.sound(t)
	})
	t.Run("stop gone (cancelled by hand): sold, blocked", func(t *testing.T) {
		ag := newT212Agent(t)
		ap := ag.enterOne(t)
		st := ag.activeStops(t)[0]
		if err := ag.sim.Cancel(ports.WithAccount(ag.ctx, ag.a), st.ID); err != nil {
			t.Fatal(err)
		}
		ag.clock.Advance(time.Minute)
		if _, err := ag.svc.Supervise(ag.ctx, ag.a.ID); err != nil {
			t.Fatal(err)
		}
		if ag.position(t, ap.PositionID).Open() || len(ag.sim.Held) != 0 || !ag.isBlocked(t) {
			t.Fatal("a position without a stop was kept")
		}
		ag.sound(t)
	})
	t.Run("exits work when eToro, the price source, the LLM and the catalogue fail", func(t *testing.T) {
		ag := newT212Agent(t)
		ap := ag.enterOne(t)
		if err := ag.store.T212Instruments().ReplaceAll(ag.ctx, t212Catalogue, ag.clock.Now().Add(-72*time.Hour)); err != nil {
			t.Fatal(err)
		}
		ag.m.Err = domain.ErrUnavailable
		ag.svc.d.Broker, ag.svc.d.LLM = nil, nil
		if err := ag.store.Settings().Set(ag.ctx, priceSourceKey(ag.a.ID), "", ag.clock.Now()); err != nil {
			t.Fatal(err)
		}
		ag.clock.T = time.Date(2026, 11, 23, 16, 21, 0, 0, time.UTC)
		if _, err := ag.svc.Supervise(ag.ctx, ag.a.ID); err != nil {
			t.Fatal(err)
		}
		if ag.position(t, ap.PositionID).Open() || len(ag.sim.Held) != 0 {
			t.Fatal("exit did not work without eToro")
		}
		ag.sound(t)
	})
	t.Run("account unreadable: the session rule from our records; sold once readable", func(t *testing.T) {
		ag := newT212Agent(t)
		ap := ag.enterOne(t)
		ag.clock.T = time.Date(2026, 11, 23, 16, 21, 0, 0, time.UTC)
		ag.sim.ReadErr = domain.ErrUnavailable
		rep, err := ag.svc.Supervise(ag.ctx, ag.a.ID)
		if err != nil || !rep.LocalPositions || len(rep.Exits) != 1 {
			t.Fatalf("rep %+v %v", rep, err)
		}
		ag.sim.ReadErr = nil
		ag.clock.Advance(SupervisorInterval)
		if _, err := ag.svc.Supervise(ag.ctx, ag.a.ID); err != nil {
			t.Fatal(err)
		}
		if ag.position(t, ap.PositionID).Open() {
			t.Fatal("not sold once readable")
		}
		ag.sound(t)
	})
	t.Run("drawdown: kill, everything sold", func(t *testing.T) {
		ag := newT212Agent(t)
		ag.enterOne(t)
		ag.sim.Cash -= 1_000_00 // the owner's loss elsewhere in the account
		ag.clock.Advance(time.Minute)
		rep, err := ag.svc.Supervise(ag.ctx, ag.a.ID)
		if err != nil || !rep.Tripped || rep.Closing != 1 || len(ag.sim.Held) != 0 {
			t.Fatalf("rep %+v %v held %v", rep, err, ag.sim.Held)
		}
		ag.sound(t)
	})
}

// TestT212KillWithEightPositions: every stop cancelled, every position sold, nothing of the
// owner's touched; Stop and ForceClose work on a current Trading 212 account.
func TestT212KillWithEightPositions(t *testing.T) {
	ag := newT212Agent(t)
	cat := slices.Clone(t212Catalogue)
	for i := range 8 {
		id := domain.InstrumentID(100 + i)
		sym := "S" + string(rune('A'+i))
		cat = append(cat, domain.T212Instrument{Ticker: sym + "l_EQ", ShortName: sym, Name: sym, Currency: "GBX", Type: "STOCK", ISIN: "GB" + sym, MaxOpenQuantity: 1e12})
		ag.sim.Prices[sym+"l_EQ"] = 200_000_000
		ag.m.setQuote(id, 199_900_000, 200_000_000)
		in := domain.Instrument{ID: id, Symbol: sym + ".L", Type: "Stocks", Market: domain.MarketLSE, Currency: "GBX"}
		if err := ag.store.Instruments().Upsert(ag.ctx, in, domain.Eligibility{Instrument: id, Symbol: in.Symbol}, ag.clock.Now()); err != nil {
			t.Fatal(err)
		}
		if err := ag.store.Watchlist().Add(ag.ctx, ag.a.ID, id, "test", ag.clock.Now()); err != nil {
			t.Fatal(err)
		}
		if err := ag.store.InstrumentMatches().Put(ag.ctx, domain.InstrumentMatch{
			AccountID: ag.a.ID, Instrument: id, Ticker: sym + "l_EQ", ISIN: "GB" + sym,
			Method: domain.MatchMethodAuto, ConfirmedBy: ag.owner.UserID, ConfirmedAt: ag.clock.Now(), UpdatedAt: ag.clock.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	ag.sim.Catalogue = cat
	if err := ag.store.T212Instruments().ReplaceAll(ag.ctx, cat, ag.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ag.store.Watchlist().Remove(ag.ctx, ag.a.ID, 1); err != nil {
		t.Fatal(err)
	}
	ag.sim.Cash = 5_000_000
	ag.sim.Held["OWNl_EQ"] = domain.T212Position{Ticker: "OWNl_EQ", Quantity: 5_000_000, QuantityAvailable: 5_000_000, Cost: 1000}
	ag.cand = func(id domain.InstrumentID) *domain.Candidate {
		c := barcCand(id)
		c.SuggestedSize = 300_00
		return c
	}
	if _, err := ag.svc.StopAgent(ag.ctx, ag.owner, ag.a.ID); err != nil {
		t.Fatal(err)
	}
	ag.limits(t, func(l *domain.RiskLimits) { l.TradingBudget, l.MaxOpenPositions = 4_000_00, 10 })
	if _, err := ag.svc.StartAgent(ag.ctx, ag.owner, ag.a.ID); err != nil {
		t.Fatal(err)
	}
	if tr := ag.tick(t); tr.Entries != 8 {
		t.Fatalf("entries %d: %+v", tr.Entries, tr)
	}
	if n := len(ag.activeStops(t)); n != 8 {
		t.Fatalf("%d stops", n)
	}
	// Kill through the current account (no account id), as the panel does.
	rep, err := ag.svc.Kill(ag.ctx, ag.owner, "", "drill")
	if err != nil || rep.Closing != 8 {
		t.Fatalf("kill = %+v %v", rep, err)
	}
	if len(ag.activeStops(t)) != 0 || len(ag.sim.Held) != 1 || ag.sim.Held["OWNl_EQ"].Quantity != 5_000_000 {
		t.Fatalf("after the kill: stops %v held %v", ag.activeStops(t), ag.sim.Held)
	}
	ours, _ := ag.store.AgentPositions().ListOpen(ag.ctx, ag.a.ID)
	if len(ours) != 0 {
		t.Fatalf("still ours: %+v", ours)
	}
	ag.sound(t)
	// The killed agent stays killed; a reset reads Trading 212's equity.
	if tr := ag.tick(t); tr.Entries != 0 {
		t.Fatal("entered after the kill")
	}
	if err := ag.svc.ShareAccount(ag.ctx, ag.owner, ag.a.ID, "admin", domain.AccessTrade); err != nil {
		t.Fatal(err)
	}
	st, err := ag.svc.ResetKill(ag.ctx, ag.admin, ag.a.ID, ResetPhrase(ag.a.Name))
	if err != nil || st.State != domain.AgentStopped {
		t.Fatalf("reset = %+v %v", st, err)
	}
	rs, err := ag.store.RiskStates().Get(ag.ctx, ag.a.ID)
	acct, _ := ag.sim.Account(ports.WithAccount(ag.ctx, ag.a))
	if err != nil || rs.State.Killed || rs.State.HighWaterMark != acct.Equity {
		t.Fatalf("risk state %+v %v (equity %s)", rs, err, acct.Equity)
	}
}

// TestT212ControlsOnCurrentAccount: with a Trading 212 practice account current, stop, kill
// and force close (without an account id) work (review of R45.2–R45.5: exits must work).
func TestT212ControlsOnCurrentAccount(t *testing.T) {
	ag := newT212Agent(t)
	ag.enterOne(t)
	if v, err := ag.svc.CurrentAccount(ag.ctx, ag.owner); err != nil || v.ID != ag.a.ID {
		t.Fatalf("current = %+v %v", v, err)
	}
	if st, err := ag.svc.StopAgent(ag.ctx, ag.owner, ""); err != nil || st.State != domain.AgentStopped {
		t.Fatalf("stop = %+v %v", st, err)
	}
	admin := ag.admin
	if err := ag.svc.ShareAccount(ag.ctx, ag.owner, ag.a.ID, "admin", domain.AccessTrade); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.svc.SetCurrentAccount(ag.ctx, admin, ag.a.ID); err != nil {
		t.Fatal(err)
	}
	rep, err := ag.svc.ForceClose(ag.ctx, admin, "", "admin password 1")
	if err != nil || rep.Closing != 1 || len(ag.sim.Held) != 0 {
		t.Fatalf("force close = %+v %v", rep, err)
	}
	if k, err := ag.svc.Kill(ag.ctx, ag.owner, "", "drill"); err != nil || !k.Already && k.AccountID != ag.a.ID {
		t.Fatalf("kill = %+v %v", k, err)
	}
	ag.sound(t)
}

// TestT212Reconcile: created-not-sent abandoned, a lost buy adopted, a foreign holding left
// alone, an orphan stop of ours cancelled, a position without a stop keeps it unreconciled.
func TestT212Reconcile(t *testing.T) {
	ag := newT212Agent(t)
	ag.sim.Held["OWNl_EQ"] = domain.T212Position{Ticker: "OWNl_EQ", Quantity: 5_000_000, QuantityAvailable: 5_000_000}
	o, _ := domain.NewOrder(domain.Order{
		RequestID: "c1", AccountID: ag.a.ID, Mode: domain.ModeDemo, Kind: domain.OrderOpen, Platform: domain.PlatformTrading212,
		Ticker: barc, Quantity: 1_000_000, Direction: domain.Long, Instrument: 1, Amount: 300, Entry: 200_000_000, StopLoss: 190_000_000,
	}, ag.clock.Now())
	if err := ag.store.Orders().Create(ag.ctx, o); err != nil {
		t.Fatal(err)
	}
	r, err := ag.svc.Reconcile(ag.ctx, ag.a.ID)
	if err != nil || !r.OK {
		t.Fatalf("reconcile = %+v %v", r, err)
	}
	if got := ag.order(t, "c1"); got.State != domain.OrderAbandoned {
		t.Fatalf("created order %+v", got)
	}
	// A position whose stop vanished: not reconciled until the supervisor sells it.
	ag.enterOne(t)
	st := ag.activeStops(t)[0]
	if err := ag.sim.Cancel(ports.WithAccount(ag.ctx, ag.a), st.ID); err != nil {
		t.Fatal(err)
	}
	if r, _ := ag.svc.Reconcile(ag.ctx, ag.a.ID); r.OK {
		t.Fatal("reconciled without a stop")
	}
	// An orphan: a stop of ours left at Trading 212 for a closed position.
	ag2 := newT212Agent(t)
	ap2 := ag2.enterOne(t)
	st2 := ag2.orders(t, domain.OrderStop)[0]
	ap2.ClosedAt = ag2.clock.Now()
	_ = ag2.store.AgentPositions().Update(ag2.ctx, ap2)
	if _, err := ag2.svc.Reconcile(ag2.ctx, ag2.a.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ag2.sim.CancelIDs, st2.EtoroOrderID) || !slices.Contains(ag2.events(t), "order.orphan_cancelled") {
		t.Fatalf("orphan not cancelled: %v", ag2.sim.CancelIDs)
	}
	if ag2.sim.Held[barc].Quantity == 0 {
		t.Fatal("the shares were sold by reconciliation")
	}
}

func (x *t212X) order(t *testing.T, id string) domain.Order {
	t.Helper()
	o, err := x.store.Orders().Get(x.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
