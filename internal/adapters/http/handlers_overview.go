package httpapi

import (
	"net/http"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

func toHoldings(h domain.Holdings) Holdings {
	out := Holdings{
		Platform: Platform(h.Platform), Currency: h.Currency, Cash: MinorUnits(h.Cash), Invested: MinorUnits(h.Invested),
		Equity: MinorUnits(h.Equity), UnrealisedPnl: MinorUnits(h.UnrealisedPnL), RealisedPnl: MinorUnits(h.RealisedPnL),
		Positions: make([]Holding, 0, len(h.Positions)), AsOf: h.AsOf,
	}
	for _, p := range h.Positions {
		x := Holding{
			Symbol: p.Symbol, Name: p.Name, Currency: p.Currency, Quantity: dec(p.Quantity), AvgPrice: dec(p.AvgPrice),
			CurrentPrice: dec(p.CurrentPrice), Value: MinorUnits(p.Value), Cost: MinorUnits(p.Cost), Pnl: MinorUnits(p.PnL),
			Long: p.Long, Cfd: p.CFD,
		}
		if !p.OpenedAt.IsZero() {
			x.OpenedAt = ptr(p.OpenedAt)
		}
		out.Positions = append(out.Positions, x)
	}
	return out
}

// GetOverview reads every account the caller can view.
func (s *Server) GetOverview(w http.ResponseWriter, r *http.Request) {
	ov, err := s.svc.Overview(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := Overview{Accounts: make([]AccountHoldings, 0, len(ov.Accounts)), Totals: make([]CurrencyTotal, 0, len(ov.Totals)), AsOf: ov.AsOf}
	for _, a := range ov.Accounts {
		x := AccountHoldings{Account: toAccount(a.Account), NoKeys: a.NoKeys}
		if a.Holdings != nil {
			x.Holdings = ptr(toHoldings(*a.Holdings))
		}
		if a.Error != "" {
			x.Error = ptr(a.Error)
		}
		out.Accounts = append(out.Accounts, x)
	}
	for _, t := range ov.Totals {
		out.Totals = append(out.Totals, CurrencyTotal{
			Currency: t.Currency, Accounts: t.Accounts, Cash: MinorUnits(t.Cash), Equity: MinorUnits(t.Equity), UnrealisedPnl: MinorUnits(t.UnrealisedPnL),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// GetEquityHistory returns the recorded equity per account.
func (s *Server) GetEquityHistory(w http.ResponseWriter, r *http.Request, params GetEquityHistoryParams) {
	days := 0
	if params.Days != nil {
		days = *params.Days
		if days == 0 {
			days = -1 // an explicit 0 is out of range, not "the default"
		}
	}
	series, err := s.svc.EquityHistory(r.Context(), state(r).principal, days)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSeries(series))
}

func toSeries(series []app.EquitySeries) []EquitySeries {
	out := make([]EquitySeries, 0, len(series))
	for _, sr := range series {
		x := EquitySeries{
			AccountId: sr.Account.ID, Name: sr.Account.Name, Platform: Platform(sr.Account.OnPlatform()), Mode: TradingMode(sr.Account.Mode),
			Currency: sr.Currency, Points: make([]EquityPoint, 0, len(sr.Points)),
		}
		for _, p := range sr.Points {
			x.Points = append(x.Points, EquityPoint{At: p.At, Cash: MinorUnits(p.Cash), Equity: MinorUnits(p.Equity)})
		}
		out = append(out, x)
	}
	return out
}
