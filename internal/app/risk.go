package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
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
	// MinOrderValue and T212Fractional are Trading 212's (R45); absent (0, off) on eToro.
	MinOrderValue  int64 `json:"minOrderValueCents,omitempty"`
	T212Fractional bool  `json:"t212Fractional,omitempty"`
}

func toLimitsJSON(l domain.RiskLimits) limitsJSON {
	return limitsJSON{
		TradingBudget: int64(l.TradingBudget), DailyLoss: int64(l.DailyLoss), Drawdown: int64(l.Drawdown),
		MaxPosition: int64(l.MaxPosition), MaxOpenPositions: l.MaxOpenPositions,
		NoEntryBeforeClose: int64(l.NoEntryBeforeClose / time.Second), FlattenBeforeClose: int64(l.FlattenBeforeClose / time.Second),
		MaxPortfolioAge: int64(l.MaxPortfolioAge / time.Second), MaxQuoteAge: int64(l.MaxQuoteAge / time.Second),
		MaxShort: ptrTo(int64(l.MaxShort)), MinOrderValue: int64(l.MinOrderValue), T212Fractional: l.T212Fractional,
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
		MaxShort: domain.HardMaxShort, MinOrderValue: domain.Cents(j.MinOrderValue), T212Fractional: j.T212Fractional,
	}
	if j.MaxShort != nil {
		l.MaxShort = domain.BasisPoints(*j.MaxShort)
	}
	return l
}

// agentState is an account's stored agent state; anything unreadable or unknown is
// "stopped" (fail closed: never running by accident).
func agentSinceKey(accountID string) string { return "agent.since." + accountID }

// agentSince is when the agent entered its current state (zero when unknown, e.g. a state
// stored before this was recorded, or never started).
func (s *Service) agentSince(ctx context.Context, accountID string) time.Time {
	v, err := s.d.Store.Settings().Get(ctx, agentSinceKey(accountID))
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (s *Service) agentState(ctx context.Context, accountID string) domain.AgentState {
	st, err := s.readAgentState(ctx, accountID)
	if err != nil {
		s.d.Log.WarnContext(ctx, "reading the agent state failed; treating it as stopped", "err", err)
		return domain.AgentStopped
	}
	return st
}

// readAgentState is an account's stored agent state, with the store's error: never stored
// (or an unknown value) is "stopped", but a failed read is an error. Callers that must not
// mistake an unreadable state for a stopped agent (the data reset's checks) use it and
// refuse on error (CLAUDE.md §2.6: fail closed).
func (s *Service) readAgentState(ctx context.Context, accountID string) (domain.AgentState, error) {
	v, err := s.d.Store.Settings().Get(ctx, agentStateKey(accountID))
	if errors.Is(err, domain.ErrNotFound) {
		return domain.AgentStopped, nil
	}
	if err != nil {
		return "", fmt.Errorf("app.readAgentState: %w", err)
	}
	switch st := domain.AgentState(v); st {
	case domain.AgentRunning, domain.AgentPaused, domain.AgentKilled:
		return st, nil
	}
	return domain.AgentStopped, nil
}

// riskLimits returns the stored limits, or the defaults with a zero budget. A stored value
// that no longer validates is replaced by the defaults with a zero budget (nothing trades).
func (s *Service) riskLimits(ctx context.Context, accountID string) domain.RiskLimits {
	if l, ok := s.storedLimits(ctx, accountID); ok {
		return l
	}
	return domain.DefaultRiskLimits
}

// storedLimits reads the stored limits; ok is false when there are none or they are
// unreadable or invalid (logged).
func (s *Service) storedLimits(ctx context.Context, accountID string) (domain.RiskLimits, bool) {
	v, err := s.d.Store.Settings().Get(ctx, riskLimitsKey(accountID))
	if err != nil {
		return domain.RiskLimits{}, false
	}
	var j limitsJSON
	if err := json.Unmarshal([]byte(v), &j); err != nil {
		s.d.Log.ErrorContext(ctx, "stored risk limits are unreadable; using defaults with no budget", "err", err)
		return domain.RiskLimits{}, false
	}
	l := j.domain()
	if err := l.Validate(); err != nil {
		s.d.Log.ErrorContext(ctx, "stored risk limits are invalid; using defaults with no budget", "err", err)
		return domain.RiskLimits{}, false
	}
	return l, true
}

// riskLimitsFor is riskLimits with the defaults of the account's mode: a real-money account
// without stored (or with unreadable) limits gets domain.DefaultRealRiskLimits (OD25). Stored
// limits are returned as they are (a real account's limits over the real ceilings stay
// visible; the gate's checklist refuses them).
func (s *Service) riskLimitsFor(ctx context.Context, a domain.Account) domain.RiskLimits {
	if l, ok := s.storedLimits(ctx, a.ID); ok {
		return l
	}
	l := domain.DefaultLimitsFor(accountMode(a))
	if a.OnPlatform() == domain.PlatformTrading212 {
		l.MaxShort = 0 // long only (D41 b); the minimum order value stays unset until the owner sets it
	}
	return l
}

// RiskLimits returns the current account's limits (view): an eToro account or a Trading 212
// practice account.
func (s *Service) RiskLimits(ctx context.Context, p Principal) (domain.RiskLimits, error) {
	v, err := s.agentAccount(ctx, p, "", domain.AccessView)
	if err != nil {
		return domain.RiskLimits{}, err
	}
	return s.riskLimitsFor(ctx, v.Account), nil
}

// SetRiskLimits changes the current account's limits (owner, web session only). Only while
// its agent is stopped; the trading budget may not exceed the account's equity as eToro
// reports it now (a budget above zero therefore needs a working eToro connection). On a
// real-money account the limits must stay within domain.RealCeilings (OD25), and raising any
// of them disarms real trading (OD24). The platform's rules apply too (ValidateForPlatform):
// on a Trading 212 practice account the budget is checked against Trading 212's equity (in
// the account's currency), a minimum order value is required and shorts and the fractional
// switch are refused (T212-e, T212-m). Audited with the old and new values.
func (s *Service) SetRiskLimits(ctx context.Context, p Principal, l domain.RiskLimits) (domain.RiskLimits, error) {
	v, err := s.agentAccount(ctx, p, "", domain.AccessOwner)
	if err != nil {
		return domain.RiskLimits{}, err
	}
	if p.Via != ViaSession { // RiskLimitsView applies the same rules
		return domain.RiskLimits{}, fmt.Errorf("risk limits can only be changed in the web UI: %w", domain.ErrForbidden)
	}
	if err := s.limitsChangeable(ctx, v.ID); err != nil {
		return domain.RiskLimits{}, err
	}
	if err := l.ValidateFor(accountMode(v.Account)); err != nil {
		return domain.RiskLimits{}, err
	}
	if err := l.ValidateForPlatform(v.OnPlatform()); err != nil {
		return domain.RiskLimits{}, err
	}
	// The equity is read before the control lock is taken (review m5): a Kill never waits for
	// this call.
	budgetText := "$" + l.TradingBudget.String()
	if l.TradingBudget > 0 {
		cur, err := s.budgetWithinEquity(ctx, v, l.TradingBudget)
		if err != nil {
			return domain.RiskLimits{}, err
		}
		if v.T212Trading() {
			budgetText = domain.FormatMoney(cur, l.TradingBudget) // the account's currency, never "$" (T212-f)
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
	old := s.riskLimitsFor(ctx, v.Account)
	b, err := json.Marshal(toLimitsJSON(l))
	if err != nil {
		return domain.RiskLimits{}, fmt.Errorf("app.SetRiskLimits: %w", err)
	}
	if err := s.d.Store.Settings().Set(ctx, riskLimitsKey(v.ID), string(b), s.d.Clock.Now().UTC()); err != nil {
		return domain.RiskLimits{}, fmt.Errorf("app.SetRiskLimits: %w", err)
	}
	ob, _ := json.Marshal(toLimitsJSON(old))
	s.audit(ctx, p, "risk.limits_changed", v.ID, "old="+string(ob)+" new="+string(b))
	s.emitFor(ctx, v.ID, p.Username, "risk.limits_changed", domain.SeverityWarning, "Risk limits of "+v.Name+" changed", "trading budget "+budgetText)
	if domain.RaisesAny(old, l) {
		s.disarmIfArmed(ctx, v.Account, p, domain.DisarmLimits)
	}
	return l, nil
}

// budgetWithinEquity refuses a trading budget above the account's equity as its platform
// reports it now (eToro's portfolio, or Trading 212's account summary), and returns the
// currency of that equity ("USD" on eToro).
func (s *Service) budgetWithinEquity(ctx context.Context, v domain.AccountView, budget domain.Cents) (string, error) {
	if v.T212Trading() {
		if err := s.needT212(); err != nil {
			return "", err
		}
		a, err := s.d.T212.Account(ports.WithAccount(ctx, v.Account))
		if err != nil {
			return "", fmt.Errorf("checking the budget against equity: %w", err)
		}
		if budget > a.Equity {
			return "", domain.Invalid("tradingBudgetCents", "must not exceed the account equity ("+domain.FormatMoney(a.Currency, a.Equity)+")")
		}
		return a.Currency, nil
	}
	if err := s.needBroker(); err != nil {
		return "", err
	}
	pf, err := s.d.Broker.Portfolio(brokerCtx(ctx, v))
	if err != nil {
		return "", fmt.Errorf("checking the budget against equity: %w", err)
	}
	if eq := pf.Equity(); budget > eq {
		return "", domain.Invalid("tradingBudgetCents", fmt.Sprintf("must not exceed the account equity ($%s)", eq))
	}
	return "USD", nil
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
	// Preset is the risk preset the limits match, or domain.PresetCustom (R28).
	Preset domain.RiskPreset
	// Platform is the account's platform; Currency the currency of the money figures (USD on
	// eToro, the Trading 212 account's own, "" when it could not be read); FractionsVerified
	// whether the Trading 212 fractional switch may be turned on (domain.T212FractionsVerified).
	Platform          domain.Platform
	Currency          string
	FractionsVerified bool
}

// RiskLimitsView returns the current account's limits (view access) and whether the caller
// may change them now, by the rules SetRiskLimits applies (PLAN 6.14: the server decides,
// the web UI only shows it): the account's owner, in a web session, with the agent stopped
// and no kill switch on. Whether a budget fits the equity is checked only on the change.
func (s *Service) RiskLimitsView(ctx context.Context, p Principal) (RiskLimitsView, error) {
	v, err := s.agentAccount(ctx, p, "", domain.AccessView)
	if err != nil {
		return RiskLimitsView{}, err
	}
	out := RiskLimitsView{Limits: s.riskLimitsFor(ctx, v.Account), EditBlocked: s.limitsEditBlocked(ctx, p, v)}
	out.CanEdit = out.EditBlocked == ""
	out.Preset = presetOf(v.Account, out.Limits)
	out.Platform, out.Currency, out.FractionsVerified = v.OnPlatform(), "USD", domain.T212FractionsVerified
	if v.T212Trading() {
		cur, err := s.t212AccountCurrency(ctx, v.Account)
		if err != nil {
			s.d.Log.WarnContext(ctx, "reading the Trading 212 currency for the limits view failed", "account", v.ID, "err", err)
		}
		out.Currency = cur
	}
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
