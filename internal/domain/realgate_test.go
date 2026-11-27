package domain

import (
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestRealTradingEnabledByOwnerConsent: this build may arm real trading, since the owner's
// written consent (D39, 2026-11-27; it was TestRealTradingDisabledInThisBuild until then).
// Changing RealTradingEnabled changes this test: a visible, reviewed edit. A build without the
// consent refuses with ErrRealTradingDisabled, which still matches ErrRealModeGated.
func TestRealTradingEnabledByOwnerConsent(t *testing.T) {
	if !RealTradingEnabled {
		t.Fatal("RealTradingEnabled is true since the owner's written consent (D39); turning it off is a reviewed edit")
	}
	if !errors.Is(ErrRealTradingDisabled, ErrRealModeGated) || !errors.Is(ErrRealTradingDisabled, ErrForbidden) {
		t.Fatal("ErrRealTradingDisabled must match ErrRealModeGated and ErrForbidden")
	}
	if !errors.Is(ErrNoRealPermit, ErrRealModeGated) || !errors.Is(ErrNoRealPermit, ErrForbidden) {
		t.Fatal("ErrNoRealPermit must match ErrRealModeGated and ErrForbidden")
	}
}

// day is the risk date n days after d.
func day(d RiskDate, n int) RiskDate {
	t, _ := parseRiskDate(d) // the tests pass valid dates
	return RiskDate(t.AddDate(0, 0, n).Format("2006-01-02"))
}

func goodDay(d RiskDate) DayFacts {
	return DayFacts{Day: d, Decisions: 3, FilledOpens: 1, OpenedPositions: 1, ClosedSameDay: 1}
}

// qualifyingRun is n qualifying days, one per day, the latest age days before today.
func qualifyingRun(today RiskDate, n, age int) []DayFacts {
	out := make([]DayFacts, 0, n)
	for i := range n {
		out = append(out, goodDay(day(today, -age-i)))
	}
	return out
}

func TestDayFactsQualifies(t *testing.T) {
	tests := []struct {
		name string
		f    DayFacts
		want bool
	}{
		{"all there", goodDay("2026-11-02"), true},
		{"two positions, both closed", DayFacts{Decisions: 1, FilledOpens: 2, OpenedPositions: 2, ClosedSameDay: 2}, true},
		{"no decision", DayFacts{FilledOpens: 1, OpenedPositions: 1, ClosedSameDay: 1}, false},
		{"no filled open", DayFacts{Decisions: 1, OpenedPositions: 1, ClosedSameDay: 1}, false},
		{"no position", DayFacts{Decisions: 1, FilledOpens: 1}, false},
		{"one held past the day", DayFacts{Decisions: 1, FilledOpens: 2, OpenedPositions: 2, ClosedSameDay: 1}, false},
	}
	for _, tt := range tests {
		if got := tt.f.Qualifies(); got != tt.want {
			t.Errorf("%s: Qualifies = %v", tt.name, got)
		}
	}
}

func TestFoldDayFacts(t *testing.T) {
	// 2026-11-02 14:00 UTC is in risk day 2026-11-02; 22:30 UTC on it is in 2026-11-03.
	at := time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)
	open := func(id string, mode TradingMode, st OrderState, sends int, sent time.Time) Order {
		return Order{RequestID: id, AccountID: "d", Mode: mode, Kind: OrderOpen, State: st, Sends: sends, SentAt: sent, CreatedAt: sent}
	}
	orders := []Order{
		open("a", ModeDemo, OrderFilled, 1, at),
		open("b", ModeDemo, OrderPartial, 1, at),
		open("c", ModeReal, OrderFilled, 1, at),                                                      // not demo
		open("d", ModeDemo, OrderRejected, 1, at),                                                    // not filled
		open("e", ModeDemo, OrderFilled, 0, at),                                                      // never sent
		{RequestID: "f", Kind: OrderClose, Mode: ModeDemo, State: OrderFilled, Sends: 1, SentAt: at}, // a close
		open("g", ModeDemo, OrderFilled, 1, at.Add(9*time.Hour)),                                     // 23:00 UTC: the next risk day
	}
	positions := []AgentPosition{
		{PositionID: 1, OpenedAt: at, ClosedAt: at.Add(time.Hour)},
		{PositionID: 2, OpenedAt: at, ClosedAt: at.Add(9 * time.Hour)}, // closed the next risk day
		{PositionID: 3, OpenedAt: at.Add(9 * time.Hour)},               // still open
	}
	got := FoldDayFacts([]RiskDate{"2026-11-02", "2026-11-02", "2026-11-01"}, orders, positions)
	want := []DayFacts{
		{Day: "2026-11-01", Decisions: 1},
		{Day: "2026-11-02", Decisions: 2, FilledOpens: 2, OpenedPositions: 2, ClosedSameDay: 1},
		{Day: "2026-11-03", FilledOpens: 1, OpenedPositions: 1},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("FoldDayFacts =\n%+v\nwant\n%+v", got, want)
	}
}

// rule20 is a 20-day rule (OD20's first default), so the cases can count up to 20.
var rule20 = EvidenceRule{MinDays: 20, WindowDays: 60, MaxAgeDays: 7}

func TestQualifyingDays(t *testing.T) {
	const today RiskDate = "2026-11-24"
	rule := rule20
	tests := []struct {
		name  string
		facts []DayFacts
		count int
		ok    bool
		why   string
	}{
		{"20 days, latest yesterday", qualifyingRun(today, 20, 1), 20, true, "20 qualifying"},
		{"19 days", qualifyingRun(today, 19, 1), 19, false, "19 qualifying demo trading days"},
		{"20 days, latest 7 days old", qualifyingRun(today, 20, 7), 20, true, ""},
		{"20 days, latest 8 days old", qualifyingRun(today, 20, 8), 20, false, "8 days old"},
		{"today never counts", append(qualifyingRun(today, 19, 1), goodDay(today)), 19, false, "19 qualifying"},
		{"older than the window", append(qualifyingRun(today, 19, 1), goodDay(day(today, -61))), 19, false, ""},
		{"day 60 is in the window", append(qualifyingRun(today, 19, 1), goodDay(day(today, -60))), 20, true, ""},
		{"a future day never counts", append(qualifyingRun(today, 19, 1), goodDay(day(today, 3))), 19, false, ""},
		{"unreadable dates are ignored", append(qualifyingRun(today, 19, 1), goodDay("2026-13-01"), goodDay("x")), 19, false, ""},
		{
			"a duplicate that held a position disqualifies the day",
			append(qualifyingRun(today, 20, 1), DayFacts{Day: day(today, -1), OpenedPositions: 1}), 19, false, "",
		},
		{"no facts", nil, 0, false, "0 qualifying"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := QualifyingDays(tt.facts, today, rule)
			ok, why := ev.Sufficient(today, rule)
			if ev.Count != tt.count || len(ev.Days) != ev.Count || ok != tt.ok || !strings.Contains(why, tt.why) {
				t.Fatalf("count %d ok %v why %q (evidence %+v)", ev.Count, ok, why, ev)
			}
			if ev.Count > 0 && (ev.First != ev.Days[0] || ev.Last != ev.Days[ev.Count-1] || !slices.IsSorted(ev.Days)) {
				t.Fatalf("first/last/sorted: %+v", ev)
			}
		})
	}
	if ev := QualifyingDays(qualifyingRun(today, 20, 1), "not a date", rule); ev.Count != 0 {
		t.Fatalf("bad today: %+v", ev)
	}
	if ok, _ := (GateEvidence{Count: 25, Last: "bad"}).Sufficient(today, rule); ok {
		t.Fatal("unreadable last day passed")
	}
}

// TestQualifyingDaysProperties: a day without a decision, without a filled demo open, or with
// a position held past its risk day never qualifies; adding facts for a new day never lowers
// the count.
func TestQualifyingDaysProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(19, 20))
	const today RiskDate = "2026-11-24"
	for range 5_000 {
		var facts []DayFacts
		seen := map[RiskDate]bool{}
		for range r.IntN(40) {
			d := day(today, -r.IntN(70))
			if seen[d] {
				continue
			}
			seen[d] = true
			f := DayFacts{Day: d, Decisions: r.IntN(3), FilledOpens: r.IntN(3), OpenedPositions: r.IntN(3)}
			f.ClosedSameDay = r.IntN(f.OpenedPositions + 1)
			facts = append(facts, f)
		}
		ev := QualifyingDays(facts, today, DefaultEvidenceRule)
		for _, f := range facts {
			counted := slices.Contains(ev.Days, f.Day)
			bad := f.Decisions == 0 || f.FilledOpens == 0 || f.OpenedPositions == 0 || f.ClosedSameDay < f.OpenedPositions
			if counted && bad {
				t.Fatalf("a day that must not qualify was counted: %+v", f)
			}
		}
		// Adding a fact for a day not seen yet never lowers the count or the freshness.
		d := day(today, -r.IntN(70))
		if seen[d] {
			continue
		}
		more := append(slices.Clone(facts), DayFacts{Day: d, Decisions: r.IntN(2), FilledOpens: r.IntN(2), OpenedPositions: 1, ClosedSameDay: r.IntN(2)})
		ev2 := QualifyingDays(more, today, DefaultEvidenceRule)
		if ev2.Count < ev.Count || ev2.Last < ev.Last {
			t.Fatalf("adding %v lowered the evidence: %+v → %+v", d, ev, ev2)
		}
	}
}

// gateInput is an input on which every check passes.
func gateInput() GateInput {
	const today RiskDate = "2026-11-24"
	real := Account{ID: "real", Name: "Live ISA", OwnerID: "u-admin", Mode: ModeReal, Platform: PlatformEtoro}
	demo := Account{ID: "demo", Name: "Practice", OwnerID: "u-admin", Mode: ModeDemo}
	l := DefaultRealRiskLimits
	l.TradingBudget = 100_000
	return GateInput{
		Real: real, RealKeys: true, Reconciled: true, Agent: AgentStopped, Limits: l, Demo: &demo,
		Evidence: QualifyingDays(qualifyingRun(today, 20, 1), today, rule20),
		Today:    today, Rule: rule20,
		User: GateUser{ID: "u-admin", Role: RoleAdmin, Session: true}, Confirm: "  ENABLE REAL TRADING Live ISA ",
		Consent: "D39",
	}
}

func TestGateChecks(t *testing.T) {
	base := GateChecks(gateInput())
	names := make([]string, 0, len(base))
	for _, c := range base {
		if !c.Pass || c.Detail == "" {
			t.Fatalf("baseline check fails: %+v", c)
		}
		names = append(names, c.Name)
	}
	want := []string{CheckRealAccount, CheckRealKeys, CheckReconciled, CheckNotKilled, CheckAgentStopped, CheckRealLimits, CheckDemoAccount, CheckEvidence, CheckArmingUser, CheckConsent, CheckPhrase}
	if !slices.Equal(names, want) {
		t.Fatalf("checks %v, want %v", names, want)
	}
	// Every check passing arms (D39); one failing check refuses (below).
	if err := CanArm(base); err != nil {
		t.Fatalf("CanArm(all pass) = %v", err)
	}
	demo := func(f func(*Account)) func(*GateInput) {
		return func(in *GateInput) { d := *in.Demo; f(&d); in.Demo = &d }
	}
	tests := []struct {
		name  string
		edit  func(*GateInput)
		check string
		why   string
	}{
		{"a demo account", func(in *GateInput) { in.Real.Mode = ModeDemo }, CheckRealAccount, "not an eToro"},
		{"a Trading 212 account", func(in *GateInput) { in.Real.Platform = PlatformTrading212 }, CheckRealAccount, ""},
		{"no keys", func(in *GateInput) { in.RealKeys = false }, CheckRealKeys, ""},
		{"not reconciled", func(in *GateInput) { in.Reconciled = false }, CheckReconciled, ""},
		{"killed", func(in *GateInput) { in.Killed = true }, CheckNotKilled, ""},
		{"agent paused", func(in *GateInput) { in.Agent = AgentPaused }, CheckAgentStopped, "paused"},
		{"daily loss over the ceiling", func(in *GateInput) { in.Limits.DailyLoss = 201 }, CheckRealLimits, "dailyLossBp"},
		{"no position cap", func(in *GateInput) { in.Limits.MaxPosition = 0 }, CheckRealLimits, "maxPositionBp"},
		{"no demo account", func(in *GateInput) { in.Demo = nil }, CheckDemoAccount, "choose"},
		{"demo is real", demo(func(a *Account) { a.Mode = ModeReal }), CheckDemoAccount, "not an eToro demo"},
		{"demo is Saxo", demo(func(a *Account) { a.Platform = PlatformSaxo }), CheckDemoAccount, ""},
		{"demo of someone else", demo(func(a *Account) { a.OwnerID = "u-bob" }), CheckDemoAccount, "owner"},
		{"demo is the real account", demo(func(a *Account) { a.ID = "real" }), CheckDemoAccount, "owner"},
		{"19 days", func(in *GateInput) {
			in.Evidence = QualifyingDays(qualifyingRun(in.Today, 19, 1), in.Today, in.Rule)
		}, CheckEvidence, "19 qualifying"},
		{"20 days, latest 8 days old", func(in *GateInput) {
			in.Evidence = QualifyingDays(qualifyingRun(in.Today, 20, 8), in.Today, in.Rule)
		}, CheckEvidence, "8 days old"},
		{"an operator", func(in *GateInput) { in.User.Role = RoleOperator }, CheckArmingUser, "admin"},
		{"an admin who is not the owner", func(in *GateInput) { in.User.ID = "u-other" }, CheckArmingUser, "owner"},
		{"no user", func(in *GateInput) { in.User.ID, in.Real.OwnerID = "", "" }, CheckArmingUser, "owner"},
		{"an API key", func(in *GateInput) { in.User.Session = false }, CheckArmingUser, "web session"},
		{"wrong phrase", func(in *GateInput) { in.Confirm = "ENABLE REAL TRADING live isa" }, CheckPhrase, "ENABLE REAL TRADING Live ISA"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := gateInput()
			tt.edit(&in)
			checks := GateChecks(in)
			var failed []string
			for _, c := range checks {
				if !c.Pass {
					failed = append(failed, c.Name)
					if c.Name == tt.check && !strings.Contains(c.Detail, tt.why) {
						t.Fatalf("detail %q, want %q", c.Detail, tt.why)
					}
				}
			}
			if !slices.Contains(failed, tt.check) {
				t.Fatalf("failed %v, want %s among them", failed, tt.check)
			}
			err := CanArm(checks)
			gr, ok := errors.AsType[*GateRefusal](err)
			if !ok || !errors.Is(err, ErrRealModeGated) || !errors.Is(err, ErrConflict) || len(gr.Failed) != len(failed) ||
				!strings.Contains(err.Error(), tt.check) {
				t.Fatalf("CanArm = %v", err)
			}
		})
	}
	if err := CanArm(nil); !errors.Is(err, ErrRealModeGated) {
		t.Fatalf("CanArm(no checks) = %v", err)
	}
}

// TestGateProperties: CanArm allows arming exactly when every check passes (D39), and it
// refuses with the failing checks whenever one fails.
func TestGateProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 18))
	for range 10_000 {
		in := gateInput()
		flip := func() bool { return r.IntN(6) == 0 }
		if flip() {
			in.RealKeys = false
		}
		if flip() {
			in.Reconciled = false
		}
		if flip() {
			in.Killed = true
		}
		if flip() {
			in.Agent = AgentRunning
		}
		if flip() {
			in.Limits.DailyLoss = BasisPoints(r.IntN(3000))
		}
		if flip() {
			in.Demo = nil
		}
		if flip() {
			in.Evidence = QualifyingDays(qualifyingRun(in.Today, r.IntN(25), r.IntN(10)), in.Today, in.Rule)
		}
		if flip() {
			in.User.Role = RoleViewer
		}
		if flip() {
			in.Confirm = ""
		}
		checks := GateChecks(in)
		err := CanArm(checks)
		anyFail := slices.ContainsFunc(checks, func(c GateCheck) bool { return !c.Pass })
		if (err == nil) == anyFail || (err != nil && !errors.Is(err, ErrRealModeGated)) {
			t.Fatalf("CanArm = %v with failing checks %v: %+v", err, anyFail, in)
		}
		if _, refused := errors.AsType[*GateRefusal](err); refused != anyFail {
			t.Fatalf("refusal %v with failing checks %v: %v", refused, anyFail, err)
		}
	}
}

func TestRealGate(t *testing.T) {
	now := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	ev := GateEvidence{Days: []RiskDate{"2026-11-23"}, Count: 1, First: "2026-11-23", Last: "2026-11-23"}
	g := LockedGate("real")
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	if ok, why := g.Live(now, time.Time{}); ok || why != "" {
		t.Fatalf("locked gate live: %v %q", ok, why)
	}
	a := g.Arm("demo", "u-admin", ev, now)
	if err := a.Validate(); err != nil || a.State != GateArmed || !a.ExpiresAt.Equal(now.Add(RealGateTTL)) || a.Evidence.Count != 1 {
		t.Fatalf("Arm = %+v %v", a, err)
	}
	tests := []struct {
		name     string
		at       time.Time
		restored time.Time
		ok       bool
		why      string
	}{
		{"just armed", now, time.Time{}, true, ""},
		{"a restore before arming", now.Add(time.Hour), now.Add(-time.Second), true, ""},
		{"a restore after arming", now.Add(time.Hour), now.Add(time.Minute), false, DisarmRestore},
		{"a restore at the same time", now, now, false, DisarmRestore},
		{"one second before expiry", now.Add(RealGateTTL - time.Second), time.Time{}, true, ""},
		{"at expiry", now.Add(RealGateTTL), time.Time{}, false, DisarmExpired},
	}
	for _, tt := range tests {
		if ok, why := a.Live(tt.at, tt.restored); ok != tt.ok || why != tt.why {
			t.Errorf("%s: Live = %v %q", tt.name, ok, why)
		}
	}
	d := a.Disarm("u-op", strings.Repeat("é", 400), now.Add(time.Minute))
	if err := d.Validate(); err != nil || d.State != GateLocked || d.ArmedBy != "u-admin" || d.DisarmedBy != "u-op" ||
		len(d.DisarmReason) > MaxDisarmReason || !strings.HasPrefix(d.DisarmReason, "é") || strings.ContainsRune(d.DisarmReason, '�') {
		t.Fatalf("Disarm = %+v %v", d, err)
	}
	if ok, _ := d.Live(now.Add(time.Hour), time.Time{}); ok {
		t.Fatal("a disarmed gate is live")
	}
	if re := d.Arm("demo2", "u-admin", ev, now.Add(2*time.Hour)); re.DisarmReason != "" || re.DisarmedBy != "" || !re.DisarmedAt.IsZero() {
		t.Fatalf("re-arm kept the disarm: %+v", re)
	}
	bad := []RealGate{
		{State: GateLocked},
		{AccountID: "x", State: "open"},
		{AccountID: "x", State: GateArmed},
		{AccountID: "x", State: GateArmed, DemoAccountID: "d", ArmedBy: "u", ArmedAt: now, ExpiresAt: now},
		{AccountID: "x", State: GateLocked, DisarmReason: strings.Repeat("x", MaxDisarmReason+1)},
	}
	for _, g := range bad {
		if err := g.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("Validate(%+v) = %v", g, err)
		}
	}
}

func TestEvidenceRuleWithMinDays(t *testing.T) {
	for _, n := range []int{MinEvidenceDaysFloor, 1, 5, 20, DefaultEvidenceRule.WindowDays} {
		r, err := DefaultEvidenceRule.WithMinDays(n)
		if err != nil || r.MinDays != n || r.WindowDays != 60 || r.MaxAgeDays != 7 {
			t.Errorf("%d: %+v %v", n, r, err)
		}
	}
	for _, n := range []int{-1, 0, MinEvidenceDaysFloor - 1, DefaultEvidenceRule.WindowDays + 1} {
		if r, err := DefaultEvidenceRule.WithMinDays(n); err == nil || r.MinDays != DefaultEvidenceRule.MinDays {
			t.Errorf("%d accepted: %+v", n, r)
		}
	}
}

// TestRealConsents pins the real accounts the owner's written consent covers (D39: Real Money
// Maker only). Adding one needs a new written decision; this test changes with it.
func TestRealConsents(t *testing.T) {
	if d, ok := RealConsentFor("c0a9096b-d7af-4360-97e0-8fb2f691cb50"); !ok || d != "D39" {
		t.Fatalf("Real Money Maker: %q %v", d, ok)
	}
	if len(realConsents) != 1 {
		t.Fatalf("%d consented real accounts; D39 covers exactly one", len(realConsents))
	}
	for _, id := range []string{"", "real", "93b46c9a-0f9a-4794-9ee8-37b14ba18466"} {
		if _, ok := RealConsentFor(id); ok {
			t.Errorf("%q is consented", id)
		}
	}
	in := gateInput()
	in.Consent = ""
	if err := CanArm(GateChecks(in)); err == nil || !strings.Contains(err.Error(), CheckConsent) {
		t.Fatalf("arming without consent: %v", err)
	}
}
