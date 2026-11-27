package domain

// PlatformWatchlist is one of the user's watchlists as the trading platform keeps it (R46,
// owner's decision D38: read from eToro, never written). Instruments are the platform's
// instrument ids, in the platform's order; Others counts entries that are not instruments
// (people the user follows), which are never imported.
type PlatformWatchlist struct {
	ID          string
	Name        string
	Type        string // eToro: Static, Dynamic, RecentlyInvested, Default
	IsDefault   bool
	Instruments []InstrumentID
	Others      int
}

// MaxPlatformWatchlistItems is how many items are read per watchlist (eToro's maximum).
const MaxPlatformWatchlistItems = 1000
