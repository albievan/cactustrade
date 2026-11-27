package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// The real-mode gate (PLAN 7.13, lifted by 7.19–7.23 with the owner's written consent, D39):
// its status, arming (every check of domain.GateChecks, then CanArm), disarming and the
// automatic disarms (OD24). An armed gate is only ever stored by armLocked, which ArmReal
// reaches only once CanArm allows it. An armed gate alone trades nothing: every real entry
// also needs the deployment opt-in (OD28) and a fresh permit (realEntryPermit).

// RealGateView is a real-money account's gate as the Real trading page shows it.
type RealGateView struct {
	Account domain.AccountView
	// Gate is the stored gate (a never-armed account's reads locked); Armed says whether it
	// is in force now (stored armed, not expired, armed after the latest restore and not
	// disarmed since in this process).
	Gate  domain.RealGate
	Armed bool
	// DemoAccountID is the demo account the checks and the evidence are for ("" = none);
	// DemoCandidates the eToro demo accounts of the same owner the caller can see (OD21).
	DemoAccountID  string
	DemoCandidates []domain.AccountView
	// Checks is the arming checklist without the typed phrase (shown as Phrase).
	Checks   []domain.GateCheck
	Evidence domain.GateEvidence
	Rule     domain.EvidenceRule
	Phrase   string
	// Enabled is domain.RealTradingEnabled: whether this build may arm real trading at all
	// (true since D39).
	Enabled bool
	// CanArm: the caller may submit an arm request (admin, owner, web session); the server
	// still runs every check then. CanDisarm: the caller may disarm (trade access, kill
	// permission; a web session or an API key, OD23).
	CanArm    bool
	CanDisarm bool
	// CanSetDays: the caller may change how many qualifying demo days arming needs (D36: an
	// admin who owns the account, in a web session).
	CanSetDays bool
}

// gateLatch remembers, per account, the latest disarm asked for in this process: a gate
// armed before it is not in force even if the disarm could not be stored (fail closed, as
// the kill latch, review m3).
type gateLatch struct {
	mu sync.Mutex
	at map[string]time.Time
}

func newGateLatch() *gateLatch { return &gateLatch{at: map[string]time.Time{}} }

func (l *gateLatch) latch(accountID string, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if at.After(l.at[accountID]) {
		l.at[accountID] = at
	}
}

func (l *gateLatch) since(accountID string) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.at[accountID]
}

// isRealAccount reports whether the account is an eToro real-money account (the only kind
// with a gate).
func isRealAccount(a domain.Account) bool { return a.Tradable() && a.Mode == domain.ModeReal }

// restoredAt is the time of the latest restore (zero = never). An unreadable value is
// returned as an error: the caller treats every gate as not in force (fail closed).
func (s *Service) restoredAt(ctx context.Context) (time.Time, error) {
	v, err := s.d.Store.Settings().Get(ctx, domain.SettingRestoreAt)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return time.Time{}, nil
	case err != nil:
		return time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("the restore time %q is unreadable: %w", v, err)
	}
	return t, nil
}

// storedGate is the account's stored gate, locked when there is none.
func (s *Service) storedGate(ctx context.Context, accountID string) (domain.RealGate, error) {
	g, err := s.d.Store.RealGates().Get(ctx, accountID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.LockedGate(accountID), nil
	}
	return g, err
}

// gateLive says whether the gate is in force now and, when it is stored armed but not in
// force, why (the reason to record when it is disarmed). Any read error is "not in force".
func (s *Service) gateLive(ctx context.Context, g domain.RealGate) (bool, string) {
	if g.State != domain.GateArmed {
		return false, ""
	}
	restored, err := s.restoredAt(ctx)
	if err != nil {
		s.d.Log.WarnContext(ctx, "real gate: the restore time cannot be read; treating the gate as disarmed", "err", err)
		return false, DisarmUnreadableRestore
	}
	live, why := g.Live(s.d.Clock.Now().UTC(), restored)
	if live && !s.gates.since(g.AccountID).Before(g.ArmedAt) {
		return false, "disarmed in this process"
	}
	return live, why
}

// DisarmUnreadableRestore is the reason recorded when the restore time cannot be read.
const DisarmUnreadableRestore = "the time of the latest restore cannot be read"

// realGateState reports whether the account's gate is in force now (read only: nothing is
// stored). Non-real accounts are not armed. A read error (the gate or the restore time) is
// returned: the state is then unknown, which the banner shows as such (never as "not armed").
func (s *Service) realGateState(ctx context.Context, a domain.Account) (bool, error) {
	if !isRealAccount(a) {
		return false, nil
	}
	g, err := s.storedGate(ctx, a.ID)
	if err != nil {
		return false, err
	}
	if g.State != domain.GateArmed {
		return false, nil
	}
	if _, err := s.d.Store.Settings().Get(ctx, domain.SettingRestoreAt); err != nil && !errors.Is(err, domain.ErrNotFound) {
		return false, err
	}
	live, _ := s.gateLive(ctx, g)
	return live, nil
}

// realEntryBlocked says why no real-money entry may be made on a now ("" = it may, as far as
// the gate goes; PLAN 7.22): the deployment opt-in (OD28), this build (RealTradingEnabled),
// the gate armed and in force (not expired, not older than the latest restore, not disarmed
// in this process), the account reconciled and its limits within the real ceilings (OD25).
// Any read error blocks (fail closed).
func (s *Service) realEntryBlocked(ctx context.Context, a domain.Account) string {
	switch {
	case !isRealAccount(a):
		return a.Name + " is not an eToro real-money account"
	case !s.cfg.RealTrading:
		return "real trading is off in this deployment (CACTUSTRADE_REAL_TRADING is not on)"
	case !domain.RealTradingEnabled:
		return "real trading is not enabled in this build"
	}
	if _, ok := s.consentFor(a.ID); !ok {
		return "the owner's written consent does not cover " + a.Name
	}
	g, err := s.storedGate(ctx, a.ID)
	if err != nil {
		return "the real-trading gate cannot be read"
	}
	if live, _ := s.gateLive(ctx, g); !live {
		return "real trading is not armed on " + a.Name
	}
	if rc := s.Reconciled(a.ID); !rc.OK {
		return "the account is not reconciled: " + rc.Reason
	}
	if err := s.riskLimitsFor(ctx, a).ValidateFor(domain.ModeReal); err != nil {
		return "the limits are not within the real-money ceilings: " + err.Error()
	}
	return ""
}

// realEntryPermit builds the permit for one entry on the real-money account a, issued at
// now (domain.PermitEntry, good for domain.RealPermitTTL): the caller passes the time it
// will assess or stamp the entry with, so the permit is never issued after it (review M1 of
// the lift: a permit issued later than the decision's time is refused by AllowsEntry).
// realEntryBlocked must say nothing, and the gate is read again for the permit itself.
// Refusals wrap domain.ErrNoRealPermit.
func (s *Service) realEntryPermit(ctx context.Context, a domain.Account, now time.Time) (domain.RealPermit, error) {
	if why := s.realEntryBlocked(ctx, a); why != "" {
		return domain.RealPermit{}, fmt.Errorf("%s: %w", why, domain.ErrNoRealPermit)
	}
	if !s.rt.realStartedHere(a.ID) {
		return domain.RealPermit{}, fmt.Errorf("the agent of %s was not started by a person since this process started (OD26): %w", a.Name, domain.ErrNoRealPermit)
	}
	g, err := s.storedGate(ctx, a.ID)
	if err != nil {
		return domain.RealPermit{}, fmt.Errorf("reading the gate: %w: %w", err, domain.ErrNoRealPermit)
	}
	restored, err := s.restoredAt(ctx)
	if err != nil {
		return domain.RealPermit{}, fmt.Errorf("reading the restore time: %w: %w", err, domain.ErrNoRealPermit)
	}
	now = now.UTC()
	if !s.gates.since(a.ID).Before(g.ArmedAt) { // disarmed in this process after it was armed
		return domain.RealPermit{}, fmt.Errorf("real trading was disarmed on %s: %w", a.Name, domain.ErrNoRealPermit)
	}
	return domain.PermitEntry(g, a, now, restored)
}

// AnnounceRealTrading records, at start-up, that this deployment allows real-money entries
// (OD28: CACTUSTRADE_REAL_TRADING=on): an audit entry and a danger system event everyone sees
// (and an alert e-mail by the usual rules). With the opt-in off it does nothing.
func (s *Service) AnnounceRealTrading(ctx context.Context) {
	if !s.cfg.RealTrading {
		return
	}
	s.audit(ctx, Principal{}, "system.real_trading_on", "", "CACTUSTRADE_REAL_TRADING=on at start-up")
	s.emit(ctx, "", "system.real_trading_on", domain.SeverityDanger, "REAL MONEY: this deployment allows real-money trading",
		"CACTUSTRADE_REAL_TRADING=on. An entry on a real-money account still needs its real-trading gate armed (Real trading page), "+
			"a reconciliation and a fresh permit; exits work whatever the gate. Disarm a gate, or restart without the variable, to stop real entries.")
}

// RealArmedAccount names an account whose real trading is armed (the banner, OD27).
type RealArmedAccount struct {
	ID, Name string
}

// realArmed lists the accounts in vs whose gate is in force now; unknown reports that at
// least one real account's gate could not be read.
func (s *Service) realArmed(ctx context.Context, vs []domain.AccountView) (out []RealArmedAccount, unknown bool) {
	for _, v := range vs {
		armed, err := s.realGateState(ctx, v.Account)
		if err != nil {
			s.d.Log.WarnContext(ctx, "real gate: the state cannot be read", "account", v.ID, "err", err)
			unknown = true
			continue
		}
		if armed {
			out = append(out, RealArmedAccount{ID: v.ID, Name: v.Name})
		}
	}
	return out, unknown
}

// realAccount resolves a real-money account for the gate use cases.
func (s *Service) realAccount(ctx context.Context, p Principal, accountID string, need domain.AccountAccess) (domain.AccountView, error) {
	if accountID == "" {
		return domain.AccountView{}, domain.Invalid("accountId", "required")
	}
	v, err := s.account(ctx, p, accountID, need)
	if err != nil {
		return domain.AccountView{}, err
	}
	if !isRealAccount(v.Account) {
		return domain.AccountView{}, domain.Invalid("accountId", v.Name+" is not a real-money account: only those have a real-trading gate")
	}
	return v, nil
}

// RealGateStatus is a real-money account's gate, its arming checklist and the demo evidence
// (view access). demoAccountID chooses the demo account the checklist is for ("" = the one
// the gate was last armed with, else the first of the owner's demo accounts the caller can
// see). It only reads; a gate found expired or older than a restore is disarmed.
func (s *Service) RealGateStatus(ctx context.Context, p Principal, accountID, demoAccountID string) (RealGateView, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return RealGateView{}, err
	}
	v, err := s.realAccount(ctx, p, accountID, domain.AccessView)
	if err != nil {
		return RealGateView{}, err
	}
	g, err := s.storedGate(ctx, v.ID)
	if err != nil {
		return RealGateView{}, fmt.Errorf("app.RealGateStatus: %w", err)
	}
	out := RealGateView{Account: v, Rule: s.evidenceRule(ctx, v.ID), Phrase: domain.ArmPhrase(v.Name), Enabled: domain.RealTradingEnabled}
	var why string
	out.Armed, why = s.gateLive(ctx, g)
	if g.State == domain.GateArmed && !out.Armed {
		g = s.expireGate(ctx, v.Account, why, g)
	}
	out.Gate = g
	vs, err := s.Accounts(ctx, p)
	if err != nil {
		return RealGateView{}, err
	}
	for _, c := range vs {
		if c.Tradable() && c.Mode == domain.ModeDemo && c.OwnerID == v.OwnerID {
			out.DemoCandidates = append(out.DemoCandidates, c)
		}
	}
	pick := func(id string) *domain.Account {
		i := slices.IndexFunc(out.DemoCandidates, func(c domain.AccountView) bool { return c.ID == id })
		if i < 0 {
			return nil
		}
		return &out.DemoCandidates[i].Account
	}
	var demo *domain.Account
	switch {
	case demoAccountID != "":
		demo = pick(demoAccountID)
	case pick(g.DemoAccountID) != nil:
		demo = pick(g.DemoAccountID)
	case len(out.DemoCandidates) > 0:
		demo = &out.DemoCandidates[0].Account
	}
	in, err := s.gateInput(ctx, p, v.Account, demo, "")
	if err != nil {
		return RealGateView{}, fmt.Errorf("app.RealGateStatus: %w", err)
	}
	if demo != nil {
		out.DemoAccountID = demo.ID
	}
	out.Evidence = in.Evidence
	for _, c := range domain.GateChecks(in) {
		if c.Name == domain.CheckPhrase {
			continue
		}
		out.Checks = append(out.Checks, c)
		if c.Name == domain.CheckArmingUser {
			out.CanArm, out.CanSetDays = c.Pass, c.Pass
		}
	}
	out.CanDisarm = p.Can(domain.PermKillSwitch) && v.Access.AtLeast(domain.AccessTrade)
	return out, nil
}

// gateInput gathers the checklist's input for the real account a, the demo account (nil =
// none) and the caller.
func (s *Service) gateInput(ctx context.Context, p Principal, a domain.Account, demo *domain.Account, confirm string) (domain.GateInput, error) {
	now := s.d.Clock.Now().UTC()
	killed, kerr := s.isKilled(ctx, a.ID)
	in := domain.GateInput{
		Real: a, RealKeys: s.hasKeys(ctx, a), Reconciled: s.Reconciled(a.ID).OK, Killed: killed || kerr != nil,
		Agent: s.agentState(ctx, a.ID), Limits: s.riskLimitsFor(ctx, a), Demo: demo,
		Today: domain.RiskDay(now), Rule: s.evidenceRule(ctx, a.ID),
		User: domain.GateUser{ID: p.UserID, Role: p.Role, Session: p.Via == ViaSession}, Confirm: confirm,
	}
	in.Consent, _ = s.consentFor(a.ID)
	if demo != nil {
		ev, err := s.demoEvidence(ctx, demo.ID, now, in.Rule)
		if err != nil {
			return domain.GateInput{}, err
		}
		in.Evidence = ev
	}
	return in, nil
}

// demoEvidence folds the demo account's last WindowDays completed risk days into the
// qualifying days (OD19, OD20): one decision per day (read a day at a time, newest first),
// the orders and the agent positions of the window.
func (s *Service) demoEvidence(ctx context.Context, demoID string, now time.Time, rule domain.EvidenceRule) (domain.GateEvidence, error) {
	todayStart := domain.RiskDayStart(now)
	since := todayStart.AddDate(0, 0, -rule.WindowDays)
	var days []domain.RiskDate
	before := todayStart // today's risk day never counts
	for range rule.WindowDays + 1 {
		ds, err := s.d.Store.Decisions().List(ctx, demoID, before, "", 1)
		if err != nil {
			return domain.GateEvidence{}, fmt.Errorf("reading the demo decisions: %w", err)
		}
		if len(ds) == 0 || ds[0].At.Before(since) {
			break
		}
		days = append(days, domain.RiskDay(ds[0].At))
		before = domain.RiskDayStart(ds[0].At)
	}
	orders, err := s.d.Store.Orders().ListSince(ctx, demoID, since)
	if err != nil {
		return domain.GateEvidence{}, fmt.Errorf("reading the demo orders: %w", err)
	}
	positions, err := s.d.Store.AgentPositions().ListSince(ctx, demoID, since)
	if err != nil {
		return domain.GateEvidence{}, fmt.Errorf("reading the demo positions: %w", err)
	}
	return domain.QualifyingDays(domain.FoldDayFacts(days, orders, positions), domain.RiskDay(now), rule), nil
}

// ArmReal asks to arm real trading on a real-money account (PLAN 7.13): the mode.switch
// permission (admin), a web session, the account's owner, the caller's password (checked by
// the bounded hasher; the HTTP adapter counts it with the sign-in limiter), the typed phrase
// "ENABLE REAL TRADING <account name>" (422 when wrong), the checklist, then CanArm (a
// refusal is domain.ErrRealModeGated: a *domain.GateRefusal naming the failing checks).
// Every refusal after the password is audited as real.arm_refused with the failing checks;
// when every check passes the gate is stored armed (armLocked).
func (s *Service) ArmReal(ctx context.Context, p Principal, accountID, demoAccountID, confirm, password string) (domain.RealGate, error) {
	if err := s.authorize(p, domain.PermSwitchMode); err != nil {
		return domain.RealGate{}, err
	}
	if p.Via != ViaSession {
		return domain.RealGate{}, fmt.Errorf("arming real trading needs a web session, not an API key: %w", domain.ErrForbidden)
	}
	v, err := s.realAccount(ctx, p, accountID, domain.AccessOwner)
	if err != nil {
		return domain.RealGate{}, err
	}
	if err := s.checkPassword(ctx, p, password, v.ID, "real.arm_refused"); err != nil {
		return domain.RealGate{}, err
	}
	if strings.TrimSpace(confirm) != domain.ArmPhrase(v.Name) {
		s.audit(ctx, p, "real.arm_refused", v.ID, "wrong phrase")
		return domain.RealGate{}, domain.Invalid("confirm", fmt.Sprintf("type %q to confirm", domain.ArmPhrase(v.Name)))
	}
	var demo *domain.Account
	if vs, err := s.Accounts(ctx, p); err != nil {
		return domain.RealGate{}, err
	} else if i := slices.IndexFunc(vs, func(x domain.AccountView) bool { return x.ID == demoAccountID }); i >= 0 {
		demo = &vs[i].Account
	}
	in, err := s.gateInput(ctx, p, v.Account, demo, confirm)
	if err != nil {
		return domain.RealGate{}, fmt.Errorf("app.ArmReal: %w", err)
	}
	if err := domain.CanArm(domain.GateChecks(in)); err != nil {
		detail := "real trading is not enabled in this build"
		if gr, ok := errors.AsType[*domain.GateRefusal](err); ok {
			names := make([]string, 0, len(gr.Failed))
			for _, c := range gr.Failed {
				names = append(names, c.Name)
			}
			detail = "failed checks: " + strings.Join(names, ", ")
		}
		s.audit(ctx, p, "real.arm_refused", v.ID, detail)
		return domain.RealGate{}, fmt.Errorf("app.ArmReal: %w", err)
	}
	if demo == nil { // CanArm needs the demo check to pass: never reached
		return domain.RealGate{}, fmt.Errorf("app.ArmReal: no demo account: %w", domain.ErrRealModeGated)
	}
	locks := s.rt.agent(v.ID)
	locks.ctl.Lock()
	defer locks.ctl.Unlock()
	return s.armLocked(ctx, p, v.Account, demo.ID, in.Evidence)
}

// armLocked stores the account's gate armed with the evidence of the demo account, audits
// real.arm, raises a danger event and e-mails the admins and the owner. The caller holds the
// account's control lock and has passed CanArm: ArmReal is its only caller outside tests.
func (s *Service) armLocked(ctx context.Context, p Principal, a domain.Account, demoID string, ev domain.GateEvidence) (domain.RealGate, error) {
	now := s.d.Clock.Now().UTC()
	var g domain.RealGate
	err := s.putGate(ctx, a.ID, func(cur domain.RealGate) (domain.RealGate, bool) {
		return cur.Arm(demoID, p.UserID, ev, now), true
	}, &g)
	if err != nil {
		return domain.RealGate{}, fmt.Errorf("app.armLocked: %w", err)
	}
	detail := fmt.Sprintf("linked demo account %s, %d qualifying days (%s to %s), expires %s",
		demoID, ev.Count, ev.First, ev.Last, g.ExpiresAt.Format(time.RFC3339))
	s.audit(ctx, p, "real.arm", a.ID, detail)
	title := "REAL MONEY: real trading armed on " + a.Name
	body := "Real trading was armed by " + p.Username + ": " + detail + ". Disarm it at any time on the Real trading page."
	s.emitFor(ctx, a.ID, p.Username, "real.armed", domain.SeverityDanger, title, body)
	s.mailGateChange(ctx, a, p.Username, title, body)
	return g, nil
}

// putGate applies change to the account's stored gate and stores it, re-reading and
// re-applying on a version conflict (at most 3 times). change returns false to store
// nothing (out is then the stored gate).
func (s *Service) putGate(ctx context.Context, accountID string, change func(domain.RealGate) (domain.RealGate, bool), out *domain.RealGate) error {
	for range 3 {
		cur, err := s.storedGate(ctx, accountID)
		if err != nil {
			return err
		}
		next, ok := change(cur)
		if !ok {
			*out = cur
			return nil
		}
		stored, err := s.d.Store.RealGates().Put(ctx, next)
		if errors.Is(err, domain.ErrConflict) {
			continue
		}
		if err != nil {
			return err
		}
		*out = stored
		return nil
	}
	return fmt.Errorf("the gate kept changing: %w", domain.ErrConflict)
}

// maxDisarmReasonInput bounds the reason a user types when disarming.
const maxDisarmReasonInput = 200

// DisarmReal disarms real trading on a real-money account now (OD23): trade access and the
// agent.kill permission, from a web session or an API key. Idempotent (a locked gate stays
// locked, 200). The agent goes to stopped when running or paused (entries stop); nothing is
// cancelled or closed (exits continue, a close in flight is never touched). Never rate
// limited (D32). Audited, with a danger event and an e-mail to the admins and the owner.
func (s *Service) DisarmReal(ctx context.Context, p Principal, accountID, reason string) (domain.RealGate, error) {
	if err := s.authorize(p, domain.PermKillSwitch); err != nil {
		return domain.RealGate{}, err
	}
	v, err := s.realAccount(ctx, p, accountID, domain.AccessTrade)
	if err != nil {
		return domain.RealGate{}, err
	}
	why := "disarmed by " + p.Username
	if r := strings.TrimSpace(reason); r != "" {
		why += ": " + truncate(r, maxDisarmReasonInput)
	}
	// Like the kill, a disarm runs to the end once authorized (a closed tab must not stop it).
	ctx = context.WithoutCancel(ctx)
	locks := s.rt.agent(v.ID)
	locks.ctl.Lock()
	defer locks.ctl.Unlock()
	g, err := s.disarmLocked(ctx, v.Account, p, why)
	if err != nil {
		return g, fmt.Errorf("app.DisarmReal: %w", err)
	}
	return g, nil
}

// disarmLocked disarms the account's gate for why and announces it at once (see
// disarmQuietly). The caller holds the account's control lock.
func (s *Service) disarmLocked(ctx context.Context, a domain.Account, p Principal, why string) (domain.RealGate, error) {
	g, announce, err := s.disarmQuietly(ctx, a, p, why)
	announce()
	return g, err
}

// disarmQuietly disarms the account's gate for why: latched in memory first (a gate armed
// before it is not in force even if the write fails), then stored if it was armed, then the
// agent is moved to stopped when running or paused. When the gate cannot be stored, a kill is
// stored as well (unless this process already holds one), so every process refuses entries,
// not only this one (review minor 3). Only a change is audited; its danger event and e-mails
// are left to announce, which the caller runs when the urgent work is done (a kill runs it
// after its flatten, review minor 4). announce is never nil. The caller holds the account's
// control lock. p is the user (Principal{} = the system).
func (s *Service) disarmQuietly(ctx context.Context, a domain.Account, p Principal, why string) (domain.RealGate, func(), error) {
	nothing := func() {}
	now := s.d.Clock.Now().UTC()
	s.gates.latch(a.ID, now)
	s.rt.cancelEntries(a.ID) // no entry pipeline keeps running on a gate being disarmed
	var g domain.RealGate
	changed := false
	err := s.putGate(ctx, a.ID, func(cur domain.RealGate) (domain.RealGate, bool) {
		if cur.State != domain.GateArmed {
			return cur, false
		}
		changed = true
		return cur.Disarm(p.UserID, why, now), true
	}, &g)
	if st := s.agentState(ctx, a.ID); st == domain.AgentRunning || st == domain.AgentPaused {
		if serr := s.setAgentState(ctx, a.ID, domain.AgentStopped); serr != nil {
			err = errors.Join(err, fmt.Errorf("stopping the agent: %w", serr))
		} else {
			s.audit(ctx, p, "agent.stop", a.ID, "from "+string(st)+": real trading disarmed")
		}
	}
	if err != nil {
		s.audit(ctx, p, "real.disarm_failed", a.ID, why)
		if !s.rt.killLatched(a.ID) { // a kill being tripped (tripLocked) is stored by it
			kwhy := "the real-trading disarm could not be stored (" + why + ")"
			already, kerr := s.storeKillLocked(ctx, a, kwhy)
			if kerr != nil {
				err = errors.Join(err, fmt.Errorf("storing the kill: %w", kerr))
			}
			if !already || kerr != nil {
				s.announceKill(ctx, a, p, kwhy)
			}
		}
		return domain.LockedGate(a.ID), nothing, err
	}
	if !changed {
		return g, nothing, nil
	}
	s.audit(ctx, p, "real.disarm", a.ID, why)
	return g, func() {
		title := "Real trading disarmed on " + a.Name
		body := why + ". New entries stop; positions due are still closed. Arming again needs the full checklist."
		s.emitFor(ctx, a.ID, p.Username, "real.disarmed", domain.SeverityDanger, title, body)
		s.mailGateChange(ctx, a, p.Username, title, body)
	}, nil
}

// disarmIfArmed is the automatic disarm (OD24) for a real-money account whose gate is armed
// (a key change, a raised limit), announced at once. The caller holds the account's control
// lock.
func (s *Service) disarmIfArmed(ctx context.Context, a domain.Account, p Principal, why string) {
	s.disarmIfArmedQuietly(ctx, a, p, why)()
}

// disarmIfArmedQuietly is disarmIfArmed leaving the announcement to the caller (a kill-switch
// trip announces after its flatten). Errors are logged (the in-memory latch, and the kill
// stored on a failed write, hold the disarm all the same). The result is never nil.
func (s *Service) disarmIfArmedQuietly(ctx context.Context, a domain.Account, p Principal, why string) func() {
	if !isRealAccount(a) {
		return func() {}
	}
	if g, err := s.storedGate(ctx, a.ID); err == nil && g.State != domain.GateArmed {
		return func() {}
	}
	_, announce, err := s.disarmQuietly(ctx, a, p, why)
	if err != nil {
		s.d.Log.ErrorContext(ctx, "real gate: the automatic disarm could not be stored", "account", a.ID, "reason", why, "err", err)
	}
	return announce
}

// expireGate disarms a gate found stored armed but no longer in force (expired, older than a
// restore, OD24) when the account's control lock is free now; otherwise the next sweep does
// (it is not in force meanwhile). It returns the gate as stored afterwards.
func (s *Service) expireGate(ctx context.Context, a domain.Account, why string, g domain.RealGate) domain.RealGate {
	locks := s.rt.agent(a.ID)
	if !locks.ctl.TryLock() {
		return g
	}
	defer locks.ctl.Unlock()
	out, err := s.disarmLocked(ctx, a, Principal{}, why)
	if err != nil {
		s.d.Log.ErrorContext(ctx, "real gate: the expired gate could not be disarmed", "account", a.ID, "reason", why, "err", err)
		return g
	}
	return out
}

// sweepTimeout bounds one sweep of the real gates.
const sweepTimeout = 30 * time.Second

// sweepRealGatesAside runs sweepRealGates in its own goroutine (in wg) with sweepTimeout, so
// a slow store never delays a supervisor pass's exits; a sweep still running is not started
// again.
func (s *Service) sweepRealGatesAside(ctx context.Context, wg *sync.WaitGroup, as []domain.Account) {
	if !s.rt.sweeping.CompareAndSwap(false, true) {
		return
	}
	wg.Go(func() {
		defer s.rt.sweeping.Store(false)
		sctx, cancel := context.WithTimeout(ctx, sweepTimeout)
		defer cancel()
		s.sweepRealGates(sctx, as)
	})
}

// sweepRealGates disarms every stored-armed gate that is no longer in force (expiry, a
// restore). The supervisor loop runs it on every tick (sweepRealGatesAside).
func (s *Service) sweepRealGates(ctx context.Context, as []domain.Account) {
	for _, a := range as {
		if !isRealAccount(a) {
			continue
		}
		g, err := s.storedGate(ctx, a.ID)
		if err != nil || g.State != domain.GateArmed {
			continue
		}
		if live, why := s.gateLive(ctx, g); !live {
			s.expireGate(ctx, a, why, g)
		}
	}
}

// mailGateChange e-mails an arming or disarming to every admin and to the account's owner
// who has an e-mail address, whatever their notification choices, unless the event's own
// alert e-mail (emitFor → notify) already went to them.
func (s *Service) mailGateChange(ctx context.Context, a domain.Account, actor, title, body string) {
	users, err := s.d.Store.Users().List(ctx)
	if err != nil {
		s.d.Log.WarnContext(ctx, "real gate: listing users for the e-mail failed", "err", err)
		return
	}
	notified := map[string]bool{a.OwnerID: true}
	if shares, err := s.d.Store.Accounts().Shares(ctx, a.ID); err == nil {
		for _, sh := range shares {
			notified[sh.UserID] = true
		}
	}
	if actor == "" {
		actor = "system"
	}
	for _, u := range users {
		if u.Disabled || u.Email == "" || (u.Role != domain.RoleAdmin && u.ID != a.OwnerID) {
			continue
		}
		if notified[u.ID] && s.notifyPrefs(ctx, u.ID)[domain.NotifyAlerts] {
			continue // the event's alert e-mail reached them
		}
		s.enqueue(ctx, u.Email, "[cactusTrade] "+title, title+"\n\n"+body+"\n\nWhen: "+
			s.d.Clock.Now().UTC().Format("Mon 2 Jan 2006 15:04 MST")+"\nBy: "+actor+
			"\n\nYou get this because you are an administrator or the account's owner.\n"+
			"cactusTrade is software, not financial advice — trading can lose money.")
	}
}

// demoDaysKey stores how many qualifying demo days a real account's arming needs (D36).
func demoDaysKey(accountID string) string { return "realgate.min_days." + accountID }

// evidenceRule is the real account's evidence rule: the default (OD20) with the admin's
// number of days when one is stored and valid (else the default, logged).
func (s *Service) evidenceRule(ctx context.Context, accountID string) domain.EvidenceRule {
	rule := domain.DefaultEvidenceRule
	v, err := s.d.Store.Settings().Get(ctx, demoDaysKey(accountID))
	if err != nil {
		return rule
	}
	n, err := strconv.Atoi(v)
	if err == nil {
		if r, err := rule.WithMinDays(n); err == nil {
			return r
		}
	}
	s.d.Log.ErrorContext(ctx, "stored demo days for the real gate are invalid; using the default", "account", accountID, "value", v)
	return rule
}

// SetDemoDaysRequired sets how many qualifying demo days arming the real account needs
// (D36): an admin who owns the account, in a web session; MinEvidenceDaysFloor up to the
// window. Audited with the old and new numbers and announced; an armed gate is disarmed (it
// was armed under the old rule).
func (s *Service) SetDemoDaysRequired(ctx context.Context, p Principal, accountID string, days int) (RealGateView, error) {
	v, err := s.realAccount(ctx, p, accountID, domain.AccessOwner)
	if err != nil {
		return RealGateView{}, err
	}
	switch {
	case p.Role != domain.RoleAdmin:
		return RealGateView{}, fmt.Errorf("only an admin can set the demo days real trading needs: %w", domain.ErrForbidden)
	case p.UserID == "" || p.UserID != v.OwnerID:
		return RealGateView{}, fmt.Errorf("only the account's owner can set the demo days real trading needs: %w", domain.ErrForbidden)
	case p.Via != ViaSession:
		return RealGateView{}, fmt.Errorf("the demo days real trading needs can only be set in the web UI: %w", domain.ErrForbidden)
	}
	old := s.evidenceRule(ctx, v.ID)
	next, err := old.WithMinDays(days)
	if err != nil {
		return RealGateView{}, err
	}
	if next.MinDays != old.MinDays {
		if err := s.d.Store.Settings().Set(ctx, demoDaysKey(v.ID), strconv.Itoa(days), s.d.Clock.Now().UTC()); err != nil {
			return RealGateView{}, fmt.Errorf("app.SetDemoDaysRequired: %w", err)
		}
		s.audit(ctx, p, "realgate.demo_days_changed", v.ID, fmt.Sprintf("old=%d new=%d", old.MinDays, next.MinDays))
		s.emitFor(ctx, v.ID, p.Username, "realgate.demo_days_changed", domain.SeverityWarning,
			"Demo days needed for real trading on "+v.Name+" changed",
			fmt.Sprintf("%d → %d qualifying demo days in the last %d risk days", old.MinDays, next.MinDays, next.WindowDays))
		s.disarmIfArmed(ctx, v.Account, p, domain.DisarmDays)
	}
	return s.RealGateStatus(ctx, p, v.ID, "")
}
