package httpapi

import (
	"net/http"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// The Trading 212 practice account endpoints (PLAN R45.9): the price source, the instrument
// catalogue, the watchlist's matches and resolving an order whose outcome is unknown. Who may
// do what is decided by the use cases (owner, web session); CSRF is checked by the middleware
// for every cookie-authenticated mutation. Invalid input is 422, as on the agent endpoints.

// SetPriceSource chooses (or removes) the eToro price source of a Trading 212 practice account.
func (s *Server) SetPriceSource(w http.ResponseWriter, r *http.Request, accountID string) {
	var req PriceSourceRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.svc.SetT212PriceSource(r.Context(), state(r).principal, accountID, req.AccountId); err != nil {
		s.failAgent(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toT212Catalogue(c app.T212Catalogue) T212Catalogue {
	return T212Catalogue{Count: c.Count, Stale: c.Stale, FetchedAt: optTime(c.FetchedAt)}
}

// GetT212Catalogue reports the age of the stored Trading 212 instrument list.
func (s *Server) GetT212Catalogue(w http.ResponseWriter, r *http.Request, accountID string) {
	c, err := s.svc.T212CatalogueStatus(r.Context(), state(r).principal, accountID)
	if err != nil {
		s.failAgent(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toT212Catalogue(c))
}

// RefreshT212Catalogue fetches Trading 212's instrument list with the account's keys.
func (s *Server) RefreshT212Catalogue(w http.ResponseWriter, r *http.Request, accountID string) {
	c, err := s.svc.RefreshT212Instruments(r.Context(), state(r).principal, accountID)
	if err != nil {
		s.failAgent(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toT212Catalogue(c))
}

func toBrokerMatch(b app.BrokerMatch) BrokerMatch {
	out := BrokerMatch{InstrumentId: int64(b.Instrument), Ticker: b.Ticker, Isin: b.ISIN, Status: BrokerMatchStatus(b.Status), ConfirmedAt: optTime(b.ConfirmedAt)}
	if b.Name != "" {
		out.Name = ptr(b.Name)
	}
	if b.Currency != "" {
		out.Currency = ptr(b.Currency)
	}
	if b.Reason != "" {
		out.Reason = ptr(b.Reason)
	}
	return out
}

// ConfirmBrokerMatch confirms the Trading 212 instrument of a watchlist share.
func (s *Server) ConfirmBrokerMatch(w http.ResponseWriter, r *http.Request, instrumentID int64) {
	m, err := s.svc.ConfirmT212Match(r.Context(), state(r).principal, domain.InstrumentID(instrumentID))
	if err != nil {
		s.failAgent(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBrokerMatch(app.BrokerMatch{
		Instrument: m.Instrument, Ticker: m.Ticker, ISIN: m.ISIN, Status: app.MatchConfirmed, ConfirmedAt: m.ConfirmedAt,
	}))
}

// ResolveOrder says what became of a Trading 212 order whose outcome is unknown.
func (s *Server) ResolveOrder(w http.ResponseWriter, r *http.Request, requestID string) {
	var req ResolveOrderRequest
	if !decode(w, r, &req) {
		return
	}
	var id int64
	if req.BrokerOrderId != nil {
		id = *req.BrokerOrderId
	}
	o, err := s.svc.ResolveOrder(r.Context(), state(r).principal, requestID, id, req.NotPlaced != nil && *req.NotPlaced)
	if err != nil {
		s.failAgent(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrder(o))
}

// SettleOrder handles POST /api/v1/orders/{requestId}/settle: the owner settles the Trading
// 212 position a buy opened that Trading 212 does not list (web session; audited).
func (s *Server) SettleOrder(w http.ResponseWriter, r *http.Request, requestID string) {
	o, err := s.svc.SettleOrder(r.Context(), state(r).principal, requestID)
	if err != nil {
		s.failAgent(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrder(o))
}
