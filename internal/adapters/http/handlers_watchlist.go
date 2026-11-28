package httpapi

import (
	"net/http"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// Watchlist themes, holdings, export and import (PLAN R27). Every rule lives in the app
// layer; these handlers only translate.

// maxImportBody caps an imported watchlist file (R27-f).
const maxImportBody = 1 << 20

func toWatchHolding(h app.WatchHolding) WatchHolding {
	return WatchHolding{
		InstrumentId: int64(h.Instrument), Symbol: h.Symbol, Name: h.Name, Market: string(h.Market),
		Positions: h.Positions, Units: dec(h.Units), ValueCents: Cents(h.Value), CostCents: Cents(h.Cost),
		PnlCents: Cents(h.PnL), Ours: h.Ours, Shorts: h.Shorts,
	}
}

// ListWatchlist lists the watchlist with each share's theme and holding.
func (s *Server) ListWatchlist(w http.ResponseWriter, r *http.Request) {
	items, err := s.svc.WatchlistEntries(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	matches, err := s.svc.WatchlistMatches(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]WatchItem, 0, len(items))
	for _, it := range items {
		x := toWatchItem(it.WatchItem)
		if m, ok := matches[it.Instrument.ID]; ok {
			x.BrokerMatch = ptr(toBrokerMatch(m))
		}
		if it.ThemeID != "" {
			x.ThemeId = ptr(it.ThemeID)
		}
		if it.Holding != nil {
			x.Holding = ptr(toWatchHolding(*it.Holding))
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, out)
}

// GetWatchlistOverview says whether the caller may edit and what the account holds.
func (s *Server) GetWatchlistOverview(w http.ResponseWriter, r *http.Request) {
	o, err := s.svc.WatchlistOverview(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	h := o.Holdings
	out := WatchlistOverview{CanEdit: o.CanEdit, Holdings: WatchlistHoldings{
		Known: h.Known, Stale: h.Stale, Currency: h.Currency, WatchlistValueCents: Cents(h.WatchlistValue),
		WatchlistPnlCents: Cents(h.WatchlistPnL), WatchlistPositions: h.WatchlistPositions,
		NotOnWatchlist: make([]WatchHolding, 0, len(h.NotOnWatchlist)),
	}}
	if h.Known {
		out.Holdings.AsOf = ptr(h.AsOf)
	}
	if h.Error != "" {
		out.Holdings.Error = ptr(h.Error)
	}
	for _, x := range h.NotOnWatchlist {
		out.Holdings.NotOnWatchlist = append(out.Holdings.NotOnWatchlist, toWatchHolding(x))
	}
	writeJSON(w, http.StatusOK, out)
}

// AssignWatchlistThemes puts watchlist shares into themes.
func (s *Server) AssignWatchlistThemes(w http.ResponseWriter, r *http.Request) {
	var req ThemeAssignmentsRequest
	if !decode(w, r, &req) {
		return
	}
	as := make([]app.ThemeAssignment, 0, len(req.Assignments))
	for _, a := range req.Assignments {
		as = append(as, app.ThemeAssignment{Instrument: domain.InstrumentID(a.InstrumentId), ThemeID: a.ThemeId})
	}
	n, err := s.svc.AssignThemes(r.Context(), state(r).principal, as)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ThemeAssignmentsResult{ChangedThemes: n})
}

// ProposeWatchlistThemes suggests a theme for each share without one.
func (s *Server) ProposeWatchlistThemes(w http.ResponseWriter, r *http.Request) {
	ps, err := s.svc.ProposeThemes(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := ThemeProposals{Proposals: make([]ThemeProposal, 0, len(ps.Proposals)), Unmatched: ps.Unmatched, Notes: ps.Notes}
	for _, x := range ps.Proposals {
		out.Proposals = append(out.Proposals, ThemeProposal{
			InstrumentId: int64(x.Instrument), Symbol: x.Symbol, ThemeId: x.ThemeID, ThemeName: x.ThemeName,
			Score: x.Match.Score, Reasons: x.Match.Reasons,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ExportWatchlist sends the watchlist and themes as a JSON file.
func (s *Server) ExportWatchlist(w http.ResponseWriter, r *http.Request) {
	f, err := s.svc.ExportWatchlist(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := WatchlistFile{
		Version: WatchlistFileVersion(f.Version), ExportedAt: f.ExportedAt, Account: WatchlistFileAccount{Name: f.AccountName},
		Watchlist: make([]WatchlistFileShare, 0, len(f.Watchlist)), Themes: make([]WatchlistFileTheme, 0, len(f.Themes)),
	}
	for _, x := range f.Watchlist {
		out.Watchlist = append(out.Watchlist, WatchlistFileShare{InstrumentId: int64(x.Instrument), Symbol: x.Symbol, Market: x.Market})
	}
	for _, t := range f.Themes {
		ft := WatchlistFileTheme{Name: t.Name, Keywords: t.Keywords, IndustryId: t.IndustryID, Instruments: make([]int64, 0, len(t.Instruments))}
		for _, id := range t.Instruments {
			ft.Instruments = append(ft.Instruments, int64(id))
		}
		if t.LLMProfileName != "" {
			ft.LlmProfileName = ptr(t.LLMProfileName)
		}
		out.Themes = append(out.Themes, ft)
	}
	h := w.Header()
	h.Set("Content-Disposition", `attachment; filename="cactustrade-watchlist-`+f.ExportedAt.Format("2006-01-02")+`.json"`)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// ImportWatchlist merges an uploaded watchlist file.
func (s *Server) ImportWatchlist(w http.ResponseWriter, r *http.Request) {
	var req WatchlistFile
	if !decodeLimit(w, r, &req, maxImportBody) {
		return
	}
	f := domain.WatchlistFile{
		Version: int(req.Version), ExportedAt: req.ExportedAt, AccountName: req.Account.Name,
		Watchlist: make([]domain.FileShare, 0, len(req.Watchlist)), Themes: make([]domain.FileTheme, 0, len(req.Themes)),
	}
	for _, x := range req.Watchlist {
		f.Watchlist = append(f.Watchlist, domain.FileShare{Instrument: domain.InstrumentID(x.InstrumentId), Symbol: x.Symbol, Market: x.Market})
	}
	for _, t := range req.Themes {
		ft := domain.FileTheme{Name: t.Name, Keywords: t.Keywords, IndustryID: t.IndustryId, Instruments: make([]domain.InstrumentID, 0, len(t.Instruments))}
		for _, id := range t.Instruments {
			ft.Instruments = append(ft.Instruments, domain.InstrumentID(id))
		}
		if t.LlmProfileName != nil {
			ft.LLMProfileName = *t.LlmProfileName
		}
		f.Themes = append(f.Themes, ft)
	}
	res, err := s.svc.ImportWatchlist(r.Context(), state(r).principal, f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toImportResult(res))
}

// toImportResult is an import's outcome on the wire.
func toImportResult(res app.ImportResult) WatchlistImportResult {
	skips := func(xs []app.ImportSkip) []ImportSkip {
		out := make([]ImportSkip, 0, len(xs))
		for _, x := range xs {
			k := ImportSkip{Name: x.Name, Reason: x.Reason}
			if x.Instrument > 0 {
				k.InstrumentId = ptr(int64(x.Instrument))
			}
			out = append(out, k)
		}
		return out
	}
	return WatchlistImportResult{
		Added: res.Added, Already: res.Already, Skipped: skips(res.Skipped), ThemesCreated: res.ThemesCreated,
		ThemesUpdated: res.ThemesUpdated, ThemesUnchanged: res.ThemesUnchanged, ThemesSkipped: skips(res.ThemesSkipped), Notes: res.Notes,
	}
}

// ListPlatformWatchlists lists the current account's eToro watchlists (R46, D38).
func (s *Server) ListPlatformWatchlists(w http.ResponseWriter, r *http.Request) {
	ws, err := s.svc.PlatformWatchlists(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]PlatformWatchlist, 0, len(ws))
	for _, x := range ws {
		out = append(out, PlatformWatchlist{Id: x.ID, Name: x.Name, Type: x.Type, IsDefault: x.IsDefault, Shares: len(x.Instruments), Others: x.Others})
	}
	writeJSON(w, http.StatusOK, out)
}

// ImportPlatformWatchlist adds one eToro watchlist's shares to the current account's list.
func (s *Server) ImportPlatformWatchlist(w http.ResponseWriter, r *http.Request, watchlistID string) {
	res, err := s.svc.ImportPlatformWatchlist(r.Context(), state(r).principal, watchlistID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toImportResult(res))
}
