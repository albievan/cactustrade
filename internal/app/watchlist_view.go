package app

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// The watchlist page's view (PLAN R27): each share with its theme and what the account
// holds of it, and the holdings not on the watchlist. Holdings come from the portfolio view
// cache (PLAN 6.6a) — at most one eToro read per account every viewCacheTTL, shared with
// every other view — never from a read per row, and nothing here sizes or sends an order
// (CLAUDE.md §2.2; TestViewCacheReadersAreViewsOnly lists this file as a view).

// WatchHolding is what the account holds of one instrument, with its symbol and market.
type WatchHolding struct {
	domain.InstrumentHolding
	Symbol string
	Name   string
	Market domain.Market
}

// WatchlistEntry is a watchlist share with its theme ("" = none) and its holding (nil = the
// account holds none, or the portfolio is not known).
type WatchlistEntry struct {
	domain.WatchItem
	ThemeID string
	Holding *WatchHolding
}

// WatchlistHoldings sums up the account's holdings for the watchlist page. Known is false
// when no portfolio could be read (Error says why); Stale when it is older than
// liveStaleAfter. Amounts are USD cents (eToro accounts are in USD).
type WatchlistHoldings struct {
	Known              bool
	Stale              bool
	AsOf               time.Time
	Error              string
	Currency           string
	WatchlistValue     domain.Cents
	WatchlistPnL       domain.Cents
	WatchlistPositions int
	NotOnWatchlist     []WatchHolding
}

// WatchlistOverview is the page's header: whether the caller may edit (trade access) and the
// holdings summary.
type WatchlistOverview struct {
	CanEdit  bool
	Holdings WatchlistHoldings
}

// WatchlistEntries lists the current account's watchlist with each share's theme and holding
// (view).
func (s *Service) WatchlistEntries(ctx context.Context, p Principal) ([]WatchlistEntry, error) {
	v, err := s.account(ctx, p, "", domain.AccessView)
	if err != nil {
		return nil, err
	}
	items, err := s.d.Store.Watchlist().List(ctx, v.ID)
	if err != nil {
		return nil, fmt.Errorf("app.WatchlistEntries: %w", err)
	}
	themes, err := s.d.Store.Themes().List(ctx, v.ID)
	if err != nil {
		return nil, fmt.Errorf("app.WatchlistEntries: %w", err)
	}
	themeOf := shareThemes(themes)
	held, _ := s.watchHoldings(ctx, v)
	out := make([]WatchlistEntry, 0, len(items))
	for _, it := range items {
		e := WatchlistEntry{WatchItem: it, ThemeID: themeOf[it.Instrument.ID]}
		if h, ok := held[it.Instrument.ID]; ok {
			e.Holding = &WatchHolding{InstrumentHolding: h, Symbol: it.Instrument.Symbol, Name: it.Instrument.Name, Market: it.Instrument.Market}
		}
		out = append(out, e)
	}
	return out, nil
}

// WatchlistOverview says whether the caller may edit the current account's watchlist and
// themes, and what the account holds: the total on watchlist shares and every holding that
// is not on the watchlist (view).
func (s *Service) WatchlistOverview(ctx context.Context, p Principal) (WatchlistOverview, error) {
	v, err := s.account(ctx, p, "", domain.AccessView)
	if err != nil {
		return WatchlistOverview{}, err
	}
	out := WatchlistOverview{CanEdit: v.Access.AtLeast(domain.AccessTrade)}
	items, err := s.d.Store.Watchlist().List(ctx, v.ID)
	if err != nil {
		return WatchlistOverview{}, fmt.Errorf("app.WatchlistOverview: %w", err)
	}
	on := make(map[domain.InstrumentID]bool, len(items))
	for _, it := range items {
		on[it.Instrument.ID] = true
	}
	held, sum := s.watchHoldings(ctx, v)
	sum.NotOnWatchlist = []WatchHolding{}
	ids := make([]domain.InstrumentID, 0, len(held))
	for id := range held {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		h := held[id]
		if on[id] {
			sum.WatchlistValue += h.Value
			sum.WatchlistPnL += h.PnL
			sum.WatchlistPositions += h.Positions
			continue
		}
		wh := WatchHolding{InstrumentHolding: h, Symbol: "#" + strconv.FormatInt(int64(id), 10)}
		if in, _, err := s.d.Store.Instruments().Get(ctx, id); err == nil {
			wh.Symbol, wh.Name, wh.Market = in.Symbol, in.Name, in.Market
		}
		sum.NotOnWatchlist = append(sum.NotOnWatchlist, wh)
	}
	out.Holdings = sum
	return out, nil
}

// shareThemes maps each picked share to the first theme (in the store's order, by name)
// that holds it. R27-a: one theme per watchlist share; older data may hold a share in more
// than one, and the first one is shown until the share is assigned again.
func shareThemes(themes []domain.Theme) map[domain.InstrumentID]string {
	out := map[domain.InstrumentID]string{}
	for _, t := range themes {
		for _, id := range t.Instruments {
			if _, ok := out[id]; !ok {
				out[id] = t.ID
			}
		}
	}
	return out
}

// watchHoldings is the account's holdings per instrument from the view cache, and the
// summary's freshness. A failure is not an error: the page then shows holdings as unknown.
func (s *Service) watchHoldings(ctx context.Context, v domain.AccountView) (map[domain.InstrumentID]domain.InstrumentHolding, WatchlistHoldings) {
	sum := WatchlistHoldings{Currency: "USD", Stale: true}
	switch {
	case s.d.Broker == nil:
		sum.Error = "the eToro client is not configured"
		return nil, sum
	case !s.hasKeys(ctx, v.Account):
		sum.Error = "the account's eToro keys are not set"
		return nil, sum
	}
	pf, at, err := s.viewPortfolio(ctx, v)
	if err != nil {
		s.d.Log.WarnContext(ctx, "watchlist holdings: portfolio read failed", "account", v.ID, "err", err)
		sum.Error = "eToro's portfolio could not be read just now"
		return nil, sum
	}
	ours := map[int64]bool{}
	aps, err := s.d.Store.AgentPositions().ListOpen(ctx, v.ID)
	if err != nil {
		s.d.Log.WarnContext(ctx, "watchlist holdings: own positions unreadable", "account", v.ID, "err", err)
	}
	for _, ap := range aps {
		ours[ap.PositionID] = true
	}
	sum.Known, sum.AsOf = true, at.UTC()
	sum.Stale = s.d.Clock.Now().Sub(at) > liveStaleAfter
	out := map[domain.InstrumentID]domain.InstrumentHolding{}
	for _, h := range domain.HoldingsByInstrument(pf, ours) {
		out[h.Instrument] = h
	}
	return out, sum
}
