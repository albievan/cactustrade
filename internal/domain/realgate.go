package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// The real-mode gate (PLAN 7.10). Real trading needs an armed gate per real-money account,
// and arming needs evidence from a linked demo account, an admin who owns the account, a
// typed phrase and the password. This build can show the checklist but can never arm:
// RealTradingEnabled is false until the owner's written consent (PLAN 7.18).

// RealTradingEnabled says whether this build may arm real trading at all. It stays false:
// it needs the owner's written consent, PLAN 7.18. Changing it changes
// TestRealTradingDisabledInThisBuild (a visible, reviewed edit).
const RealTradingEnabled = false

// EvidenceRule is how much demo history arming needs (OD20): MinDays qualifying risk days
// within the WindowDays completed risk days before today, the latest at most MaxAgeDays old.
type EvidenceRule struct {
	MinDays    int
	WindowDays int
	MaxAgeDays int
}

// DefaultEvidenceRule is OD20's default: 20 qualifying days in the last 60, the latest at
// most 7 days old.
var DefaultEvidenceRule = EvidenceRule{MinDays: 20, WindowDays: 60, MaxAgeDays: 7}

// DayFacts is what the linked demo account did on one risk day (OD19).
type DayFacts struct {
	Day RiskDate
	// Decisions counts the decision-log entries (the agent ran and evaluated a candidate).
	Decisions int
	// FilledOpens counts open orders in demo mode that were sent and filled (fully or partly).
	FilledOpens int
	// OpenedPositions counts the positions cactusTrade opened that day; ClosedSameDay how
	// many of them were closed within the same risk day.
	OpenedPositions int
	ClosedSameDay   int
}

// Qualifies reports whether the day counts as a demo trading day (OD19, strict): at least
// one decision, at least one filled demo open, at least one position opened, and every
// position opened that day closed within it.
func (f DayFacts) Qualifies() bool {
	return f.Decisions > 0 && f.FilledOpens > 0 && f.OpenedPositions > 0 && f.ClosedSameDay == f.OpenedPositions
}

// FoldDayFacts builds one DayFacts per risk day from the demo account's decision days, its
// orders and the positions cactusTrade opened there. Only open orders in demo mode that were
// sent (Sends ≥ 1) and reached filled or partially_filled count, on the risk day they were
// sent. Days come out sorted.
func FoldDayFacts(decisionDays []RiskDate, orders []Order, positions []AgentPosition) []DayFacts {
	m := map[RiskDate]*DayFacts{}
	get := func(d RiskDate) *DayFacts {
		f, ok := m[d]
		if !ok {
			f = &DayFacts{Day: d}
			m[d] = f
		}
		return f
	}
	for _, d := range decisionDays {
		get(d).Decisions++
	}
	for _, o := range orders {
		if o.Kind != OrderOpen || o.Mode != ModeDemo || o.Sends < 1 || (o.State != OrderFilled && o.State != OrderPartial) {
			continue
		}
		at := o.SentAt
		if at.IsZero() {
			at = o.CreatedAt
		}
		get(RiskDay(at)).FilledOpens++
	}
	for _, p := range positions {
		day := RiskDay(p.OpenedAt)
		f := get(day)
		f.OpenedPositions++
		if !p.ClosedAt.IsZero() && RiskDay(p.ClosedAt) == day {
			f.ClosedSameDay++
		}
	}
	out := make([]DayFacts, 0, len(m))
	for _, f := range m {
		out = append(out, *f)
	}
	slices.SortFunc(out, func(a, b DayFacts) int { return strings.Compare(string(a.Day), string(b.Day)) })
	return out
}

// GateEvidence is the qualifying demo days found in the window, oldest first.
type GateEvidence struct {
	Days  []RiskDate
	Count int
	First RiskDate // "" when there are none
	Last  RiskDate
}

// parseRiskDate reads a RiskDate as the UTC midnight of its date.
func parseRiskDate(d RiskDate) (time.Time, bool) {
	t, err := time.Parse("2006-01-02", string(d))
	return t, err == nil
}

// daysBetween is b − a in whole days.
func daysBetween(a, b time.Time) int { return int(b.Sub(a) / (24 * time.Hour)) }

// QualifyingDays returns the qualifying days (DayFacts.Qualifies) among the WindowDays
// completed risk days before today. Today's risk day never counts (it is not over: a
// position may still be opened and held). Facts for the same day are merged by adding
// them up first (stricter: a held position on any of them disqualifies the day); facts with
// an unreadable date are ignored.
func QualifyingDays(facts []DayFacts, today RiskDate, rule EvidenceRule) GateEvidence {
	td, ok := parseRiskDate(today)
	if !ok {
		return GateEvidence{}
	}
	merged := map[RiskDate]DayFacts{}
	for _, f := range facts {
		d, ok := parseRiskDate(f.Day)
		if !ok || f.Day != RiskDate(d.Format("2006-01-02")) {
			continue
		}
		if age := daysBetween(d, td); age < 1 || age > rule.WindowDays {
			continue
		}
		m := merged[f.Day]
		m.Day = f.Day
		m.Decisions += f.Decisions
		m.FilledOpens += f.FilledOpens
		m.OpenedPositions += f.OpenedPositions
		m.ClosedSameDay += f.ClosedSameDay
		merged[f.Day] = m
	}
	var ev GateEvidence
	for d, f := range merged {
		if f.Qualifies() {
			ev.Days = append(ev.Days, d)
		}
	}
	slices.Sort(ev.Days)
	ev.Count = len(ev.Days)
	if ev.Count > 0 {
		ev.First, ev.Last = ev.Days[0], ev.Days[ev.Count-1]
	}
	return ev
}

// Sufficient says whether the evidence meets the rule on day today, and if not, why.
func (e GateEvidence) Sufficient(today RiskDate, rule EvidenceRule) (bool, string) {
	if e.Count < rule.MinDays {
		return false, fmt.Sprintf("%d qualifying demo trading days in the last %d risk days; %d needed", e.Count, rule.WindowDays, rule.MinDays)
	}
	td, ok1 := parseRiskDate(today)
	last, ok2 := parseRiskDate(e.Last)
	if !ok1 || !ok2 {
		return false, "the dates of the evidence cannot be read"
	}
	if age := daysBetween(last, td); age > rule.MaxAgeDays {
		return false, fmt.Sprintf("the latest qualifying demo day (%s) is %d days old; at most %d allowed", e.Last, age, rule.MaxAgeDays)
	}
	return true, fmt.Sprintf("%d qualifying demo trading days in the last %d risk days (%s to %s)", e.Count, rule.WindowDays, e.First, e.Last)
}

// GateUser is the user who asks to arm.
type GateUser struct {
	ID      string
	Role    Role
	Session bool // a web session (never an API key, OD22)
}

// GateInput is everything the arming checklist depends on. All of it is passed in.
type GateInput struct {
	Real       Account
	RealKeys   bool // both eToro keys of the real account are set
	Reconciled bool
	Killed     bool // a read error must be passed as killed (fail closed)
	Agent      AgentState
	Limits     RiskLimits
	// Demo is the linked demo account; nil when none was chosen or it cannot be found.
	Demo     *Account
	Evidence GateEvidence
	Today    RiskDate
	Rule     EvidenceRule
	User     GateUser
	Confirm  string // the phrase typed
}

// Gate check names, in checklist order.
const (
	CheckRealAccount  = "real_account"
	CheckRealKeys     = "real_keys"
	CheckReconciled   = "real_reconciled"
	CheckNotKilled    = "real_not_killed"
	CheckAgentStopped = "real_agent_stopped"
	CheckRealLimits   = "real_limits"
	CheckDemoAccount  = "demo_account"
	CheckEvidence     = "demo_evidence"
	CheckArmingUser   = "arming_user"
	CheckPhrase       = "confirm_phrase"
)

// GateCheck is one line of the arming checklist.
type GateCheck struct {
	Name   string
	Pass   bool
	Detail string
}

// ArmPhrase is what an admin types to arm real trading on an account (OD22).
func ArmPhrase(accountName string) string { return "ENABLE REAL TRADING " + accountName }

// GateChecks is the arming checklist (PLAN 7.10): the real account (eToro, real mode, keys,
// reconciled, not killed, agent stopped, limits valid within RealCeilings), the linked demo
// account (eToro, demo, same owner, OD21), the demo evidence (OD19, OD20), the arming user
// (admin, owner, web session, OD22) and the typed phrase.
func GateChecks(in GateInput) []GateCheck {
	c := func(name string, pass bool, ok, bad string) GateCheck {
		if pass {
			return GateCheck{Name: name, Pass: true, Detail: ok}
		}
		return GateCheck{Name: name, Detail: bad}
	}
	var out []GateCheck
	out = append(out,
		c(CheckRealAccount, in.Real.Tradable() && in.Real.Mode == ModeReal, "an eToro real-money account", "not an eToro real-money account"),
		c(CheckRealKeys, in.RealKeys, "its eToro keys are set", "its eToro keys are not set"),
		c(CheckReconciled, in.Reconciled, "reconciled with eToro", "not reconciled with eToro"),
		c(CheckNotKilled, !in.Killed, "the kill switch is off", "the kill switch is on (or cannot be read)"),
		c(CheckAgentStopped, in.Agent == AgentStopped, "the agent is stopped", fmt.Sprintf("the agent is %s: stop it first", in.Agent)),
	)
	if err := in.Limits.ValidateFor(ModeReal); err != nil {
		out = append(out, GateCheck{Name: CheckRealLimits, Detail: "the limits are not within the real-money ceilings: " + err.Error()})
	} else {
		out = append(out, GateCheck{Name: CheckRealLimits, Pass: true, Detail: "the limits are within the real-money ceilings"})
	}
	switch d := in.Demo; {
	case d == nil:
		out = append(out, GateCheck{Name: CheckDemoAccount, Detail: "choose the demo account whose history is the evidence"})
	case !d.Tradable() || d.Mode != ModeDemo:
		out = append(out, GateCheck{Name: CheckDemoAccount, Detail: d.Name + " is not an eToro demo account"})
	case d.ID == in.Real.ID || d.OwnerID != in.Real.OwnerID:
		out = append(out, GateCheck{Name: CheckDemoAccount, Detail: d.Name + " does not belong to the real account's owner"})
	default:
		out = append(out, GateCheck{Name: CheckDemoAccount, Pass: true, Detail: d.Name + ", same owner"})
	}
	if in.Demo == nil {
		out = append(out, GateCheck{Name: CheckEvidence, Detail: "no demo account chosen"})
	} else {
		ok, why := in.Evidence.Sufficient(in.Today, in.Rule)
		out = append(out, GateCheck{Name: CheckEvidence, Pass: ok, Detail: why})
	}
	u := in.User
	switch {
	case u.Role != RoleAdmin:
		out = append(out, GateCheck{Name: CheckArmingUser, Detail: "only an admin can arm real trading"})
	case u.ID == "" || u.ID != in.Real.OwnerID:
		out = append(out, GateCheck{Name: CheckArmingUser, Detail: "only the account's owner can arm real trading"})
	case !u.Session:
		out = append(out, GateCheck{Name: CheckArmingUser, Detail: "arming needs a web session, never an API key"})
	default:
		out = append(out, GateCheck{Name: CheckArmingUser, Pass: true, Detail: "an admin who owns the account, in a web session"})
	}
	out = append(out, c(CheckPhrase, strings.TrimSpace(in.Confirm) == ArmPhrase(in.Real.Name),
		"the phrase was typed", fmt.Sprintf("type %q", ArmPhrase(in.Real.Name))))
	return out
}

// GateRefusal is an arming refused by failing checks. It matches ErrRealModeGated (arming
// is refused) and ErrConflict (the account's state does not allow it).
type GateRefusal struct {
	Failed []GateCheck
}

func (e *GateRefusal) Error() string {
	names := make([]string, 0, len(e.Failed))
	for _, c := range e.Failed {
		names = append(names, c.Name+" ("+c.Detail+")")
	}
	return "real trading cannot be armed: " + strings.Join(names, "; ")
}

// Unwrap makes the refusal match ErrRealModeGated and ErrConflict.
func (e *GateRefusal) Unwrap() []error { return []error{ErrRealModeGated, ErrConflict} }

// ErrRealTradingDisabled means every check passed but this build cannot arm real trading
// (RealTradingEnabled is false until the owner's written consent, PLAN 7.18). It wraps
// ErrRealModeGated.
var ErrRealTradingDisabled = fmt.Errorf("real trading is not enabled in this build: %w", ErrRealModeGated)

// CanArm says whether the gate may be armed: every check passes and this build allows real
// trading. A failing check gives a *GateRefusal; all passing gives ErrRealTradingDisabled
// while RealTradingEnabled is false. Both match ErrRealModeGated.
func CanArm(checks []GateCheck) error {
	var failed []GateCheck
	for _, c := range checks {
		if !c.Pass {
			failed = append(failed, c)
		}
	}
	if len(failed) > 0 || len(checks) == 0 {
		return &GateRefusal{Failed: failed}
	}
	if !RealTradingEnabled {
		return ErrRealTradingDisabled
	}
	return nil
}

// GateState is whether a real account's gate is armed.
type GateState string

// Gate states. A gate never stored reads as locked.
const (
	GateLocked GateState = "locked"
	GateArmed  GateState = "armed"
)

// RealGateTTL is how long an armed gate lasts before it disarms by itself (OD24).
const RealGateTTL = 30 * 24 * time.Hour

// Automatic disarm reasons (OD24); a manual disarm carries the user's reason.
const (
	DisarmKill    = "the kill switch tripped"
	DisarmKeys    = "the account's eToro keys changed"
	DisarmLimits  = "a risk limit was raised"
	DisarmRestore = "a backup was restored after the gate was armed"
	DisarmExpired = "the arming expired (30 days)"
	// DisarmRestored is recorded by the restore itself, which locks every gate whatever the
	// clocks say (deploy/backup/mariadb-restore.sh writes the same text).
	DisarmRestored = "a backup was restored"
)

// SettingRestoreAt is the settings key holding the time of the latest restore of the
// database (RFC 3339, UTC; PLAN 7.6a). A gate armed before it is no longer in force.
const SettingRestoreAt = "restore.at"

// MaxDisarmReason bounds a stored disarm reason.
const MaxDisarmReason = 500

// RealGate is a real-money account's gate (PLAN 7.10): locked, or armed by an admin with the
// evidence of a linked demo account, until it expires or is disarmed. Version is the stored
// version (0 = never stored): a stale write fails, so a disarm is never overwritten by an
// older arm.
type RealGate struct {
	AccountID     string
	State         GateState
	DemoAccountID string
	ArmedAt       time.Time
	ArmedBy       string // user id
	ExpiresAt     time.Time
	Evidence      GateEvidence
	DisarmedAt    time.Time
	DisarmedBy    string // user id, or "" for the system
	DisarmReason  string
	Version       int64
	UpdatedAt     time.Time
}

// LockedGate is the gate of an account that was never armed.
func LockedGate(accountID string) RealGate { return RealGate{AccountID: accountID, State: GateLocked} }

// Validate checks a gate before it is stored.
func (g RealGate) Validate() error {
	switch {
	case g.AccountID == "":
		return Invalid("accountId", "required")
	case g.State != GateLocked && g.State != GateArmed:
		return Invalid("state", "locked or armed")
	case g.State == GateArmed && (g.DemoAccountID == "" || g.ArmedBy == "" || g.ArmedAt.IsZero() || !g.ExpiresAt.After(g.ArmedAt)):
		return Invalid("state", "an armed gate needs its demo account, who armed it, when, and a later expiry")
	case len(g.DisarmReason) > MaxDisarmReason:
		return Invalid("reason", fmt.Sprintf("at most %d bytes", MaxDisarmReason))
	}
	return nil
}

// Live reports whether the gate is armed and in force at now: stored armed, not expired, and
// armed after the latest restore (restoredAt; zero = never restored). When it is stored
// armed but no longer in force, why is the disarm reason to record.
func (g RealGate) Live(now, restoredAt time.Time) (armed bool, why string) {
	switch {
	case g.State != GateArmed:
		return false, ""
	case !now.Before(g.ExpiresAt):
		return false, DisarmExpired
	case !restoredAt.IsZero() && !restoredAt.Before(g.ArmedAt):
		return false, DisarmRestore
	}
	return true, ""
}

// Arm returns the gate armed at now by user by, linked to the demo account, with the
// evidence; it expires after RealGateTTL. The disarm fields are cleared.
func (g RealGate) Arm(demoAccountID, by string, ev GateEvidence, now time.Time) RealGate {
	g.State, g.DemoAccountID, g.ArmedBy, g.ArmedAt, g.ExpiresAt = GateArmed, demoAccountID, by, now, now.Add(RealGateTTL)
	g.Evidence = ev
	g.DisarmedAt, g.DisarmedBy, g.DisarmReason = time.Time{}, "", ""
	g.UpdatedAt = now
	return g
}

// Disarm returns the gate locked at now by user by ("" = the system) for reason (cut to
// MaxDisarmReason bytes). The arming record (who, when, evidence) is kept for the audit.
func (g RealGate) Disarm(by, reason string, now time.Time) RealGate {
	g.State, g.DisarmedAt, g.DisarmedBy = GateLocked, now, by
	g.DisarmReason = clipUTF8(reason, MaxDisarmReason)
	g.UpdatedAt = now
	return g
}

// clipUTF8 cuts s to at most n bytes on a rune boundary.
func clipUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
