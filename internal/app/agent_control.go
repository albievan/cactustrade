package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// Agent control (PLAN 4.11): start, pause, stop, the kill switch and its reset, and the
// resume after a restart (OD5). Every change of an account's agent runs under the account's
// control lock, which SetRiskLimits takes as well: the limits can only change while the agent
// is stopped, with no race between a start and a change of limits.

// AgentStatus is an account's agent state after a control action.
type AgentStatus struct {
	AccountID string
	State     domain.AgentState
}

// KillReport says what a kill did.
type KillReport struct {
	AccountID string
	// Already: the kill switch was on before (positions and orders are flattened again).
	Already bool
	// Cancelled counts cancels sent for pending open orders; Closing the closes sent or in
	// flight for cactusTrade's positions (OD1: never anyone else's).
	Cancelled int
	Closing   int
	Errors    []string
}

// controlled resolves the account for an agent action: the permission, trade access to
// the account (which needs agent.control as well), and, when session is set, a web session
// (OD11: starting and resetting are never done with an API key).
func (s *Service) controlled(ctx context.Context, p Principal, accountID string, perm domain.Permission, session bool, what string) (domain.AccountView, error) {
	if err := s.authorize(p, perm); err != nil {
		return domain.AccountView{}, err
	}
	if session && p.Via != ViaSession {
		return domain.AccountView{}, fmt.Errorf("%s needs a web session, not an API key: %w", what, domain.ErrForbidden)
	}
	return s.account(ctx, p, accountID, domain.AccessTrade)
}

// StartAgent starts an account's agent (agent.control, trade access, web session only). It
// is refused for a real-money account (phase 7), without an eToro client or keys, with
// invalid limits or no trading budget, while unreconciled and while the kill switch is on.
// accountID "" is the caller's current account. Audited, with an event.
func (s *Service) StartAgent(ctx context.Context, p Principal, accountID string) (AgentStatus, error) {
	v, err := s.controlled(ctx, p, accountID, domain.PermAgentControl, true, "starting the agent")
	if err != nil {
		return AgentStatus{}, err
	}
	locks := s.rt.agent(v.ID)
	locks.ctl.Lock()
	defer locks.ctl.Unlock()
	if err := s.startable(ctx, v.Account); err != nil {
		return AgentStatus{}, fmt.Errorf("app.StartAgent: %w", s.fixable(p, v, err))
	}
	cur := s.agentState(ctx, v.ID)
	if cur == domain.AgentRunning {
		return AgentStatus{AccountID: v.ID, State: cur}, nil
	}
	if err := s.setAgentState(ctx, v.ID, domain.AgentRunning); err != nil {
		return AgentStatus{}, fmt.Errorf("app.StartAgent: %w", err)
	}
	l := s.riskLimits(ctx, v.ID)
	s.audit(ctx, p, "agent.start", v.ID, "from "+string(cur)+", trading budget $"+l.TradingBudget.String())
	s.emitFor(ctx, v.ID, p.Username, "agent.started", domain.SeverityInfo, "Agent started on "+v.Name,
		"trading budget $"+l.TradingBudget.String()+"; entries at each 5-minute bar close")
	return AgentStatus{AccountID: v.ID, State: domain.AgentRunning}, nil
}

// The messages of the "missing setup" refusals (R22), for people: no field names.
const (
	msgNoKeys   = "The account's eToro keys are not set"
	msgKilled   = "The kill switch is on: an admin must reset it first"
	msgNoBudget = "Set a trading budget before starting the agent"
)

// startable says why an account's agent may not run (nil = it may): the conditions of
// StartAgent, also used by the OD5 resume.
func (s *Service) startable(ctx context.Context, a domain.Account) error {
	switch {
	case !a.Tradable():
		return fmt.Errorf("%s: %w", a.Name, domain.ErrViewOnly)
	case a.Mode != domain.ModeDemo:
		return fmt.Errorf("%s: %w", a.Name, domain.ErrRealModeGated)
	case s.d.Broker == nil || s.d.Market == nil:
		return fmt.Errorf("the eToro client is not configured: %w", domain.ErrUnavailable)
	case !s.hasKeys(ctx, a):
		return domain.MissingSetup(domain.MissingAccountKeys, msgNoKeys, domain.ErrCredentialsMissing)
	}
	switch killed, err := s.isKilled(ctx, a.ID); {
	case err != nil:
		return fmt.Errorf("reading the kill switch: %w: %w", err, domain.ErrUnavailable)
	case killed:
		return domain.MissingSetup(domain.MissingKillReset, msgKilled, domain.ErrConflict)
	}
	l := s.riskLimits(ctx, a.ID)
	if err := l.Validate(); err != nil {
		return err
	}
	if l.TradingBudget <= 0 {
		return domain.MissingSetup(domain.MissingTradingBudget, msgNoBudget, domain.Invalid("tradingBudgetCents", msgNoBudget))
	}
	if rc := s.Reconciled(a.ID); !rc.OK {
		return domain.MissingSetup(domain.MissingReconcile, fmt.Sprintf("The account is not reconciled (%s)", rc.Reason), domain.ErrConflict)
	}
	return nil
}

func (s *Service) setAgentState(ctx context.Context, accountID string, st domain.AgentState) error {
	return s.d.Store.Settings().Set(ctx, agentStateKey(accountID), string(st), s.d.Clock.Now().UTC())
}

// PauseAgent pauses a running agent (agent.control, trade access; a web session or an API
// key, OD11): no new entries, while the supervisor keeps closing what is due (D21). The
// limits stay locked until the agent is stopped.
func (s *Service) PauseAgent(ctx context.Context, p Principal, accountID string) (AgentStatus, error) {
	return s.haltAgent(ctx, p, accountID, domain.AgentPaused)
}

// StopAgent stops the agent (agent.control, trade access; a web session or an API key):
// no new entries, exits continue, and the limits may be changed.
func (s *Service) StopAgent(ctx context.Context, p Principal, accountID string) (AgentStatus, error) {
	return s.haltAgent(ctx, p, accountID, domain.AgentStopped)
}

func (s *Service) haltAgent(ctx context.Context, p Principal, accountID string, to domain.AgentState) (AgentStatus, error) {
	verbs := map[domain.AgentState]string{domain.AgentPaused: "pause", domain.AgentStopped: "stop"}
	v, err := s.controlled(ctx, p, accountID, domain.PermAgentControl, false, verbs[to]+" the agent")
	if err != nil {
		return AgentStatus{}, err
	}
	locks := s.rt.agent(v.ID)
	locks.ctl.Lock()
	defer locks.ctl.Unlock()
	cur := s.agentState(ctx, v.ID)
	switch {
	case cur == to:
		return AgentStatus{AccountID: v.ID, State: cur}, nil
	case cur == domain.AgentKilled:
		return AgentStatus{}, s.fixable(p, v, domain.MissingSetup(domain.MissingKillReset, msgKilled, domain.ErrConflict))
	case to == domain.AgentPaused && cur != domain.AgentRunning:
		return AgentStatus{}, fmt.Errorf("only a running agent can be paused (it is %s): %w", cur, domain.ErrConflict)
	}
	if err := s.setAgentState(ctx, v.ID, to); err != nil {
		return AgentStatus{}, fmt.Errorf("app.%sAgent: %w", verbs[to], err)
	}
	s.audit(ctx, p, "agent."+verbs[to], v.ID, "from "+string(cur))
	s.emitFor(ctx, v.ID, p.Username, "agent."+string(to), domain.SeverityInfo, "Agent "+string(to)+" on "+v.Name,
		"no new entries; positions due are still closed by the supervisor")
	return AgentStatus{AccountID: v.ID, State: to}, nil
}

// Kill turns the kill switch on (agent.kill, trade access; a web session or an API key,
// OD11). The kill is persisted first (risk state and agent state), then the running entry
// pipeline is cancelled and waited for, then every pending open order of ours is cancelled
// and every position cactusTrade opened is closed through the executor (OD1: never anyone
// else's). Closes are sent without waiting for each other, so they stay within eToro's
// execution quota, which the eToro client's limiter paces (D12: at most 15 positions).
// Killing again flattens again. Audited, with an event.
func (s *Service) Kill(ctx context.Context, p Principal, accountID, reason string) (KillReport, error) {
	v, err := s.controlled(ctx, p, accountID, domain.PermKillSwitch, false, "the kill switch")
	if err != nil {
		return KillReport{}, err
	}
	locks := s.rt.agent(v.ID)
	locks.ctl.Lock()
	defer locks.ctl.Unlock()
	why := "manual kill by " + p.Username
	if r := strings.TrimSpace(reason); r != "" {
		why += ": " + truncate(r, 200)
	}
	rep := KillReport{AccountID: v.ID}
	already, terr := s.tripLocked(ctx, v.Account, p, why)
	rep.Already = already
	if terr != nil {
		// Exits must work even when the kill could not be stored: flatten anyway.
		rep.Errors = append(rep.Errors, "persisting the kill: "+terr.Error())
	}
	// Wait for a running tick to unwind (its context is cancelled): no entry is placed once
	// the flattening starts, and later ticks see the kill.
	locks.entry.Lock()
	defer locks.entry.Unlock()
	s.flattenKilled(ctx, v.Account, &rep)
	if terr != nil {
		return rep, fmt.Errorf("app.Kill: %w", terr)
	}
	return rep, nil
}

// flattenKilled cancels every pending open order of ours and then closes every agent
// position, sending each request once without polling (the supervisor follows them up).
func (s *Service) flattenKilled(ctx context.Context, a domain.Account, rep *KillReport) {
	if a.Mode != domain.ModeDemo || !a.Tradable() || s.d.Broker == nil {
		return // nothing of ours can be there
	}
	sup := SupervisorReport{AccountID: a.ID, Killed: true}
	s.superviseOrders(ctx, a, &sup)
	rep.Cancelled = sup.Cancelled
	rep.Errors = append(rep.Errors, sup.Errors...)
	ours, err := s.d.Store.AgentPositions().ListOpen(ctx, a.ID)
	if err != nil {
		rep.Errors = append(rep.Errors, "reading the agent positions: "+err.Error())
		return
	}
	for _, ap := range ours {
		if _, err := s.closePosition(ctx, a, ap, string(domain.ExitKilled)+": the kill switch is on", nil); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("closing position %d: %v", ap.PositionID, err))
			continue
		}
		rep.Closing++
	}
}

// truncate cuts s to at most n bytes on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// ResetPhrase is what an admin types to reset an account's kill switch (OD10).
func ResetPhrase(accountName string) string { return "RESET " + accountName }

// ResetKill turns the kill switch off (OD10): admin only, in a web session, with the typed
// phrase "RESET <account name>". The high-water mark becomes the current equity (read from
// eToro now; without it nothing is reset), and the agent comes back stopped. Audited, with
// an event.
func (s *Service) ResetKill(ctx context.Context, p Principal, accountID, confirm string) (AgentStatus, error) {
	v, err := s.controlled(ctx, p, accountID, domain.PermKillSwitch, true, "resetting the kill switch")
	if err != nil {
		return AgentStatus{}, err
	}
	if p.Role != domain.RoleAdmin {
		return AgentStatus{}, fmt.Errorf("only an admin can reset the kill switch: %w", domain.ErrForbidden)
	}
	if strings.TrimSpace(confirm) != ResetPhrase(v.Name) {
		return AgentStatus{}, domain.Invalid("confirm", fmt.Sprintf("type %q to confirm", ResetPhrase(v.Name)))
	}
	locks := s.rt.agent(v.ID)
	locks.ctl.Lock()
	defer locks.ctl.Unlock()
	if killed, err := s.isKilled(ctx, v.ID); err != nil {
		return AgentStatus{}, fmt.Errorf("app.ResetKill: reading the kill switch: %w", err)
	} else if !killed {
		return AgentStatus{}, fmt.Errorf("the kill switch is not on: %w", domain.ErrConflict)
	}
	if err := s.needBroker(); err != nil {
		return AgentStatus{}, err
	}
	pf, err := s.d.Broker.Portfolio(brokerCtx(ctx, v))
	if err != nil {
		return AgentStatus{}, fmt.Errorf("app.ResetKill: reading the equity for the new high-water mark: %w", err)
	}
	eq := pf.Equity()
	if eq <= 0 {
		return AgentStatus{}, fmt.Errorf("app.ResetKill: the account has no equity: %w", domain.ErrConflict)
	}
	old, err := s.clearKill(ctx, v.ID, eq)
	if err != nil {
		return AgentStatus{}, fmt.Errorf("app.ResetKill: %w", err)
	}
	if err := s.setAgentState(ctx, v.ID, domain.AgentStopped); err != nil {
		return AgentStatus{}, fmt.Errorf("app.ResetKill: %w", err)
	}
	s.rt.clearKillLatch(v.ID)
	s.audit(ctx, p, "agent.kill_reset", v.ID, fmt.Sprintf("kill reason %q; high-water mark $%s -> $%s", old.KillReason, old.State.HighWaterMark, eq))
	s.emitFor(ctx, v.ID, p.Username, "agent.kill_reset", domain.SeverityWarning, "Kill switch reset on "+v.Name,
		fmt.Sprintf("high-water mark set to the current equity $%s; the agent is stopped until someone starts it", eq))
	return AgentStatus{AccountID: v.ID, State: domain.AgentStopped}, nil
}

// clearKill stores the risk state without the kill and with the high-water mark at equity
// (versioned, retried on a conflict) and returns the state before.
func (s *Service) clearKill(ctx context.Context, accountID string, equity domain.Cents) (domain.AccountRiskState, error) {
	for range 3 {
		cur, err := s.d.Store.RiskStates().Get(ctx, accountID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			cur = domain.AccountRiskState{AccountID: accountID}
		case err != nil:
			return cur, err
		}
		next := cur
		next.State.Killed, next.KillReason, next.KilledAt = false, "", time.Time{}
		next.State.HighWaterMark, next.UpdatedAt = equity, s.d.Clock.Now().UTC()
		if _, err := s.d.Store.RiskStates().Put(ctx, next); !errors.Is(err, domain.ErrConflict) {
			return cur, err
		}
	}
	return domain.AccountRiskState{}, fmt.Errorf("the risk state kept changing: %w", domain.ErrConflict)
}

// ResumeOutcome is what the OD5 resume decided for one account.
type ResumeOutcome struct {
	AccountID string
	State     domain.AgentState
	// Reason says why the agent did not resume ("" = it resumed).
	Reason string
}

// ResumeAgents decides, after start-up reconciliation, what happens to the agents that were
// running before the restart (OD5, owner): an agent resumes by itself only when its account
// reconciled cleanly in this process, is demo, not killed, has valid limits with a budget
// and keys; otherwise it comes back paused (killed when the kill switch is on), exits stay
// active, and a warning event (an alert) says why. Either way an event records it. It must
// run after ReconcileAll and before the agent loop (4.14).
func (s *Service) ResumeAgents(ctx context.Context) ([]ResumeOutcome, error) {
	as, err := s.d.Store.Accounts().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("app.ResumeAgents: %w", err)
	}
	var out []ResumeOutcome
	for _, a := range as {
		if s.agentState(ctx, a.ID) != domain.AgentRunning {
			continue
		}
		out = append(out, s.resume(ctx, a))
	}
	return out, nil
}

func (s *Service) resume(ctx context.Context, a domain.Account) ResumeOutcome {
	locks := s.rt.agent(a.ID)
	locks.ctl.Lock()
	defer locks.ctl.Unlock()
	r := ResumeOutcome{AccountID: a.ID, State: domain.AgentRunning}
	if err := s.startable(ctx, a); err != nil {
		r.State, r.Reason = domain.AgentPaused, err.Error()
		if killed, kerr := s.isKilled(ctx, a.ID); killed || kerr != nil {
			r.State = domain.AgentKilled
		}
		if serr := s.setAgentState(ctx, a.ID, r.State); serr != nil {
			r.Reason += "; storing the state failed: " + serr.Error()
		}
		s.audit(ctx, Principal{}, "agent.resume_refused", a.ID, r.Reason)
		s.emitFor(ctx, a.ID, "", "agent.resume_refused", domain.SeverityWarning, "Agent on "+a.Name+" did not resume after the restart",
			"it is "+string(r.State)+" because "+r.Reason+". Positions due are still closed by the supervisor.")
		return r
	}
	s.audit(ctx, Principal{}, "agent.resume", a.ID, "after a clean reconciliation")
	s.emitFor(ctx, a.ID, "", "agent.resumed", domain.SeverityInfo, "Agent on "+a.Name+" resumed after the restart",
		"the account reconciled cleanly with eToro")
	return r
}
