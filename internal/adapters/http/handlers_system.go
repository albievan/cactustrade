package httpapi

import (
	"net/http"
	"time"

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
		ev := Event{Id: e.ID, At: e.At, Kind: e.Kind, Severity: EventSeverity(e.Severity), Title: e.Title, Actor: e.Actor}
		if e.Detail != "" {
			ev.Detail = ptr(e.Detail)
		}
		out.Events = append(out.Events, ev)
	}
	writeJSON(w, http.StatusOK, out)
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
	}
}

// GetRiskLimits returns the limits.
func (s *Server) GetRiskLimits(w http.ResponseWriter, r *http.Request) {
	l, err := s.svc.RiskLimits(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRiskLimits(l))
}

// SetRiskLimits changes the limits.
func (s *Server) SetRiskLimits(w http.ResponseWriter, r *http.Request) {
	var req RiskLimits
	if !decode(w, r, &req) {
		return
	}
	l, err := s.svc.SetRiskLimits(r.Context(), state(r).principal, domain.RiskLimits{
		TradingBudget: domain.Cents(req.TradingBudgetCents), DailyLoss: domain.BasisPoints(req.DailyLossBp),
		Drawdown: domain.BasisPoints(req.DrawdownBp), MaxPosition: domain.BasisPoints(req.MaxPositionBp),
		MaxOpenPositions: req.MaxOpenPositions, NoEntryBeforeClose: time.Duration(req.NoEntryBeforeCloseMinutes) * time.Minute,
		FlattenBeforeClose: time.Duration(req.FlattenBeforeCloseMinutes) * time.Minute,
		MaxPortfolioAge:    time.Duration(req.MaxPortfolioAgeSeconds) * time.Second, MaxQuoteAge: time.Duration(req.MaxQuoteAgeSeconds) * time.Second,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRiskLimits(l))
}
