package etoro

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/albievan/cactustrade/internal/domain"
)

// Watchlists reads the user's eToro watchlists (D38, R46): a single GET, nothing is written.
// Up to domain.MaxPlatformWatchlistItems items per list, eToro's maximum; built-in lists are
// not created as a side effect (ensureBuiltinWatchlists=false).
func (c *Client) Watchlists(ctx context.Context) ([]domain.PlatformWatchlist, error) {
	var resp struct {
		Watchlists []struct {
			WatchlistID   string `json:"watchlistId"`
			Name          string `json:"name"`
			WatchlistType string `json:"watchlistType"`
			IsDefault     bool   `json:"isDefault"`
			Items         []struct {
				ItemID   int64  `json:"itemId"`
				ItemType string `json:"itemType"`
			} `json:"items"`
		} `json:"watchlists"`
	}
	q := url.Values{
		"itemsPerPageForSingle":   {strconv.Itoa(domain.MaxPlatformWatchlistItems)},
		"ensureBuiltinWatchlists": {"false"},
		"addRelatedAssets":        {"false"},
	}
	if err := c.call(ctx, OpWatchlists, nil, q, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]domain.PlatformWatchlist, 0, len(resp.Watchlists))
	for _, w := range resp.Watchlists {
		pw := domain.PlatformWatchlist{ID: w.WatchlistID, Name: w.Name, Type: w.WatchlistType, IsDefault: w.IsDefault, Instruments: []domain.InstrumentID{}}
		seen := map[int64]bool{}
		for _, it := range w.Items {
			if !strings.EqualFold(it.ItemType, "Instrument") || it.ItemID <= 0 {
				pw.Others++
				continue
			}
			if !seen[it.ItemID] {
				seen[it.ItemID] = true
				pw.Instruments = append(pw.Instruments, domain.InstrumentID(it.ItemID))
			}
		}
		out = append(out, pw)
	}
	return out, nil
}
