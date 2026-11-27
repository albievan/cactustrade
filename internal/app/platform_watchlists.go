package app

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/albievan/cactustrade/internal/domain"
)

// PlatformWatchlists lists the current account's watchlists on eToro (R46, owner's decision
// D38: read, never written), for the import. Trade access, because only an import follows.
func (s *Service) PlatformWatchlists(ctx context.Context, p Principal) ([]domain.PlatformWatchlist, error) {
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return nil, err
	}
	if s.d.Watchlists == nil {
		return nil, fmt.Errorf("reading eToro watchlists is not available: %w", domain.ErrUnavailable)
	}
	ws, err := s.d.Watchlists.Watchlists(brokerCtx(ctx, v))
	if err != nil {
		return nil, fmt.Errorf("app.PlatformWatchlists: %w", err)
	}
	return ws, nil
}

// ImportPlatformWatchlist adds the shares of one of the account's eToro watchlists to its
// cactusTrade watchlist (trade access, web session only). Every share takes the same eToro
// check as adding by hand (exchange, eligibility at leverage 1); refused ones are reported,
// never added; people on the list are skipped. At most domain.MaxFileShares shares per
// import, like a file. Importing the same list again changes nothing.
func (s *Service) ImportPlatformWatchlist(ctx context.Context, p Principal, watchlistID string) (ImportResult, error) {
	if p.Via != ViaSession {
		return ImportResult{}, fmt.Errorf("importing a watchlist is only possible from the web UI: %w", domain.ErrForbidden)
	}
	ws, err := s.PlatformWatchlists(ctx, p)
	if err != nil {
		return ImportResult{}, err
	}
	i := slices.IndexFunc(ws, func(w domain.PlatformWatchlist) bool { return w.ID == watchlistID })
	if i < 0 {
		return ImportResult{}, fmt.Errorf("no eToro watchlist %q: %w", watchlistID, domain.ErrNotFound)
	}
	w := ws[i]
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return ImportResult{}, err
	}
	res := ImportResult{
		Added: []string{}, Already: []string{}, Skipped: []ImportSkip{}, ThemesCreated: []string{},
		ThemesUpdated: []string{}, ThemesUnchanged: []string{}, ThemesSkipped: []ImportSkip{}, Notes: []string{},
	}
	ids := w.Instruments
	if len(ids) > domain.MaxFileShares {
		res.Notes = append(res.Notes, fmt.Sprintf("only the first %d of %d shares were imported; import again after removing some", domain.MaxFileShares, len(ids)))
		ids = ids[:domain.MaxFileShares]
	}
	if w.Others > 0 {
		res.Notes = append(res.Notes, strconv.Itoa(w.Others)+" entries that are not shares (people) were left out")
	}
	shares := make([]domain.FileShare, 0, len(ids))
	for _, id := range ids {
		shares = append(shares, domain.FileShare{Instrument: id})
	}
	if err := s.importShares(ctx, v, p.Username, shares, false, &res); err != nil {
		return ImportResult{}, err
	}
	summary := fmt.Sprintf("eToro watchlist %q: %d added, %d already there, %d refused", w.Name, len(res.Added), len(res.Already), len(res.Skipped))
	s.audit(ctx, p, "watchlist.import_platform", v.ID, summary)
	s.emitFor(ctx, v.ID, p.Username, "watchlist.imported", domain.SeverityInfo, "eToro watchlist "+w.Name+" imported into "+v.Name, summary)
	return res, nil
}
