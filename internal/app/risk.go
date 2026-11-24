package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// Setting keys for an account's agent and limits.
func agentStateKey(accountID string) string { return "agent.state." + accountID }
func riskLimitsKey(accountID string) string { return "risk.limits." + accountID }

// limitsJSON is how the limits are stored (durations in seconds, money in cents).
type limitsJSON struct {
	TradingBudget      int64  `json:"tradingBudgetCents"`
	DailyLoss          int64  `json:"dailyLossBp"`
	Drawdown           int64  `json:"drawdownBp"`
	MaxPosition        int64  `json:"maxPositionBp"`
	MaxOpenPositions   int    `json:"maxOpenPositions"`
	NoEntryBeforeClose int64  `json:"noEntryBeforeCloseS"`
	FlattenBeforeClose int64  `json:"flattenBeforeCloseS"`
	MaxPortfolioAge    int64  `json:"maxPortfolioAgeS"`
	MaxQuoteAge        int64  `json:"maxQuoteAgeS"`
	MaxShort           *int64 `json:"maxShortBp,omitempty"` // absent in limits stored before shorts: default
}

func toLimitsJSON(l domain.RiskLimits) limitsJSON {
	return limitsJSON{
		TradingBudget: int64(l.TradingBudget), DailyLoss: int64(l.DailyLoss), Drawdown: int64(l.Drawdown),
		MaxPosition: int64(l.MaxPosition), MaxOpenPositions: l.MaxOpenPositions,
		NoEntryBeforeClose: int64(l.NoEntryBeforeClose / time.Second), FlattenBeforeClose: int64(l.FlattenBeforeClose / time.Second),
		MaxPortfolioAge: int64(l.MaxPortfolioAge / time.Second), MaxQuoteAge: int64(l.MaxQuoteAge / time.Second),
		MaxShort: ptrTo(int64(l.MaxShort)),
	}
}

func ptrTo[T any](v T) *T { return &v }

func (j limitsJSON) domain() domain.RiskLimits {
	l := domain.RiskLimits{
		TradingBudget: domain.Cents(j.TradingBudget), DailyLoss: domain.BasisPoints(j.DailyLoss),
		Drawdown: domain.BasisPoints(j.Drawdown), MaxPosition: domain.BasisPoints(j.MaxPosition),
		MaxOpenPositions: j.MaxOpenPositions, NoEntryBeforeClose: time.Duration(j.NoEntryBeforeClose) * time.Second,
		FlattenBeforeClose: time.Duration(j.FlattenBeforeClose) * time.Second,
		MaxPortfolioAge:    time.Duration(j.MaxPortfolioAge) * time.Second, MaxQuoteAge: time.Duration(j.MaxQuoteAge) * time.Second,
		MaxShort: domain.HardMaxShort,
	}
	if j.MaxShort != nil {
		l.MaxShort = domain.BasisPoints(*j.MaxShort)
	}
	return l
}

// agentState is an account's stored agent state; anything unreadable or unknown is
// "stopped" (fail closed: never running by accident).
func (s *Service) agentState(ctx context.Context, accountID string) domain.AgentState {
	v, err := s.d.Store.Settings().Get(ctx, agentStateKey(accountID))
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			s.d.Log.WarnContext(ctx, "reading the agent state failed; treating it as stopped", "err", err)
		}
		return domain.AgentStopped
	}
	switch st := domain.AgentState(v); st {
	case domain.AgentRunning, domain.AgentPaused, domain.AgentKilled:
		return st
	}
	return domain.AgentStopped
}

// riskLimits returns the stored limits, or the defaults with a zero budget. A stored value
// that no longer validates is replaced by the defaults with a zero budget (nothing trades).
func (s *Service) riskLimits(ctx context.Context, accountID string) domain.RiskLimits {
	def := domain.DefaultRiskLimits
	v, err := s.d.Store.Settings().Get(ctx, riskLimitsKey(accountID))
	if err != nil {
		return def
	}
	var j limitsJSON
	if err := json.Unmarshal([]byte(v), &j); err != nil {
		s.d.Log.ErrorContext(ctx, "stored risk limits are unreadable; using defaults with no budget", "err", err)
		return def
	}
	l := j.domain()
	if err := l.Validate(); err != nil {
		s.d.Log.ErrorContext(ctx, "stored risk limits are invalid; using defaults with no budget", "err", err)
		return def
	}
	return l
}

// RiskLimits returns the current account's limits (view).
func (s *Service) RiskLimits(ctx context.Context, p Principal) (domain.RiskLimits, error) {
	v, err := s.account(ctx, p, "", domain.AccessView)
	if err != nil {
		return domain.RiskLimits{}, err
	}
	return s.riskLimits(ctx, v.ID), nil
}

// SetRiskLimits changes the current account's limits (owner, web session only). Only while
// its agent is stopped; the trading budget may not exceed the account's equity as eToro
// reports it now (a budget above zero therefore needs a working eToro connection). Audited
// with the old and new values.
func (s *Service) SetRiskLimits(ctx context.Context, p Principal, l domain.RiskLimits) (domain.RiskLimits, error) {
	v, err := s.account(ctx, p, "", domain.AccessOwner)
	if err != nil {
		return domain.RiskLimits{}, err
	}
	if p.Via != ViaSession { // RiskLimitsView applies the same rules
		return domain.RiskLimits{}, fmt.Errorf("risk limits can only be changed in the web UI: %w", domain.ErrForbidden)
	}
	if err := s.limitsChangeable(ctx, v.ID); err != nil {
		return domain.RiskLimits{}, err
	}
	if err := l.Validate(); err != nil {
		return domain.RiskLimits{}, err
	}
	// eToro's equity is read before the control lock is taken (review m5): a Kill never
	// waits for this call.
	if l.TradingBudget > 0 {
		if err := s.needBroker(); err != nil {
			return domain.RiskLimits{}, err
		}
		pf, err := s.d.Broker.Portfolio(brokerCtx(ctx, v))
		if err != nil {
			return domain.RiskLimits{}, fmt.Errorf("checking the budget against equity: %w", err)
		}
		if eq := pf.Equity(); l.TradingBudget > eq {
			return domain.RiskLimits{}, domain.Invalid("tradingBudgetCents", fmt.Sprintf("must not exceed the account equity ($%s)", eq))
		}
	}
	// Under the agent's control lock, checked again: a concurrent StartAgent or Kill either
	// sees these limits or makes this change fail (PLAN 4.11).
	locks := s.rt.agent(v.ID)
	locks.ctl.Lock()
	defer locks.ctl.Unlock()
	if err := s.limitsChangeable(ctx, v.ID); err != nil {
		return domain.RiskLimits{}, err
	}
	old := s.riskLimits(ctx, v.ID)
	b, err := json.Marshal(toLimitsJSON(l))
	if err != nil {
		return domain.RiskLimits{}, fmt.Errorf("app.SetRiskLimits: %w", err)
	}
	if err := s.d.Store.Settings().Set(ctx, riskLimitsKey(v.ID), string(b), s.d.Clock.Now().UTC()); err != nil {
		return domain.RiskLimits{}, fmt.Errorf("app.SetRiskLimits: %w", err)
	}
	ob, _ := json.Marshal(toLimitsJSON(old))
	s.audit(ctx, p, "risk.limits_changed", v.ID, "old="+string(ob)+" new="+string(b))
	s.emitFor(ctx, v.ID, p.Username, "risk.limits_changed", domain.SeverityWarning, "Risk limits of "+v.Name+" changed", "trading budget $"+l.TradingBudget.String())
	return l, nil
}

// limitsChangeable refuses a change of limits unless the stored agent state is stopped and no
// kill is latched. The state is read directly (review m2): an unreadable state is a refusal,
// never "stopped"; only a state never stored counts as stopped.
func (s *Service) limitsChangeable(ctx context.Context, accountID string) error {
	_, err := s.limitsBlocked(ctx, accountID)
	return err
}

// limitsBlocked applies limitsChangeable's rules and returns, besides its error, the reason
// for people ("" with a nil error = the limits can change).
func (s *Service) limitsBlocked(ctx context.Context, accountID string) (string, error) {
	v, err := s.d.Store.Settings().Get(ctx, agentStateKey(accountID))
	switch {
	case errors.Is(err, domain.ErrNotFound):
		v = string(domain.AgentStopped)
	case err != nil:
		return "The agent's state could not be read; try again in a moment",
			fmt.Errorf("reading the agent state: %w: %w", err, domain.ErrUnavailable)
	}
	st := domain.AgentState(v)
	if st == domain.AgentKilled || s.rt.killLatched(accountID) {
		return msgLimitsKilled, fmt.Errorf("the kill switch is on: an admin must reset it before the limits can change: %w", domain.ErrConflict)
	}
	if st != domain.AgentStopped {
		return fmt.Sprintf("Stop the agent to change the limits (it is %s)", st),
			fmt.Errorf("stop the agent before changing the limits (it is %s): %w", st, domain.ErrConflict)
	}
	return "", nil
}

// The reasons for people why the caller cannot change the limits (PLAN 6.14).
const (
	msgLimitsKilled     = "The kill switch is on: an admin must reset it on the dashboard before the limits can change"
	msgLimitsNotOwner   = "Only the account's owner can change its limits"
	msgLimitsNotSession = "The limits can only be changed in the web UI, not with an API key"
)

// RiskLimitsView is the current account's limits with whether the caller may change them now.
type RiskLimitsView struct {
	Limits domain.RiskLimits
	// CanEdit follows SetRiskLimits' rules; EditBlocked says why not ("" when CanEdit).
	CanEdit     bool
	EditBlocked string
}

// RiskLimitsView returns the current account's limits (view access) and whether the caller
// may change them now, by the rules SetRiskLimits applies (PLAN 6.14: the server decides,
// the web UI only shows it): the account's owner, in a web session, with the agent stopped
// and no kill switch on. Whether a budget fits the equity is checked only on the change.
func (s *Service) RiskLimitsView(ctx context.Context, p Principal) (RiskLimitsView, error) {
	v, err := s.account(ctx, p, "", domain.AccessView)
	if err != nil {
		return RiskLimitsView{}, err
	}
	out := RiskLimitsView{Limits: s.riskLimits(ctx, v.ID), EditBlocked: s.limitsEditBlocked(ctx, p, v)}
	out.CanEdit = out.EditBlocked == ""
	return out, nil
}

// limitsEditBlocked says why p may not change account v's limits now, by SetRiskLimits'
// rules ("" = it may).
func (s *Service) limitsEditBlocked(ctx context.Context, p Principal, v domain.AccountView) string {
	switch {
	case !v.Access.AtLeast(domain.AccessOwner):
		return msgLimitsNotOwner
	case p.Via != ViaSession:
		return msgLimitsNotSession
	default:
		why, _ := s.limitsBlocked(ctx, v.ID)
		return why
	}
}
