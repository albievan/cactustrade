package httpapi

import (
	"net/http"
	"strconv"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// The analysis page (R31). Who may see what is decided by the use cases (view permission,
// view access to every chosen account; another account is 404); here only shapes are
// converted.

func analysisRequest(preset *AnalysisPreset, from, to *string, ids *[]string) app.AnalysisRequest {
	req := app.AnalysisRequest{From: deref(from), To: deref(to)}
	if preset != nil {
		req.Preset = string(*preset)
	}
	if ids != nil {
		req.AccountIDs = *ids
	}
	return req
}

func toAnalysisTrade(r *domain.AnalysisTradeRef) *AnalysisTrade {
	if r == nil {
		return nil
	}
	return &AnalysisTrade{Account: r.Account, Symbol: r.Symbol, PositionId: r.PositionID, ClosedAt: r.ClosedAt, NetCents: Cents(r.Net)}
}

func toAnalysisGroup(g domain.AnalysisGroup) AnalysisGroup {
	s := g.Stats
	out := AnalysisGroup{
		Key: g.Key, Label: g.Label, Trades: s.Count, Wins: s.Wins, Losses: s.Losses, Breakeven: s.Breakeven,
		GrossCents: Cents(s.Gross), FeesCents: Cents(s.Fees), NetCents: Cents(s.Net), FeesUnknownTrades: s.FeesUnknown,
		Best: toAnalysisTrade(g.Best), Worst: toAnalysisTrade(g.Worst),
	}
	if s.HasWinRate {
		out.WinRateBp = ptr(int64(s.WinRate))
	}
	if g.HasAvg {
		out.AvgNetCents = ptr(Cents(g.AvgNet))
	}
	return out
}

func toAnalysisCounts(cs []domain.StatementCount) []AnalysisCount {
	out := make([]AnalysisCount, 0, len(cs))
	for _, c := range cs {
		out = append(out, AnalysisCount{Label: c.Label, N: c.N})
	}
	return out
}

func toAnalysis(an domain.Analysis) Analysis {
	w := an.Window
	out := Analysis{
		Preset: AnalysisPreset(w.Preset), From: w.From, To: w.To, FirstDay: string(w.FirstDay), LastDay: string(w.LastDay), Days: w.Days,
		Bucket: AnalysisBucket(w.Bucket), Title: an.Title, GeneratedAt: an.GeneratedAt, AllAccounts: an.AllAccounts, Scope: an.Scope,
		Accounts: make([]AnalysisAccount, 0, len(an.Accounts)), Totals: toAnalysisGroup(an.Totals), ForeignTrades: an.ForeignTrades,
		TradeAccounts: an.TradeAccounts, Groupings: make([]AnalysisGrouping, 0, len(an.Groupings)),
		Equity: make([]AnalysisEquity, 0, len(an.Equity)), EquityTotals: make([]AnalysisEquityTotal, 0, len(an.EquityTotals)),
		Curves: make([]AnalysisCurve, 0, len(an.Curves)),
		Risk: AnalysisRisk{
			KillSwitches: an.Risk.KillSwitches, KillResets: an.Risk.KillResets, DailyStops: an.Risk.DailyStops,
			LimitChanges: an.Risk.LimitChanges, ForceCloses: an.Risk.ForceCloses, RiskRefusals: an.Risk.RiskRefusals,
		},
		Decisions: toAnalysisCounts(an.Decisions), DecisionsTotal: an.DecisionsTotal, Verdicts: toAnalysisCounts(an.Verdicts),
		Llm: AnalysisLlm{
			Calls: an.LLM.Calls, Failed: an.LLM.Failed, InputTokens: an.LLM.InputTokens, OutputTokens: an.LLM.OutputTokens,
			KnownCostUsd: an.LLM.Cost.String(), CostUnknownCalls: an.LLM.CostUnknownCalls,
		},
		ActivityCapped: an.ActivityCapped, Narrative: make([]AnalysisSection, 0, len(an.Narrative)), Notes: nonNil(an.Notes),
	}
	if an.HasOursShare {
		out.OursShareBp = ptr(int64(an.OursShareBp))
	}
	for _, a := range an.Accounts {
		out.Accounts = append(out.Accounts, AnalysisAccount{Id: a.ID, Name: a.Name, Platform: a.Platform, Mode: TradingMode(a.Mode), ViewOnly: a.ViewOnly})
	}
	for _, g := range an.Groupings {
		gs := make([]AnalysisGroup, 0, len(g.Groups))
		for _, x := range g.Groups {
			gs = append(gs, toAnalysisGroup(x))
		}
		out.Groupings = append(out.Groupings, AnalysisGrouping{Dimension: AnalysisDimension(g.Dimension), Title: g.Title, Groups: gs})
	}
	for _, e := range an.Equity {
		out.Equity = append(out.Equity, AnalysisEquity{
			Account: e.Account, Currency: e.Currency, Known: e.Known, OpeningMinor: int64(e.Opening), ClosingMinor: int64(e.Closing),
			ChangeMinor: int64(e.Change), MaxDrawdownMinor: int64(e.Drawdown.MaxDrawdown), MaxDrawdownBp: int64(e.Drawdown.MaxDrawdownBp), ViewOnly: e.ViewOnly,
		})
	}
	for _, e := range an.EquityTotals {
		out.EquityTotals = append(out.EquityTotals, AnalysisEquityTotal{
			Currency: e.Currency, Accounts: e.Accounts, OpeningMinor: int64(e.Opening), ClosingMinor: int64(e.Closing), ChangeMinor: int64(e.Change),
		})
	}
	for _, c := range an.Curves {
		pts := make([]AnalysisPoint, 0, len(c.Points))
		for _, p := range c.Points {
			pts = append(pts, AnalysisPoint{At: p.At, Equity: int64(p.Equity)})
		}
		out.Curves = append(out.Curves, AnalysisCurve{
			Currency: c.Currency, Accounts: c.Accounts, Points: pts, MaxDrawdownMinor: int64(c.Drawdown.MaxDrawdown), MaxDrawdownBp: int64(c.Drawdown.MaxDrawdownBp),
		})
	}
	for _, s := range an.Narrative {
		out.Narrative = append(out.Narrative, AnalysisSection{Heading: s.Heading, Paragraphs: nonNil(s.Paragraphs)})
	}
	out.TradesCapped = an.TradesCapped
	out.Trades = make([]AnalysisTradeRow, 0, len(an.Trades))
	for _, t := range an.Trades {
		x := AnalysisTradeRow{
			AccountId: t.AccountID, Account: t.Account, PositionId: t.PositionID, Symbol: t.Symbol, Exchange: t.Exchange, Theme: t.Theme,
			Playbook: t.Playbook, Direction: AnalysisTradeRowDirection(t.Direction), OpenedAt: t.OpenedAt, ClosedAt: t.ClosedAt,
			InvestmentCents: Cents(t.Investment), NetCents: Cents(t.Net), FeesCents: Cents(t.Fees), FeesKnown: t.FeesKnown,
			Outcome: AnalysisTradeRowOutcome(t.Outcome), Ours: t.Ours,
		}
		if t.OpenRate > 0 {
			x.OpenRate = ptr(dec(t.OpenRate))
		}
		if t.CloseRate > 0 {
			x.CloseRate = ptr(dec(t.CloseRate))
		}
		out.Trades = append(out.Trades, x)
	}
	return out
}

// GetAnalysis returns the analysis of a period.
func (s *Server) GetAnalysis(w http.ResponseWriter, r *http.Request, params GetAnalysisParams) {
	an, err := s.svc.Analysis(r.Context(), state(r).principal, analysisRequest(params.Preset, params.From, params.To, params.AccountId))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAnalysis(an))
}

// GetAnalysisPdf sends the analysis as a one-page PDF attachment. Every error is answered as
// problem+json before the first byte of the file.
func (s *Server) GetAnalysisPdf(w http.ResponseWriter, r *http.Request, params GetAnalysisPdfParams) {
	f, err := s.svc.AnalysisPDF(r.Context(), state(r).principal, analysisRequest(params.Preset, params.From, params.To, params.AccountId))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/pdf")
	// The name is domain.AnalysisFileName: ASCII letters, digits, '.' and '-' only.
	h.Set("Content-Disposition", `attachment; filename="`+f.Name+`"`)
	h.Set("Content-Length", strconv.Itoa(len(f.Data)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(f.Data); err != nil {
		s.log.WarnContext(r.Context(), "analysis PDF not written", "err", err)
	}
}
