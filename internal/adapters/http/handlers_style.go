package httpapi

import (
	"net/http"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// The agent's style (PLAN R28): risk presets, aggressiveness and autonomy. The use cases
// decide everything; these handlers only translate.

// GetRiskPresets lists the risk presets for the current account.
func (s *Server) GetRiskPresets(w http.ResponseWriter, r *http.Request) {
	l, err := s.svc.RiskPresets(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := RiskPresetList{Current: RiskPresetMatch(l.Current), Clamped: l.Clamped, Presets: make([]RiskPreset, 0, len(l.Presets))}
	for _, p := range l.Presets {
		out.Presets = append(out.Presets, RiskPreset{
			Id: RiskPresetId(p.Preset), Label: p.Label, Summary: p.Summary,
			DailyLossBp: int(p.Limits.DailyLoss), DrawdownBp: int(p.Limits.Drawdown), MaxPositionBp: int(p.Limits.MaxPosition),
			MaxOpenPositions: p.Limits.MaxOpenPositions, MaxShortBp: int(p.Limits.MaxShort),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// GetAgentStyle returns the current account's aggressiveness and autonomy.
func (s *Server) GetAgentStyle(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.AgentStyle(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAgentStyle(v))
}

// SetAgentStyle changes the current account's aggressiveness and/or autonomy.
func (s *Server) SetAgentStyle(w http.ResponseWriter, r *http.Request) {
	var req AgentStyleChange
	if !decode(w, r, &req) {
		return
	}
	var aggr domain.Aggressiveness
	var aut domain.Autonomy
	if req.Aggressiveness != nil {
		aggr = domain.Aggressiveness(*req.Aggressiveness)
	}
	if req.Autonomy != nil {
		aut = domain.Autonomy(*req.Autonomy)
	}
	v, err := s.svc.SetAgentStyle(r.Context(), state(r).principal, aggr, aut)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAgentStyle(v))
}

func toAgentStyle(v app.AgentStyleView) AgentStyle {
	out := AgentStyle{
		Aggressiveness: StyleLevel(v.Aggressiveness), EffectiveAggressiveness: StyleLevel(v.Effective), Autonomy: StyleLevel(v.Autonomy),
		Policy: AutonomyPolicy{
			ResearchMode: ResearchMode(v.Policy.Research), ScanEveryMinutes: int(v.Policy.ScanEvery / time.Minute),
			OwnerSharesOnly: v.Policy.OwnerSharesOnly, AutoAssignThemes: v.Policy.AutoAssignThemes, PruneStale: v.Policy.PruneStale,
			AutoStart: v.Policy.AutoStart, AdaptAggressiveness: v.Policy.AdaptAggressiveness,
		},
		CanEdit:              v.CanEdit,
		AggressivenessLevels: make([]StyleLevelText, 0, len(domain.AggressivenessLevels)),
		AutonomyLevels:       make([]StyleLevelText, 0, len(domain.AutonomyLevels)),
	}
	for _, a := range domain.AggressivenessLevels {
		out.AggressivenessLevels = append(out.AggressivenessLevels, StyleLevelText{Id: StyleLevel(a), Label: levelLabel(string(a)), Summary: a.Summary()})
	}
	for _, a := range domain.AutonomyLevels {
		out.AutonomyLevels = append(out.AutonomyLevels, StyleLevelText{Id: StyleLevel(a), Label: levelLabel(string(a)), Summary: a.Summary()})
	}
	if v.EditBlocked != "" {
		out.EditBlockedReason = ptr(v.EditBlocked)
	}
	return out
}

// levelLabel is a level's name for people ("medium" → "Medium").
func levelLabel(id string) string {
	if id == "" {
		return id
	}
	return string(id[0]-'a'+'A') + id[1:]
}
