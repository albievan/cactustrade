package httpapi

import (
	"net/http"

	"github.com/albievan/cactustrade/internal/app"
)

// Trade minimums (PLAN R38). The use cases decide everything; these handlers only translate.

// GetTradeMinimums returns the current account's latest minimums check.
func (s *Server) GetTradeMinimums(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.TradeMinimums(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTradeMinimumsView(v))
}

// CheckTradeMinimums checks the minimums against the limits now (owner).
func (s *Server) CheckTradeMinimums(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.CheckTradeMinimums(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTradeMinimumsView(v))
}

// DecideTradeMinimums approves or declines a pending proposal (owner).
func (s *Server) DecideTradeMinimums(w http.ResponseWriter, r *http.Request, reviewID string) {
	var req DecideTradeMinimumsJSONRequestBody
	if !decode(w, r, &req) {
		return
	}
	v, err := s.svc.DecideTradeMinimums(r.Context(), state(r).principal, reviewID, req.Approve)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTradeMinimumsView(v))
}

func toTradeMinimumsView(v app.MinimumsView) TradeMinimumsView {
	out := TradeMinimumsView{CanDecide: v.CanDecide}
	if v.DecideHint != "" {
		out.DecideHint = ptr(v.DecideHint)
	}
	if r := v.Review; r != nil {
		m := r.Minimums
		x := MinimumsReview{
			Id: r.ID, At: r.At, Platform: string(m.Platform),
			LongCents: Cents(m.Long), ShortCents: Cents(m.Short), LongLowestCents: Cents(m.LongLowest), ShortLowestCents: Cents(m.ShortLowest),
			Instruments: m.Instruments, LongKnown: m.LongKnown, ShortKnown: m.ShortKnown, EquityCents: Cents(r.Equity),
			Current: toRiskLimits(r.Current), Proposed: toRiskLimits(r.Fit.Proposed),
			LongRoomCents: Cents(r.Fit.LongRoom), ShortRoomCents: Cents(r.Fit.ShortRoom),
			Changes: nonNil(r.Fit.Changes), Problems: nonNil(r.Fit.Problems), Status: MinimumsReviewStatus(r.Status),
		}
		if r.DecidedBy != "" {
			x.DecidedBy, x.DecidedAt = ptr(r.DecidedBy), ptr(r.DecidedAt)
		}
		out.Review = &x
	}
	return out
}
