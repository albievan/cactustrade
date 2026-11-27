package httpapi

import (
	"net/http"
	"slices"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// ReloadSettings re-reads credentials from the environment and secret files.
func (s *Server) ReloadSettings(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.ReloadSettings(r.Context(), state(r).principal); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RestartServer restarts cactusTrade.
func (s *Server) RestartServer(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.RestartServer(r.Context(), state(r).principal); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// ListEvents returns the event feed.
func (s *Server) ListEvents(w http.ResponseWriter, r *http.Request, params ListEventsParams) {
	limit := 50
	if params.Limit != nil {
		limit = *params.Limit
	}
	f, err := s.svc.Events(r.Context(), state(r).principal, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := EventFeed{Unread: f.Unread, Events: make([]Event, 0, len(f.Events))}
	for _, e := range f.Events {
		out.Events = append(out.Events, toEvent(e))
	}
	writeJSON(w, http.StatusOK, out)
}

func toEvent(e domain.Event) Event {
	ev := Event{Id: e.ID, At: e.At, Kind: e.Kind, Severity: EventSeverity(e.Severity), Title: e.Title, Actor: e.Actor}
	if e.Detail != "" {
		ev.Detail = ptr(e.Detail)
	}
	if e.AccountID != "" {
		ev.AccountId = ptr(e.AccountID)
	}
	if e.Direction == domain.Long || e.Direction == domain.Short {
		ev.Direction = ptr(EventDirection(e.Direction))
	}
	return ev
}

// MarkEventsSeen clears the caller's unread count.
func (s *Server) MarkEventsSeen(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.MarkEventsSeen(r.Context(), state(r).principal); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toRiskLimits(l domain.RiskLimits) RiskLimits {
	return RiskLimits{
		TradingBudgetCents: Cents(l.TradingBudget), DailyLossBp: int(l.DailyLoss), DrawdownBp: int(l.Drawdown),
		MaxPositionBp: int(l.MaxPosition), MaxOpenPositions: l.MaxOpenPositions,
		NoEntryBeforeCloseMinutes: int(l.NoEntryBeforeClose / time.Minute), FlattenBeforeCloseMinutes: int(l.FlattenBeforeClose / time.Minute),
		MaxPortfolioAgeSeconds: int(l.MaxPortfolioAge / time.Second), MaxQuoteAgeSeconds: int(l.MaxQuoteAge / time.Second),
		MaxShortBp: int(l.MaxShort),
	}
}

// GetRiskLimits returns the limits and whether the caller may change them now (the use
// case decides; PLAN 6.14), with the preset they match (R28).
func (s *Server) GetRiskLimits(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.RiskLimitsView(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRiskLimitsView(v))
}

func toRiskLimitsView(v app.RiskLimitsView) RiskLimitsView {
	l := toRiskLimits(v.Limits)
	out := RiskLimitsView{
		TradingBudgetCents: l.TradingBudgetCents, DailyLossBp: l.DailyLossBp, DrawdownBp: l.DrawdownBp,
		MaxPositionBp: l.MaxPositionBp, MaxOpenPositions: l.MaxOpenPositions,
		NoEntryBeforeCloseMinutes: l.NoEntryBeforeCloseMinutes, FlattenBeforeCloseMinutes: l.FlattenBeforeCloseMinutes,
		MaxPortfolioAgeSeconds: l.MaxPortfolioAgeSeconds, MaxQuoteAgeSeconds: l.MaxQuoteAgeSeconds,
		MaxShortBp: l.MaxShortBp, CanEdit: v.CanEdit, Preset: RiskPresetMatch(v.Preset),
	}
	if v.EditBlocked != "" {
		out.EditBlockedReason = ptr(v.EditBlocked)
	}
	return out
}

// SetRiskLimits changes the limits: a preset alone, or every limit (R28). The use cases
// apply every rule; this only decodes.
func (s *Server) SetRiskLimits(w http.ResponseWriter, r *http.Request) {
	var req RiskLimitsChange
	if !decode(w, r, &req) {
		return
	}
	p := state(r).principal
	var err error
	if req.Preset != nil {
		if req.TradingBudgetCents != nil || req.DailyLossBp != nil || req.DrawdownBp != nil || req.MaxPositionBp != nil ||
			req.MaxOpenPositions != nil || req.NoEntryBeforeCloseMinutes != nil || req.FlattenBeforeCloseMinutes != nil ||
			req.MaxPortfolioAgeSeconds != nil || req.MaxQuoteAgeSeconds != nil || req.MaxShortBp != nil {
			s.fail(w, r, domain.Invalid("preset", "send a preset alone, or every limit without a preset"))
			return
		}
		_, err = s.svc.ApplyRiskPreset(r.Context(), p, domain.RiskPreset(*req.Preset))
	} else {
		l, ok := customLimits(req)
		if !ok {
			s.fail(w, r, domain.Invalid("preset", "send a preset, or every limit"))
			return
		}
		_, err = s.svc.SetRiskLimits(r.Context(), p, l)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v, err := s.svc.RiskLimitsView(r.Context(), p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRiskLimitsView(v))
}

// customLimits is a change with every limit given; ok is false when one is missing.
func customLimits(req RiskLimitsChange) (domain.RiskLimits, bool) {
	ints := []*int{
		req.DailyLossBp, req.DrawdownBp, req.MaxPositionBp, req.MaxOpenPositions, req.NoEntryBeforeCloseMinutes,
		req.FlattenBeforeCloseMinutes, req.MaxPortfolioAgeSeconds, req.MaxQuoteAgeSeconds, req.MaxShortBp,
	}
	if req.TradingBudgetCents == nil || slices.Contains(ints, nil) {
		return domain.RiskLimits{}, false
	}
	return domain.RiskLimits{
		TradingBudget: domain.Cents(*req.TradingBudgetCents), DailyLoss: domain.BasisPoints(*req.DailyLossBp),
		Drawdown: domain.BasisPoints(*req.DrawdownBp), MaxPosition: domain.BasisPoints(*req.MaxPositionBp),
		MaxOpenPositions: *req.MaxOpenPositions, NoEntryBeforeClose: time.Duration(*req.NoEntryBeforeCloseMinutes) * time.Minute,
		FlattenBeforeClose: time.Duration(*req.FlattenBeforeCloseMinutes) * time.Minute,
		MaxPortfolioAge:    time.Duration(*req.MaxPortfolioAgeSeconds) * time.Second, MaxQuoteAge: time.Duration(*req.MaxQuoteAgeSeconds) * time.Second,
		MaxShort: domain.BasisPoints(*req.MaxShortBp),
	}, true
}

// GetSystemHealth answers the trading health (PLAN 7.5): 200 when every loop is alive, else
// 503 with the same body naming the account.
func (s *Server) GetSystemHealth(w http.ResponseWriter, r *http.Request) {
	h, err := s.svc.SystemHealth(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := SystemHealth{
		Ok: h.OK(), At: h.At, StartedAt: h.StartedAt, AgentLoop: h.AgentLoop,
		LastSupervisorTick: optTime(h.LastTick), Accounts: make([]AccountHealth, 0, len(h.Accounts)), Problems: h.Problems,
	}
	if out.Problems == nil {
		out.Problems = []string{}
	}
	for _, a := range h.Accounts {
		ah := AccountHealth{
			AccountId: a.AccountID, Mode: TradingMode(a.Mode), Supervised: a.Supervised, AgentState: AgentState(a.AgentState),
			Killed: a.Killed, Reconciled: a.Reconciled, LeaseHeld: a.LeaseHeld, RealGateArmed: a.RealGateArmed,
			LastSupervisorPass: optTime(a.LastPass), LastCleanSupervisorPass: optTime(a.LastCleanPass), Ok: a.Problem == "",
		}
		for _, n := range a.Orders {
			ah.NonFinalOrders += n
		}
		if a.Name != "" {
			ah.Name = ptr(a.Name)
		}
		if a.Problem != "" {
			ah.Problem = ptr(a.Problem)
		}
		out.Accounts = append(out.Accounts, ah)
	}
	code := http.StatusOK
	if !out.Ok {
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, code, out)
}

// optTime is nil for the zero time.
func optTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
