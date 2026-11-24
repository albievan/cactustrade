package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/strategy"
)

// The agent's style (PLAN R28): risk presets, aggressiveness and autonomy. Presets go through
// SetRiskLimits (every rule of a change of limits applies); aggressiveness and autonomy are
// stored per account and change under the same rules (owner, web session, agent stopped, no
// kill). None of them weakens a guardrail (CLAUDE.md §2): presets pass ValidateFor, the
// aggressiveness only tunes the strategy inside the limits, and autonomy never changes a
// limit, resets a kill or touches the real-mode gate.

// Setting keys of an account's style.
func aggressivenessKey(accountID string) string { return "agent.aggressiveness." + accountID }
func autonomyKey(accountID string) string       { return "agent.autonomy." + accountID }
func adaptKey(accountID string) string          { return "agent.aggressiveness_adapt." + accountID }

// autonomyOf is the account's stored autonomy level, or the default (medium). A value that
// cannot be read or is unknown is the default: full autonomy needs an explicit, readable
// choice.
func (s *Service) autonomyOf(ctx context.Context, accountID string) domain.Autonomy {
	v, err := s.d.Store.Settings().Get(ctx, autonomyKey(accountID))
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			s.d.Log.WarnContext(ctx, "reading the autonomy failed; using the default", "account", accountID, "err", err)
		}
		return domain.DefaultAutonomy
	}
	if a := domain.Autonomy(v); a.Valid() {
		return a
	}
	return domain.DefaultAutonomy
}

// aggressivenessOf is the account's chosen aggressiveness, or the default (medium).
func (s *Service) aggressivenessOf(ctx context.Context, accountID string) domain.Aggressiveness {
	v, err := s.d.Store.Settings().Get(ctx, aggressivenessKey(accountID))
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			s.d.Log.WarnContext(ctx, "reading the aggressiveness failed; using the default", "account", accountID, "err", err)
		}
		return domain.DefaultAggressiveness
	}
	if a := domain.Aggressiveness(v); a.Valid() {
		return a
	}
	return domain.DefaultAggressiveness
}

// adaptOf is full autonomy's adjustment of the account's aggressiveness (zero when none).
func (s *Service) adaptOf(ctx context.Context, accountID string) domain.AggressivenessAdapt {
	var a domain.AggressivenessAdapt
	v, err := s.d.Store.Settings().Get(ctx, adaptKey(accountID))
	if err != nil {
		return a
	}
	if err := json.Unmarshal([]byte(v), &a); err != nil || a.Down < 0 {
		// Unreadable: the most careful reading (fail closed: never bolder than chosen).
		return domain.AggressivenessAdapt{Down: len(domain.AggressivenessLevels) - 1}
	}
	return a
}

func (s *Service) storeAdapt(ctx context.Context, accountID string, a domain.AggressivenessAdapt) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return s.d.Store.Settings().Set(ctx, adaptKey(accountID), string(b), s.d.Clock.Now().UTC())
}

// policyOf is the autonomy policy of account a.
func (s *Service) policyOf(ctx context.Context, a domain.Account) domain.AutonomyPolicy {
	return s.autonomyOf(ctx, a.ID).Policy(accountMode(a))
}

// effectiveAggressiveness is the level the strategy uses for account a: the chosen one, one
// step lower per step full autonomy took it down.
func (s *Service) effectiveAggressiveness(ctx context.Context, a domain.Account) domain.Aggressiveness {
	chosen := s.aggressivenessOf(ctx, a.ID)
	if !s.policyOf(ctx, a).AdaptAggressiveness {
		return chosen
	}
	return chosen.Down(s.adaptOf(ctx, a.ID).Down)
}

// strategyConfig is the strategy's configuration for account a with limits l.
func (s *Service) strategyConfig(ctx context.Context, a domain.Account, l domain.RiskLimits) strategy.Config {
	return strategy.ConfigFor(s.effectiveAggressiveness(ctx, a), l.MaxShort > 0)
}

// ResearchMode is what the scanner and the web research may do with the shares they find for
// an account (R28): propose them (low autonomy) or add them to the watchlist with an event
// (medium and above). An unknown or view-only account, or one that cannot be read, is
// domain.ResearchOff.
func (s *Service) ResearchMode(ctx context.Context, accountID string) domain.ResearchMode {
	a, err := s.d.Store.Accounts().Get(ctx, accountID)
	if err != nil || !a.Tradable() {
		return domain.ResearchOff
	}
	return s.policyOf(ctx, a).Research
}

// AutonomyPolicy is an account's autonomy policy (for the scanner and the web research: how
// often they run, whether they add shares and assign themes). ok is false for an unknown or
// view-only account.
func (s *Service) AutonomyPolicy(ctx context.Context, accountID string) (domain.AutonomyPolicy, bool) {
	a, err := s.d.Store.Accounts().Get(ctx, accountID)
	if err != nil || !a.Tradable() {
		return domain.AutonomyPolicy{Research: domain.ResearchOff}, false
	}
	return s.policyOf(ctx, a), true
}

// AgentStyleView is the current account's aggressiveness and autonomy.
type AgentStyleView struct {
	AccountID string
	// Aggressiveness is the owner's choice; Effective what the strategy uses now (lower when
	// full autonomy stepped it down after a daily-loss day).
	Aggressiveness domain.Aggressiveness
	Effective      domain.Aggressiveness
	Autonomy       domain.Autonomy
	Policy         domain.AutonomyPolicy
	// CanEdit follows SetAgentStyle's rules; EditBlocked says why not.
	CanEdit     bool
	EditBlocked string
}

// AgentStyle returns the current account's aggressiveness and autonomy (view) and whether
// the caller may change them now (the rules of SetRiskLimits).
func (s *Service) AgentStyle(ctx context.Context, p Principal) (AgentStyleView, error) {
	v, err := s.account(ctx, p, "", domain.AccessView)
	if err != nil {
		return AgentStyleView{}, err
	}
	return s.styleView(ctx, p, v), nil
}

func (s *Service) styleView(ctx context.Context, p Principal, v domain.AccountView) AgentStyleView {
	out := AgentStyleView{
		AccountID: v.ID, Aggressiveness: s.aggressivenessOf(ctx, v.ID), Effective: s.effectiveAggressiveness(ctx, v.Account),
		Autonomy: s.autonomyOf(ctx, v.ID), Policy: s.policyOf(ctx, v.Account), EditBlocked: s.limitsEditBlocked(ctx, p, v),
	}
	out.CanEdit = out.EditBlocked == ""
	return out
}

// SetAgentStyle changes the current account's aggressiveness and/or autonomy ("" = keep). The
// same rules as SetRiskLimits: the account's owner, in a web session, with the agent stopped
// and no kill switch on, checked again under the agent's control lock. A change resets full
// autonomy's adjustment of the aggressiveness. Audited, with an event.
func (s *Service) SetAgentStyle(ctx context.Context, p Principal, aggr domain.Aggressiveness, aut domain.Autonomy) (AgentStyleView, error) {
	v, err := s.account(ctx, p, "", domain.AccessOwner)
	if err != nil {
		return AgentStyleView{}, err
	}
	if p.Via != ViaSession {
		return AgentStyleView{}, fmt.Errorf("the agent's style can only be changed in the web UI: %w", domain.ErrForbidden)
	}
	switch {
	case aggr == "" && aut == "":
		return AgentStyleView{}, domain.Invalid("aggressiveness", "give an aggressiveness or an autonomy level")
	case aggr != "" && !aggr.Valid():
		return AgentStyleView{}, domain.Invalid("aggressiveness", "must be low, medium, high or full")
	case aut != "" && !aut.Valid():
		return AgentStyleView{}, domain.Invalid("autonomy", "must be low, medium, high or full")
	}
	locks := s.rt.agent(v.ID)
	locks.ctl.Lock()
	defer locks.ctl.Unlock()
	if err := s.limitsChangeable(ctx, v.ID); err != nil {
		return AgentStyleView{}, err
	}
	oldAggr, oldAut := s.aggressivenessOf(ctx, v.ID), s.autonomyOf(ctx, v.ID)
	newAggr, newAut := cmp.Or(aggr, oldAggr), cmp.Or(aut, oldAut)
	now := s.d.Clock.Now().UTC()
	if err := s.d.Store.Settings().Set(ctx, aggressivenessKey(v.ID), string(newAggr), now); err != nil {
		return AgentStyleView{}, fmt.Errorf("app.SetAgentStyle: %w", err)
	}
	if err := s.d.Store.Settings().Set(ctx, autonomyKey(v.ID), string(newAut), now); err != nil {
		return AgentStyleView{}, fmt.Errorf("app.SetAgentStyle: %w", err)
	}
	if err := s.storeAdapt(ctx, v.ID, domain.AggressivenessAdapt{}); err != nil {
		return AgentStyleView{}, fmt.Errorf("app.SetAgentStyle: %w", err)
	}
	details := fmt.Sprintf("aggressiveness %s→%s, autonomy %s→%s", oldAggr, newAggr, oldAut, newAut)
	s.audit(ctx, p, "agent.style_changed", v.ID, details)
	s.emitFor(ctx, v.ID, p.Username, "agent.style_changed", domain.SeverityInfo, "Agent style of "+v.Name+" changed",
		"aggressiveness "+string(newAggr)+", autonomy "+string(newAut))
	return s.styleView(ctx, p, v), nil
}

// presetOf names the preset limits l of account a match, or domain.PresetCustom.
func presetOf(a domain.Account, l domain.RiskLimits) domain.RiskPreset {
	return domain.MatchPreset(l, accountMode(a))
}

// RiskPresetView is one preset as an account in a given mode would get it.
type RiskPresetView struct {
	Preset  domain.RiskPreset
	Label   string
	Summary string
	Limits  domain.PresetLimits
}

// RiskPresetList is the presets for the current account, and which one its limits match.
type RiskPresetList struct {
	Presets []RiskPresetView
	// Current is the matching preset or domain.PresetCustom.
	Current domain.RiskPreset
	// Clamped: a real-money account, whose presets are capped at the real ceilings (OD25).
	Clamped bool
}

// RiskPresets lists the risk presets for the current account (view): on a real-money account
// each is capped at domain.RealCeilings.
func (s *Service) RiskPresets(ctx context.Context, p Principal) (RiskPresetList, error) {
	v, err := s.account(ctx, p, "", domain.AccessView)
	if err != nil {
		return RiskPresetList{}, err
	}
	m := accountMode(v.Account)
	out := RiskPresetList{Current: domain.MatchPreset(s.riskLimitsFor(ctx, v.Account), m), Clamped: m == domain.ModeReal}
	for _, pr := range domain.RiskPresets {
		l, _ := domain.PresetFor(pr, m)
		out.Presets = append(out.Presets, RiskPresetView{Preset: pr, Label: pr.Label(), Summary: l.Summary(), Limits: l})
	}
	return out, nil
}

// ApplyRiskPreset sets the current account's limits to a preset (domain.ApplyPreset: the
// budget is kept, the windows before the close never shortened) through SetRiskLimits, so
// every rule of a change of limits applies: owner, web session, agent stopped, no kill, the
// real ceilings, the budget against equity, and a raise disarms an armed real account.
func (s *Service) ApplyRiskPreset(ctx context.Context, p Principal, preset domain.RiskPreset) (domain.RiskLimits, error) {
	v, err := s.account(ctx, p, "", domain.AccessOwner)
	if err != nil {
		return domain.RiskLimits{}, err
	}
	next, err := domain.ApplyPreset(s.riskLimitsFor(ctx, v.Account), preset, accountMode(v.Account))
	if err != nil {
		return domain.RiskLimits{}, err
	}
	return s.SetRiskLimits(ctx, p, next)
}

// foldRiskDay folds a finished risk day into full autonomy's adjustment of the
// aggressiveness (called once the risk state moved to a later day). Other levels keep no
// adjustment. A change is announced.
func (s *Service) foldRiskDay(ctx context.Context, a domain.Account, day domain.RiskDate, latched bool) {
	if !s.policyOf(ctx, a).AdaptAggressiveness {
		return
	}
	old := s.adaptOf(ctx, a.ID)
	next := old.EndOfDay(day, latched)
	if next == old {
		return
	}
	if err := s.storeAdapt(ctx, a.ID, next); err != nil {
		s.d.Log.WarnContext(ctx, "storing the aggressiveness adjustment failed", "account", a.ID, "err", err)
		return
	}
	if next.Down == old.Down {
		return
	}
	chosen := s.aggressivenessOf(ctx, a.ID)
	now := chosen.Down(next.Down)
	why := "after a day that reached the daily loss limit"
	if next.Down < old.Down {
		why = fmt.Sprintf("after %d clean trading days", domain.AdaptCleanDays)
	}
	s.audit(ctx, Principal{Username: "autonomy"}, "agent.aggressiveness_adapted", a.ID, fmt.Sprintf("%s (chosen %s), %s", now, chosen, why))
	s.emitFor(ctx, a.ID, "autonomy", "agent.aggressiveness_adapted", domain.SeverityInfo,
		"Aggressiveness of "+a.Name+" is now "+string(now), "full autonomy, "+why+" (chosen: "+string(chosen)+")")
}

// markRan records that account a's agent ran on risk day d (full autonomy counts clean
// days only when the agent ran). Written only when it changes.
func (s *Service) markRan(ctx context.Context, a domain.Account, d domain.RiskDate) {
	if !s.policyOf(ctx, a).AdaptAggressiveness {
		return
	}
	cur := s.adaptOf(ctx, a.ID)
	if cur.RanOn == d {
		return
	}
	cur.RanOn = d
	if err := s.storeAdapt(ctx, a.ID, cur); err != nil {
		s.d.Log.WarnContext(ctx, "storing the agent's trading day failed", "account", a.ID, "err", err)
	}
}
