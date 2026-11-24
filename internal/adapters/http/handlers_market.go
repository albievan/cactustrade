package httpapi

import (
	"net/http"

	"github.com/albievan/cactustrade/internal/domain"
)

func dec(m domain.Micros) Decimal { return m.String() }

func optDec(m domain.Micros) *Decimal {
	if m == 0 {
		return nil
	}
	d := dec(m)
	return &d
}

func toInstrument(in domain.Instrument) Instrument {
	return Instrument{
		InstrumentId: int64(in.ID), Symbol: in.Symbol, Name: in.Name, Type: in.Type,
		ExchangeId: ptr(in.ExchangeID), Market: Market(in.Market),
	}
}

func toWatchItem(w domain.WatchItem) WatchItem {
	el := Eligibility{RealLong: w.Eligibility.RealLong}
	if w.Eligibility.Reason != "" {
		el.Reason = ptr(w.Eligibility.Reason)
	}
	if w.Eligibility.MinPositionAmount > 0 {
		el.MinPositionCents = ptr(Cents(w.Eligibility.MinPositionAmount))
	}
	if !w.Eligibility.CheckedAt.IsZero() {
		el.CheckedAt = ptr(w.Eligibility.CheckedAt)
	}
	return WatchItem{
		Instrument: toInstrument(w.Instrument), Eligibility: el, AddedBy: w.AddedBy, AddedAt: w.AddedAt,
		Origin: WatchItemOrigin(domain.WatchOrigin(w.AddedBy)),
	}
}

// TestBrokerConnection calls eToro once with the current account's keys.
func (s *Server) TestBrokerConnection(w http.ResponseWriter, r *http.Request) {
	s.writeBrokerCheck(w, r, "")
}

func (s *Server) writeBrokerCheck(w http.ResponseWriter, r *http.Request, accountID string) {
	res, err := s.svc.TestConnection(r.Context(), state(r).principal, accountID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := BrokerCheck{
		Mode: TradingMode(res.Mode), Ok: res.OK, ConfiguredKeys: res.ConfiguredKeys,
		LatencyMs: res.Latency.Milliseconds(), CheckedAt: res.CheckedAt,
	}
	if res.OK {
		out.AvailableCashCents, out.EquityCents = ptr(Cents(res.AvailableCash)), ptr(Cents(res.Equity))
		out.Positions, out.NonConforming = ptr(res.Positions), ptr(res.NonConforming)
	} else {
		out.Error = ptr(res.Error)
	}
	writeJSON(w, http.StatusOK, out)
}

// GetPortfolio returns the account now.
func (s *Server) GetPortfolio(w http.ResponseWriter, r *http.Request) {
	pf, err := s.svc.Portfolio(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	mode := domain.ModeDemo
	if v, err := s.svc.CurrentAccount(r.Context(), state(r).principal); err == nil && v.Mode == domain.ModeReal {
		mode = domain.ModeReal
	}
	out := Portfolio{
		Mode: TradingMode(mode), AvailableCashCents: Cents(pf.AvailableCash),
		PendingOpenCents: Cents(pf.PendingOpen), EquityCents: Cents(pf.Equity()), AsOf: pf.AsOf,
		Positions: make([]Position, 0, len(pf.Positions)),
	}
	for _, p := range pf.Positions {
		out.Positions = append(out.Positions, Position{
			PositionId: p.ID, InstrumentId: int64(p.Instrument), OpenedAt: p.OpenedAt, OpenRate: dec(p.OpenRate),
			Units: dec(p.Units), AmountCents: Cents(p.Amount), UnrealisedPnlCents: Cents(p.UnrealisedPnL),
			StopLossRate: optDec(p.StopLossRate), TakeProfitRate: optDec(p.TakeProfitRate),
			RealAsset: p.RealAsset, Long: p.Long, Leverage: p.Leverage,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// GetMarketStatus reports today's sessions.
func (s *Server) GetMarketStatus(w http.ResponseWriter, r *http.Request) {
	ms, err := s.svc.MarketStatus(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]MarketSession, 0, len(ms))
	for _, m := range ms {
		x := MarketSession{Market: Market(m.Market), Trading: m.Trading, Open: m.Open}
		if m.Trading {
			x.Opens, x.Closes = ptr(m.Opens), ptr(m.Closes)
		}
		if m.Note != "" {
			x.Note = ptr(m.Note)
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, out)
}

// SearchInstruments searches eToro.
func (s *Server) SearchInstruments(w http.ResponseWriter, r *http.Request, params SearchInstrumentsParams) {
	res, err := s.svc.SearchInstruments(r.Context(), state(r).principal, params.Q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]Instrument, 0, len(res))
	for _, in := range res {
		out = append(out, toInstrument(in))
	}
	writeJSON(w, http.StatusOK, out)
}

// AddToWatchlist adds an eligible share.
func (s *Server) AddToWatchlist(w http.ResponseWriter, r *http.Request) {
	var req AddWatchRequest
	if !decode(w, r, &req) {
		return
	}
	it, err := s.svc.AddToWatchlist(r.Context(), state(r).principal, domain.InstrumentID(req.InstrumentId))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toWatchItem(it))
}

// RemoveFromWatchlist removes an instrument.
func (s *Server) RemoveFromWatchlist(w http.ResponseWriter, r *http.Request, instrumentID int64) {
	if err := s.svc.RemoveFromWatchlist(r.Context(), state(r).principal, domain.InstrumentID(instrumentID)); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListWatchlistQuotes returns bid/ask for the watchlist.
func (s *Server) ListWatchlistQuotes(w http.ResponseWriter, r *http.Request) {
	qs, err := s.svc.WatchlistQuotes(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]Quote, 0, len(qs))
	for _, q := range qs {
		out = append(out, Quote{InstrumentId: int64(q.Instrument), Bid: dec(q.Bid), Ask: dec(q.Ask), At: q.At, Delayed: q.Delayed})
	}
	writeJSON(w, http.StatusOK, out)
}
