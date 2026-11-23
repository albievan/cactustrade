package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// Setting keys for the agent and its limits.
const (
	settingAgentState = "agent.state"
	settingRiskLimits = "risk.limits"
)

// limitsJSON is how the limits are stored (durations in seconds, money in cents).
type limitsJSON struct {
	TradingBudget      int64 `json:"tradingBudgetCents"`
	DailyLoss          int64 `json:"dailyLossBp"`
	Drawdown           int64 `json:"drawdownBp"`
	MaxPosition        int64 `json:"maxPositionBp"`
	MaxOpenPositions   int   `json:"maxOpenPositions"`
	NoEntryBeforeClose int64 `json:"noEntryBeforeCloseS"`
	FlattenBeforeClose int64 `json:"flattenBeforeCloseS"`
	MaxPortfolioAge    int64 `json:"maxPortfolioAgeS"`
	MaxQuoteAge        int64 `json:"maxQuoteAgeS"`
}

func toLimitsJSON(l domain.RiskLimits) limitsJSON {
	return limitsJSON{
		TradingBudget: int64(l.TradingBudget), DailyLoss: int64(l.DailyLoss), Drawdown: int64(l.Drawdown),
		MaxPosition: int64(l.MaxPosition), MaxOpenPositions: l.MaxOpenPositions,
		NoEntryBeforeClose: int64(l.NoEntryBeforeClose / time.Second), FlattenBeforeClose: int64(l.FlattenBeforeClose / time.Second),
		MaxPortfolioAge: int64(l.MaxPortfolioAge / time.Second), MaxQuoteAge: int64(l.MaxQuoteAge / time.Second),
	}
}

func (j limitsJSON) domain() domain.RiskLimits {
	return domain.RiskLimits{
		TradingBudget: domain.Cents(j.TradingBudget), DailyLoss: domain.BasisPoints(j.DailyLoss),
		Drawdown: domain.BasisPoints(j.Drawdown), MaxPosition: domain.BasisPoints(j.MaxPosition),
		MaxOpenPositions: j.MaxOpenPositions, NoEntryBeforeClose: time.Duration(j.NoEntryBeforeClose) * time.Second,
		FlattenBeforeClose: time.Duration(j.FlattenBeforeClose) * time.Second,
		MaxPortfolioAge:    time.Duration(j.MaxPortfolioAge) * time.Second, MaxQuoteAge: time.Duration(j.MaxQuoteAge) * time.Second,
	}
}

// AgentState is the stored agent state; anything unreadable or unknown is "killed" when
// the stored text says killed, otherwise "stopped" (fail closed: never running by accident).
func (s *Service) AgentState(ctx context.Context) domain.AgentState {
	v, err := s.d.Store.Settings().Get(ctx, settingAgentState)
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
func (s *Service) riskLimits(ctx context.Context) domain.RiskLimits {
	def := domain.DefaultRiskLimits
	v, err := s.d.Store.Settings().Get(ctx, settingRiskLimits)
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

// RiskLimits returns the current limits (viewer).
func (s *Service) RiskLimits(ctx context.Context, p Principal) (domain.RiskLimits, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return domain.RiskLimits{}, err
	}
	return s.riskLimits(ctx), nil
}

// SetRiskLimits changes the limits (admin, web session only). Only while the agent is
// stopped; the trading budget may not exceed the account's equity as eToro reports it now
// (a budget above zero therefore needs a working eToro connection). Audited with the old
// and new values.
func (s *Service) SetRiskLimits(ctx context.Context, p Principal, l domain.RiskLimits) (domain.RiskLimits, error) {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return domain.RiskLimits{}, err
	}
	if p.Via != ViaSession {
		return domain.RiskLimits{}, fmt.Errorf("risk limits can only be changed in the web UI: %w", domain.ErrForbidden)
	}
	if st := s.AgentState(ctx); st != domain.AgentStopped {
		return domain.RiskLimits{}, fmt.Errorf("stop the agent before changing the limits (it is %s): %w", st, domain.ErrConflict)
	}
	if err := l.Validate(); err != nil {
		return domain.RiskLimits{}, err
	}
	if l.TradingBudget > 0 {
		if err := s.needBroker(); err != nil {
			return domain.RiskLimits{}, err
		}
		pf, err := s.d.Broker.Portfolio(ctx)
		if err != nil {
			return domain.RiskLimits{}, fmt.Errorf("checking the budget against equity: %w", err)
		}
		if eq := pf.Equity(); l.TradingBudget > eq {
			return domain.RiskLimits{}, domain.Invalid("tradingBudgetCents", fmt.Sprintf("must not exceed the account equity ($%s)", eq))
		}
	}
	old := s.riskLimits(ctx)
	b, err := json.Marshal(toLimitsJSON(l))
	if err != nil {
		return domain.RiskLimits{}, fmt.Errorf("app.SetRiskLimits: %w", err)
	}
	if err := s.d.Store.Settings().Set(ctx, settingRiskLimits, string(b), s.d.Clock.Now().UTC()); err != nil {
		return domain.RiskLimits{}, fmt.Errorf("app.SetRiskLimits: %w", err)
	}
	ob, _ := json.Marshal(toLimitsJSON(old))
	s.audit(ctx, p, "risk.limits_changed", "", "old="+string(ob)+" new="+string(b))
	s.emit(ctx, p.Username, "risk.limits_changed", domain.SeverityWarning, "Risk limits changed", "trading budget $"+l.TradingBudget.String())
	return l, nil
}
