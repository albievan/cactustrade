package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/strategy"
)

func TestAgentStyle(t *testing.T) {
	ae := newAgentEnv(t)
	if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	v, err := ae.svc.AgentStyle(ae.ctx, ae.admin)
	if err != nil || v.Aggressiveness != domain.AggressivenessMedium || v.Effective != domain.AggressivenessMedium ||
		v.Autonomy != domain.AutonomyMedium || v.Policy.Research != domain.ResearchAutoAdd || !v.CanEdit {
		t.Fatalf("defaults = %+v %v", v, err)
	}
	viewer := ae.shared(t, "vera", domain.RoleViewer, domain.AccessView)
	trader := ae.shared(t, "tom", domain.RoleOperator, domain.AccessTrade)
	if v, err := ae.svc.AgentStyle(ae.ctx, viewer); err != nil || v.CanEdit || v.EditBlocked != msgLimitsNotOwner {
		t.Fatalf("viewer = %+v %v", v, err)
	}
	tests := []struct {
		name string
		p    Principal
		aggr domain.Aggressiveness
		aut  domain.Autonomy
		want error
	}{
		{"viewer", viewer, domain.AggressivenessHigh, "", domain.ErrForbidden},
		{"trade access is not the owner", trader, domain.AggressivenessHigh, "", domain.ErrForbidden},
		{"API key", apiKey(ae.admin), domain.AggressivenessHigh, "", domain.ErrForbidden},
		{"nothing given", ae.admin, "", "", domain.ErrInvalid},
		{"unknown aggressiveness", ae.admin, "reckless", "", domain.ErrInvalid},
		{"unknown autonomy", ae.admin, "", "total", domain.ErrInvalid},
		{"both", ae.admin, domain.AggressivenessHigh, domain.AutonomyFull, nil},
		{"one", ae.admin, domain.AggressivenessLow, "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ae.svc.SetAgentStyle(ae.ctx, tc.p, tc.aggr, tc.aut)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) && !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	v, _ = ae.svc.AgentStyle(ae.ctx, ae.admin)
	if v.Aggressiveness != domain.AggressivenessLow || v.Autonomy != domain.AutonomyFull || !v.Policy.AutoStart {
		t.Fatalf("stored = %+v", v)
	}
	audit := ae.store.AuditEntries()
	if a := audit[len(audit)-1]; a.Action != "agent.style_changed" || a.Details != "aggressiveness high→low, autonomy full→full" {
		t.Fatalf("audit = %+v", a)
	}
	if !slices.Contains(ae.eventKinds(t), "agent.style_changed") {
		t.Fatal("no event")
	}
	// Only while stopped and not killed, like the limits.
	for _, st := range []domain.AgentState{domain.AgentRunning, domain.AgentPaused, domain.AgentKilled} {
		ae.setAgent(t, st)
		if _, err := ae.svc.SetAgentStyle(ae.ctx, ae.admin, domain.AggressivenessFull, ""); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("changed while %s: %v", st, err)
		}
		if v, _ := ae.svc.AgentStyle(ae.ctx, ae.admin); v.CanEdit {
			t.Fatalf("editable while %s", st)
		}
	}
	// Unknown stored values read as the defaults (never full by accident).
	ae.setAgent(t, domain.AgentStopped)
	_ = ae.store.Settings().Set(ae.ctx, autonomyKey(ae.a.ID), "everything", ae.clock.Now())
	_ = ae.store.Settings().Set(ae.ctx, aggressivenessKey(ae.a.ID), "max", ae.clock.Now())
	if v, _ := ae.svc.AgentStyle(ae.ctx, ae.admin); v.Autonomy != domain.AutonomyMedium || v.Aggressiveness != domain.AggressivenessMedium {
		t.Fatalf("bad stored values = %+v", v)
	}
}

func TestApplyRiskPresetUseCase(t *testing.T) {
	ctx := context.Background()
	e, _ := withBroker(t) // equity 106,100 cents
	acct, _ := e.svc.CurrentAccount(ctx, e.admin)
	base := domain.DefaultRiskLimits
	base.TradingBudget = 50_000
	if _, err := e.svc.SetRiskLimits(ctx, e.admin, base); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.svc.RiskLimitsView(ctx, e.admin); v.Preset != domain.PresetCustom {
		t.Fatalf("the defaults (shorts 25 %%) match %s", v.Preset)
	}
	l, err := e.svc.ApplyRiskPreset(ctx, e.admin, domain.PresetLow)
	if err != nil || l.TradingBudget != 50_000 || l.DailyLoss != 50 || l.MaxOpenPositions != 3 || l.MaxShort != 0 {
		t.Fatalf("low = %+v %v", l, err)
	}
	if v, _ := e.svc.RiskLimitsView(ctx, e.admin); v.Preset != domain.PresetLow || v.Limits != l {
		t.Fatalf("view = %+v", v)
	}
	list, err := e.svc.RiskPresets(ctx, e.admin)
	if err != nil || len(list.Presets) != len(domain.RiskPresets) || list.Current != domain.PresetLow || list.Clamped {
		t.Fatalf("presets = %+v %v", list, err)
	}
	if list.Presets[4].Preset != domain.PresetHigh || list.Presets[4].Limits.MaxOpenPositions != 15 || list.Presets[4].Summary == "" {
		t.Fatalf("high = %+v", list.Presets[4])
	}
	audit := e.store.AuditEntries()
	if a := audit[len(audit)-1]; a.Action != "risk.limits_changed" || !strings.Contains(a.Details, `"dailyLossBp":50`) {
		t.Fatalf("audit = %+v", a)
	}
	viewer := e.user(t, "vera", domain.RoleViewer)
	_ = e.svc.ShareAccount(ctx, e.admin, acct.ID, "vera", domain.AccessView)
	key := e.admin
	key.Via = ViaAPIKey
	for name, tc := range map[string]struct {
		p      Principal
		preset domain.RiskPreset
		want   error
	}{
		"viewer":  {viewer, domain.PresetHigh, domain.ErrForbidden},
		"API key": {key, domain.PresetHigh, domain.ErrForbidden},
		"unknown": {e.admin, "max", domain.ErrInvalid},
		"custom":  {e.admin, domain.PresetCustom, domain.ErrInvalid},
	} {
		if _, err := e.svc.ApplyRiskPreset(ctx, tc.p, tc.preset); !errors.Is(err, tc.want) && !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	_ = e.store.Settings().Set(ctx, agentStateKey(acct.ID), string(domain.AgentRunning), e.clock.T)
	if _, err := e.svc.ApplyRiskPreset(ctx, e.admin, domain.PresetHigh); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("applied while running: %v", err)
	}
	if v, _ := e.svc.RiskLimitsView(ctx, e.admin); v.Preset != domain.PresetLow {
		t.Fatal("a refused preset changed the limits")
	}
}

// TestRiskPresetOnARealAccount: presets are capped at the real ceilings, and one that raises
// a limit disarms an armed real account (OD24) while a lower one does not.
func TestRiskPresetOnARealAccount(t *testing.T) {
	g := newGateEnv(t)
	if _, err := g.svc.SetCurrentAccount(g.ctx, g.admin, g.real.ID); err != nil {
		t.Fatal(err)
	}
	list, err := g.svc.RiskPresets(g.ctx, g.admin)
	if err != nil || !list.Clamped {
		t.Fatalf("presets = %+v %v", list, err)
	}
	for _, p := range list.Presets {
		if p.Limits.DailyLoss > domain.RealCeilings.DailyLoss || p.Limits.MaxOpenPositions > domain.RealCeilings.MaxOpenPositions {
			t.Fatalf("%s above the real ceilings: %+v", p.Preset, p.Limits)
		}
	}
	g.arm(t)
	l, err := g.svc.ApplyRiskPreset(g.ctx, g.admin, domain.PresetLow)
	if err != nil || l.TradingBudget != 1_000_00 {
		t.Fatalf("low = %+v %v", l, err)
	}
	if g.gate(t).State != domain.GateArmed {
		t.Fatal("a lower preset disarmed the real account")
	}
	l, err = g.svc.ApplyRiskPreset(g.ctx, g.admin, domain.PresetHigh)
	if err != nil || l.DailyLoss != domain.RealCeilings.DailyLoss || l.MaxOpenPositions != domain.RealCeilings.MaxOpenPositions || l.Validate() != nil {
		t.Fatalf("high on real = %+v %v", l, err)
	}
	if gate := g.gate(t); gate.State != domain.GateLocked || !strings.Contains(gate.DisarmReason, domain.DisarmLimits) {
		t.Fatalf("a raise left the gate armed: %+v", gate)
	}
	// Full autonomy never starts a real agent (OD26).
	if _, err := g.svc.SetAgentStyle(g.ctx, g.admin, "", domain.AutonomyFull); err != nil {
		t.Fatal(err)
	}
	if v, _ := g.svc.AgentStyle(g.ctx, g.admin); v.Policy.AutoStart {
		t.Fatal("auto-start on a real account")
	}
	g.svc.autoStartAll(g.ctx, []domain.Account{g.real})
	if st := g.svc.agentState(g.ctx, g.real.ID); st != domain.AgentStopped {
		t.Fatalf("real agent is %s", st)
	}
}

func TestResearchMode(t *testing.T) {
	ctx := context.Background()
	e, _ := withBroker(t)
	acct, _ := e.svc.CurrentAccount(ctx, e.admin)
	t212, err := e.svc.CreateAccountOn(ctx, e.admin, domain.PlatformTrading212, "T212", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		account string
		level   domain.Autonomy
		want    domain.ResearchMode
	}{
		{"default (medium)", acct.ID, "", domain.ResearchAutoAdd},
		{"low proposes", acct.ID, domain.AutonomyLow, domain.ResearchPropose},
		{"medium adds", acct.ID, domain.AutonomyMedium, domain.ResearchAutoAdd},
		{"high adds", acct.ID, domain.AutonomyHigh, domain.ResearchAutoAdd},
		{"full adds", acct.ID, domain.AutonomyFull, domain.ResearchAutoAdd},
		{"unknown account", "nope", "", domain.ResearchOff},
		{"view-only platform", t212.ID, "", domain.ResearchOff},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.level != "" {
				if _, err := e.svc.SetAgentStyle(ctx, e.admin, "", tc.level); err != nil {
					t.Fatal(err)
				}
			}
			if got := e.svc.ResearchMode(ctx, tc.account); got != tc.want {
				t.Fatalf("ResearchMode = %s, want %s", got, tc.want)
			}
			pol, ok := e.svc.AutonomyPolicy(ctx, tc.account)
			if pol.Research != tc.want || ok != (tc.want != domain.ResearchOff) {
				t.Fatalf("AutonomyPolicy = %+v %v", pol, ok)
			}
		})
	}
}

// scanEnv is a scanner environment at 15:00 UTC (NYSE in session in the test calendar) where
// AAPL (1) is the only share with a signal and a theme "Tech" matches it by symbol.
func scanEnv(t *testing.T) (*env, domain.AccountView) {
	t.Helper()
	ctx := context.Background()
	e, b := withBroker(t)
	e.svc.d.Research = b
	e.svc.d.Calendar = testCalendar{}
	e.clock.T = time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)
	acct, _ := e.svc.CurrentAccount(ctx, e.admin)
	b.Bars = map[domain.InstrumentID][]domain.Candle{1: bars(60)}
	if _, err := e.svc.SaveTheme(ctx, e.admin, domain.Theme{Name: "Tech", Keywords: []string{"AAPL"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	e.svc.evalOverride = func(id domain.InstrumentID, _ []domain.Candle) strategy.Decision {
		if id == 1 {
			return strategy.Decision{Candidate: &domain.Candidate{Instrument: 1, Direction: domain.Long, Playbook: "trend_breakout", Reason: "up"}}
		}
		return strategy.Decision{Reason: "no signal"}
	}
	return e, acct
}

func TestScannerByAutonomy(t *testing.T) {
	tests := []struct {
		level                   domain.Autonomy
		added, proposed, themed bool
		wantOutcome             domain.ReviewOutcome
	}{
		{domain.AutonomyLow, false, true, false, domain.ReviewProposed},
		{domain.AutonomyMedium, true, false, false, domain.ReviewAdded},
		{domain.AutonomyHigh, true, false, true, domain.ReviewAdded},
		{domain.AutonomyFull, true, false, true, domain.ReviewAdded},
	}
	for _, tc := range tests {
		t.Run(string(tc.level), func(t *testing.T) {
			ctx := context.Background()
			e, acct := scanEnv(t)
			if _, err := e.svc.SetAgentStyle(ctx, e.admin, "", tc.level); err != nil {
				t.Fatal(err)
			}
			res, err := e.svc.ScanNow(ctx, e.admin)
			if err != nil {
				t.Fatal(err)
			}
			if (len(res.Added) == 1) != tc.added || (len(res.Proposed) == 1) != tc.proposed {
				t.Fatalf("scan = %+v", res)
			}
			wl, _ := e.svc.Watchlist(ctx, e.admin)
			if (len(wl) == 1) != tc.added {
				t.Fatalf("watchlist = %+v", wl)
			}
			rs, _, _ := e.svc.Reviews(ctx, e.admin, 0)
			i := slices.IndexFunc(rs, func(r domain.Review) bool { return r.Symbol == "AAPL" })
			if i < 0 || rs[i].Outcome != tc.wantOutcome {
				t.Fatalf("reviews = %+v", rs)
			}
			themes, _ := e.store.Themes().List(ctx, acct.ID)
			if got := slices.Contains(themes[0].Instruments, 1); got != tc.themed {
				t.Fatalf("theme assigned = %v, want %v (%+v)", got, tc.themed, themes[0])
			}
			if e.svc.CanAddToWatchlist(ctx, e.admin) != true {
				t.Fatal("the owner cannot accept a proposal")
			}
			if tc.proposed {
				// Accepting the proposal: a person adds the share (the hand path).
				if _, err := e.svc.AddToWatchlist(ctx, e.admin, 1); err != nil {
					t.Fatal(err)
				}
				if wl, _ := e.svc.Watchlist(ctx, e.admin); len(wl) != 1 || domain.IsAutoAdded(wl[0].AddedBy) {
					t.Fatalf("accepted = %+v", wl)
				}
			}
		})
	}
}

func TestScannerPrunesStaleAutomaticShares(t *testing.T) {
	ctx := context.Background()
	e, acct := scanEnv(t)
	e.svc.evalOverride = func(domain.InstrumentID, []domain.Candle) strategy.Decision {
		return strategy.Decision{Reason: "no signal"}
	}
	old, recent := e.clock.T.AddDate(0, 0, -20), e.clock.T.AddDate(0, 0, -2)
	add := func(id domain.InstrumentID, sym, by string, at time.Time) {
		in := domain.Instrument{ID: id, Symbol: sym, Name: sym, Type: "Stocks", Market: domain.MarketNYSE}
		if err := e.store.Instruments().Upsert(ctx, in, domain.Eligibility{Instrument: id, RealLong: true}, at); err != nil {
			t.Fatal(err)
		}
		if err := e.store.Watchlist().Add(ctx, acct.ID, id, by, at); err != nil {
			t.Fatal(err)
		}
	}
	add(201, "OLDS", domain.WatchAddedByScanner, old)    // stale, automatic: removed (high)
	add(202, "OWNR", "admin", old)                       // stale, but a person added it: kept
	add(203, "NEWS", domain.WatchAddedByScanner, recent) // too recent: kept
	add(204, "HELD", domain.WatchAddedByResearch, old)   // a position of ours: kept
	add(205, "SIGL", domain.WatchAddedByResearch, old)   // a signal 1 day ago: kept
	add(206, "RSCH", domain.WatchAddedByResearch, old)   // stale, automatic (research): removed
	if err := e.store.AgentPositions().Add(ctx, domain.AgentPosition{AccountID: acct.ID, PositionID: 9, OpenRequestID: "r", Instrument: 204, Direction: domain.Long, OpenedAt: recent}); err != nil {
		t.Fatal(err)
	}
	e.svc.storeWatchSignals(ctx, acct.ID, map[domain.InstrumentID]time.Time{205: e.clock.T.AddDate(0, 0, -1)})
	symbols := func() []string {
		wl, _ := e.svc.Watchlist(ctx, e.admin)
		var out []string
		for _, w := range wl {
			out = append(out, w.Instrument.Symbol)
		}
		slices.Sort(out)
		return out
	}
	// Medium: nothing is removed.
	if res, err := e.svc.ScanNow(ctx, e.admin); err != nil || len(res.Removed) != 0 {
		t.Fatalf("medium scan = %+v %v", res, err)
	}
	if _, err := e.svc.SetAgentStyle(ctx, e.admin, "", domain.AutonomyHigh); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.ScanNow(ctx, e.admin)
	if err != nil || !slices.Equal(res.Removed, []string{"OLDS", "RSCH"}) {
		t.Fatalf("high scan = %+v %v", res, err)
	}
	if got := symbols(); !slices.Equal(got, []string{"HELD", "NEWS", "OWNR", "SIGL"}) {
		t.Fatalf("watchlist = %v", got)
	}
	if !slices.ContainsFunc(e.store.AuditEntries(), func(a domain.AuditEntry) bool { return a.Action == "watchlist.auto_remove" }) {
		t.Fatal("no audit")
	}
	// Without a calendar nothing is removed (fail closed).
	e.svc.d.Calendar = nil
	e.clock.T = e.clock.T.AddDate(0, 0, 30)
	if removed := e.svc.pruneStale(ctx, acct, e.svc.watchSignals(ctx, acct.ID)); len(removed) != 0 {
		t.Fatalf("removed without a calendar: %v", removed)
	}
}

func TestScanAllFollowsTheAutonomyInterval(t *testing.T) {
	ctx := context.Background()
	e, acct := scanEnv(t)
	due := func() bool { return e.svc.scanDue(ctx, acct.Account, e.clock.T) }
	if !due() {
		t.Fatal("the first scan is not due")
	}
	e.clock.T = e.clock.T.Add(ScanTick)
	if due() {
		t.Fatal("medium autonomy scanned after 5 minutes")
	}
	e.clock.T = e.clock.T.Add(2 * ScanTick)
	if !due() {
		t.Fatal("medium autonomy did not scan after 15 minutes")
	}
	if _, err := e.svc.SetAgentStyle(ctx, e.admin, "", domain.AutonomyHigh); err != nil {
		t.Fatal(err)
	}
	e.clock.T = e.clock.T.Add(ScanTick)
	if !due() {
		t.Fatal("high autonomy did not scan after 5 minutes")
	}
}

func TestLowAutonomyTradesOwnerSharesOnly(t *testing.T) {
	ae := newAgentEnv(t, instNYSE)
	ae.addNYSE(t, 150, "SCAN")
	if err := ae.store.Watchlist().Remove(ae.ctx, ae.a.ID, 150); err != nil {
		t.Fatal(err)
	}
	if err := ae.store.Watchlist().Add(ae.ctx, ae.a.ID, 150, domain.WatchAddedByScanner, ae.clock.Now()); err != nil {
		t.Fatal(err)
	}
	style := func(a domain.Autonomy) {
		if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := ae.svc.SetAgentStyle(ae.ctx, ae.admin, "", a); err != nil {
			t.Fatal(err)
		}
		if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
			t.Fatal(err)
		}
	}
	checked := func(tr AgentTrace) []domain.InstrumentID {
		var out []domain.InstrumentID
		for _, it := range tr.Instruments {
			out = append(out, it.Instrument)
		}
		slices.Sort(out)
		return out
	}
	ae.setCand(func(domain.InstrumentID) *domain.Candidate { return nil })
	if got := checked(ae.tick(t)); !slices.Equal(got, []domain.InstrumentID{instNYSE, 150}) {
		t.Fatalf("medium checked %v", got)
	}
	style(domain.AutonomyLow)
	if got := checked(ae.tick(t)); !slices.Equal(got, []domain.InstrumentID{instNYSE}) {
		t.Fatalf("low checked %v", got)
	}
	if err := ae.store.Watchlist().Remove(ae.ctx, ae.a.ID, instNYSE); err != nil {
		t.Fatal(err)
	}
	if tr := ae.tick(t); !strings.Contains(tr.Skipped, "low autonomy") || len(tr.Instruments) != 0 {
		t.Fatalf("only automatic shares: %+v", tr)
	}
}

func TestAgentUsesTheAccountsAggressiveness(t *testing.T) {
	ae := newAgentEnv(t)
	var seen []strategy.Config
	ae.svc.evalConfigOverride = func(_ domain.InstrumentID, _ []domain.Candle, cfg strategy.Config) strategy.Decision {
		seen = append(seen, cfg)
		return strategy.Decision{Reason: "no signal (test)"}
	}
	ae.tick(t)
	if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.svc.SetAgentStyle(ae.ctx, ae.admin, domain.AggressivenessFull, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	ae.tick(t)
	if len(seen) != 2 || seen[0] != strategy.ConfigFor(domain.AggressivenessMedium, true) || seen[1] != strategy.ConfigFor(domain.AggressivenessFull, true) {
		t.Fatalf("configs = %+v", seen)
	}
}

func TestFullAutonomyStartsAtTheOpen(t *testing.T) {
	type setup func(t *testing.T, ae *agentEnv)
	full := func(t *testing.T, ae *agentEnv) {
		t.Helper()
		if _, err := ae.svc.SetAgentStyle(ae.ctx, ae.admin, "", domain.AutonomyFull); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name  string
		setup setup
		want  domain.AgentState
	}{
		{"full, stopped, NYSE open", full, domain.AgentRunning},
		{"medium never starts itself", func(*testing.T, *agentEnv) {}, domain.AgentStopped},
		{"high never starts itself", func(t *testing.T, ae *agentEnv) {
			if _, err := ae.svc.SetAgentStyle(ae.ctx, ae.admin, "", domain.AutonomyHigh); err != nil {
				t.Fatal(err)
			}
		}, domain.AgentStopped},
		{"paused stays paused", func(t *testing.T, ae *agentEnv) {
			full(t, ae)
			ae.setAgent(t, domain.AgentPaused)
		}, domain.AgentPaused},
		{"killed stays killed", func(t *testing.T, ae *agentEnv) {
			full(t, ae)
			if _, err := ae.svc.Kill(ae.ctx, ae.admin, ae.a.ID, "drill"); err != nil {
				t.Fatal(err)
			}
		}, domain.AgentKilled},
		{"no market open", func(t *testing.T, ae *agentEnv) {
			full(t, ae)
			ae.clock.Advance(7*time.Hour + 25*time.Minute) // 22:25 UTC: every test market is shut
		}, domain.AgentStopped},
		{"no budget", func(t *testing.T, ae *agentEnv) {
			full(t, ae)
			ae.limits(t, func(l *domain.RiskLimits) { l.TradingBudget = 0 })
		}, domain.AgentStopped},
		{"not reconciled", func(t *testing.T, ae *agentEnv) {
			full(t, ae)
			ae.svc.rt.mu.Lock()
			ae.svc.rt.reconciled[ae.a.ID] = Reconciliation{OK: false, Reason: "test"}
			ae.svc.rt.mu.Unlock()
		}, domain.AgentStopped},
		{"agent loop down", func(t *testing.T, ae *agentEnv) {
			full(t, ae)
			ae.svc.SetAgentLoop(false)
		}, domain.AgentStopped},
		{"no calendar", func(t *testing.T, ae *agentEnv) {
			full(t, ae)
			ae.svc.d.Calendar = nil
		}, domain.AgentStopped},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ae := newAgentEnv(t)
			if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, ae)
			ae.svc.autoStartAll(ae.ctx, []domain.Account{ae.a})
			if st := ae.svc.agentState(ae.ctx, ae.a.ID); st != tc.want {
				t.Fatalf("state = %s, want %s", st, tc.want)
			}
			started := slices.Contains(ae.eventKinds(t), "agent.auto_started")
			if started != (tc.want == domain.AgentRunning) {
				t.Fatalf("auto_started event = %v", started)
			}
		})
	}
}

func TestFullAutonomyStartsOncePerTradingDay(t *testing.T) {
	ae := newAgentEnv(t)
	if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.svc.SetAgentStyle(ae.ctx, ae.admin, "", domain.AutonomyFull); err != nil {
		t.Fatal(err)
	}
	pass := func() { // the agent loop's pass starts with the auto-start
		ae.svc.autoStartAll(ae.ctx, []domain.Account{ae.a})
	}
	pass()
	if st := ae.svc.agentState(ae.ctx, ae.a.ID); st != domain.AgentRunning {
		t.Fatalf("not started: %s", st)
	}
	if a := ae.store.AuditEntries(); a[len(a)-1].Action != "agent.auto_start" || a[len(a)-1].Actor != "autonomy" {
		t.Fatalf("audit = %+v", a[len(a)-1])
	}
	// The owner stops it: it stays stopped for the rest of the trading day.
	if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	ae.clock.Advance(time.Hour)
	pass()
	if st := ae.svc.agentState(ae.ctx, ae.a.ID); st != domain.AgentStopped {
		t.Fatalf("restarted the same day: %s", st)
	}
	// The next trading day it starts again (reconciled again, as the supervisor does).
	ae.clock.Advance(23 * time.Hour)
	if _, err := ae.svc.Reconcile(ae.ctx, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	pass()
	if st := ae.svc.agentState(ae.ctx, ae.a.ID); st != domain.AgentRunning {
		t.Fatalf("not started the next day: %s", st)
	}
}

func TestFullAutonomyAdaptsAggressiveness(t *testing.T) {
	ae := newAgentEnv(t)
	if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.svc.SetAgentStyle(ae.ctx, ae.admin, domain.AggressivenessHigh, domain.AutonomyFull); err != nil {
		t.Fatal(err)
	}
	l := ae.svc.riskLimits(ae.ctx, ae.a.ID)
	observe := func(equity domain.Cents) {
		t.Helper()
		if _, err := ae.svc.observe(ae.ctx, ae.a, domain.Portfolio{AvailableCash: equity, AsOf: ae.clock.Now()}, l, ae.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	effective := func() domain.Aggressiveness { return ae.svc.effectiveAggressiveness(ae.ctx, ae.a) }
	// Day 1: the daily loss latch is set; the next day starts one step lower.
	observe(1_000_000_00)
	observe(900_000_00)
	ae.clock.Advance(24 * time.Hour)
	observe(900_000_00)
	if effective() != domain.AggressivenessMedium {
		t.Fatalf("after a latch day: %s", effective())
	}
	if !slices.Contains(ae.eventKinds(t), "agent.aggressiveness_adapted") {
		t.Fatal("no event")
	}
	// Three clean days on which the agent ran bring it back, never above the chosen level.
	for range 4 {
		ae.svc.markRan(ae.ctx, ae.a, domain.RiskDay(ae.clock.Now()))
		ae.clock.Advance(24 * time.Hour)
		observe(900_000_00)
	}
	if effective() != domain.AggressivenessHigh {
		t.Fatalf("after clean days: %s", effective())
	}
	// A change of style resets the adjustment; other levels never adapt.
	observe(1_000_000_00)
	observe(500_000_00)
	ae.clock.Advance(24 * time.Hour)
	observe(500_000_00)
	if effective() != domain.AggressivenessMedium {
		t.Fatalf("second latch: %s", effective())
	}
	if _, err := ae.svc.SetAgentStyle(ae.ctx, ae.admin, "", domain.AutonomyHigh); err != nil {
		t.Fatal(err)
	}
	observe(100_000_00)
	ae.clock.Advance(24 * time.Hour)
	observe(100_000_00)
	if effective() != domain.AggressivenessHigh {
		t.Fatalf("high autonomy adapted: %s", effective())
	}
	// An unreadable adjustment is the most careful reading.
	_ = ae.store.Settings().Set(ae.ctx, adaptKey(ae.a.ID), "{", ae.clock.Now())
	if _, err := ae.svc.SetAgentStyle(ae.ctx, ae.admin, "", domain.AutonomyFull); err != nil {
		t.Fatal(err)
	}
	_ = ae.store.Settings().Set(ae.ctx, adaptKey(ae.a.ID), "{", ae.clock.Now())
	if effective() != domain.AggressivenessLow {
		t.Fatalf("unreadable adjustment: %s", effective())
	}
}

func TestLiveRegimesSayWhatTheAgentMayDo(t *testing.T) {
	ae := newAgentEnv(t)
	r := domain.Review{
		AccountID: ae.a.ID, Instrument: instNYSE, Symbol: "AAPL", At: ae.clock.Now(), Outcome: domain.ReviewNoSignal,
		Probs: map[domain.Regime]float64{domain.RegimeTrending: 0.65, domain.RegimeChoppy: 0.25, domain.RegimeHighVol: 0.10},
	}
	if err := ae.store.Reviews().Upsert(ae.ctx, r); err != nil {
		t.Fatal(err)
	}
	snap, err := ae.svc.LiveSnapshot(ae.ctx, ae.admin, ae.a.ID)
	if err != nil || len(snap.Regimes) != 1 || len(snap.RegimeLegend) != 3 {
		t.Fatalf("snapshot = %+v %v", snap.Regimes, err)
	}
	rg := snap.Regimes[0]
	if rg.Mood.Label != "Trending" || rg.Stance != strategy.StanceTrend || rg.MayDo != "May buy a breakout or short a breakdown" {
		t.Fatalf("regime = %+v", rg)
	}
	if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.svc.SetAgentStyle(ae.ctx, ae.admin, domain.AggressivenessLow, ""); err != nil {
		t.Fatal(err)
	}
	snap, _ = ae.svc.LiveSnapshot(ae.ctx, ae.admin, ae.a.ID)
	if rg := snap.Regimes[0]; rg.Stance != strategy.StanceWait || !strings.Contains(rg.MayDo, "70 %") {
		t.Fatalf("at low aggressiveness = %+v", rg)
	}
}
