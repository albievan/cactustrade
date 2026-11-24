package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/app"
)

// Trade history and CSV exports (PLAN 6.7). Who may read what is decided by the use cases
// (view permission and view access to the account; an API key with the viewer role may
// read and export). Nothing here calls eToro.

// daysParam reads an optional period; an explicit 0 is out of range, not "the default".
func daysParam(d *HistoryDays) int {
	if d == nil {
		return 0
	}
	if *d == 0 {
		return -1
	}
	return *d
}

func timeParam(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

func toHistoryTrade(v app.TradeView) HistoryTrade {
	t := v.Trade
	out := HistoryTrade{
		PositionId: t.PositionID, InstrumentId: int64(t.Instrument), Symbol: v.Symbol, Direction: string(t.Direction()),
		Leverage: t.Leverage, Units: dec(t.Units), InvestedCents: Cents(t.Investment), OpenRate: dec(t.OpenRate),
		CloseRate: dec(t.CloseRate), StopLoss: optDec(t.StopLoss), TakeProfit: optDec(t.TakeProfit),
		NetProfitCents: Cents(t.NetProfit), FeesKnown: t.FeesKnown, OpenedAt: t.OpenedAt, ClosedAt: t.ClosedAt,
		Ours: t.Ours, OpenRequestId: t.OpenRequestID, EtoroOrderId: t.OrderID, Mode: TradingMode(t.Mode),
	}
	if t.FeesKnown {
		out.FeesCents = ptr(Cents(t.Fees))
	}
	return out
}

// ListHistoryTrades returns an account's closed trades, newest close first.
func (s *Server) ListHistoryTrades(w http.ResponseWriter, r *http.Request, params ListHistoryTradesParams) {
	q := app.TradeQuery{From: timeParam(params.From), To: timeParam(params.To), Before: timeParam(params.Before), Ours: params.Ours}
	if params.Limit != nil {
		if q.Limit = *params.Limit; q.Limit == 0 {
			q.Limit = -1 // an explicit 0 is out of range, not "the default"
		}
	}
	ts, err := s.svc.Trades(r.Context(), state(r).principal, accountParam(params.AccountId), q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]HistoryTrade, 0, len(ts))
	for _, t := range ts {
		out = append(out, toHistoryTrade(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetHistorySummary sums an account's closed trades and its drawdown over a period.
func (s *Server) GetHistorySummary(w http.ResponseWriter, r *http.Request, params GetHistorySummaryParams) {
	h, err := s.svc.HistorySummary(r.Context(), state(r).principal, accountParam(params.AccountId), daysParam(params.Days))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	st, dd := h.Stats, h.Drawdown
	out := HistorySummary{
		AccountId: h.Account.ID, Days: h.Days, From: h.From, To: h.To,
		Trades: st.Count, Wins: st.Wins, Losses: st.Losses, Breakeven: st.Breakeven,
		GrossCents: Cents(st.Gross), FeesCents: Cents(st.Fees), NetCents: Cents(st.Net), FeesUnknownTrades: st.FeesUnknown,
		ForeignTrades: h.ForeignTrades,
		Drawdown: HistoryDrawdown{
			Known: dd.Known, PeakCents: Cents(dd.Peak), MaxDrawdownCents: Cents(dd.MaxDrawdown), MaxDrawdownBp: int64(dd.MaxDrawdownBp),
			CurrentCents: Cents(dd.Current), CurrentBp: int64(dd.CurrentBp),
		},
	}
	if st.HasWinRate {
		out.WinRateBp = ptr(int64(st.WinRate))
	}
	if dd.Known {
		out.Drawdown.PeakAt = ptr(dd.PeakAt)
	}
	writeJSON(w, http.StatusOK, out)
}

// GetHistoryDaily returns an account's figures per risk day.
func (s *Server) GetHistoryDaily(w http.ResponseWriter, r *http.Request, params GetHistoryDailyParams) {
	h, err := s.svc.DailyPnL(r.Context(), state(r).principal, accountParam(params.AccountId), daysParam(params.Days))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := DailyHistory{AccountId: h.Account.ID, Days: h.Days, From: h.From, To: h.To, Rows: make([]HistoryDay, 0, len(h.Rows))}
	for _, d := range h.Rows {
		row := HistoryDay{
			Day: string(d.Day), Trades: d.Trades, RealisedCents: Cents(d.Realised), FeesCents: Cents(d.Fees), FeesUnknownTrades: d.FeesUnknown,
		}
		if d.EquityKnown {
			row.EquityEndCents, row.EquityChangeCents = ptr(Cents(d.EquityEnd)), ptr(Cents(d.EquityChange))
		}
		out.Rows = append(out.Rows, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// ExportHistory sends an account's history of one kind as a CSV attachment. Every error is
// answered as problem+json before the first byte of the file; the rows are then written as
// they are formatted.
func (s *Server) ExportHistory(w http.ResponseWriter, r *http.Request, file ExportHistoryParamsFile, params ExportHistoryParams) {
	if !file.Valid() {
		writeProblem(w, r, http.StatusNotFound, "Not found", "no such export: trades.csv, daily.csv, equity.csv, orders.csv or decisions.csv", "")
		return
	}
	kind := app.ExportKind(strings.TrimSuffix(string(file), ".csv"))
	e, err := s.svc.ExportRows(r.Context(), state(r).principal, accountParam(params.AccountId), kind, timeParam(params.From), timeParam(params.To))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="`+exportFileName(kind, e.Account.ID)+`"`)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if err := writeCSV(w, e); err != nil {
		// The status is sent: the client sees a cut file; the log says why (no row data).
		s.log.ErrorContext(r.Context(), "csv export failed", "kind", string(kind), "account", e.Account.ID, "err", err)
	}
}
