package app

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// TestImportPlatformWatchlist (R46/D38): an eToro watchlist's shares go through the same
// check as adding by hand; refused ones are reported, people skipped, a second import
// changes nothing; only a web session with trade access may import; audited.
func TestImportPlatformWatchlist(t *testing.T) {
	ctx := context.Background()
	e, b := withBroker(t)
	b.Known[2] = domain.Instrument{ID: 2, Symbol: "BARC.L", Name: "Barclays Bank PLC", Type: "Stocks", Market: domain.MarketLSE}
	wl := &fakes.Watchlists{Lists: []domain.PlatformWatchlist{
		{ID: "w1", Name: "Tech", Type: "Static", Instruments: []domain.InstrumentID{1, 2, 3, 4, 5}, Others: 1},
	}}
	if _, err := e.svc.PlatformWatchlists(ctx, e.admin); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("without the port: %v", err)
	}
	e.svc.d.Watchlists = wl
	ws, err := e.svc.PlatformWatchlists(ctx, e.admin)
	if err != nil || len(ws) != 1 || ws[0].Name != "Tech" {
		t.Fatalf("lists %+v %v", ws, err)
	}
	res, err := e.svc.ImportPlatformWatchlist(ctx, e.admin, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Added, []string{"AAPL", "BARC.L"}) || len(res.Skipped) != 3 || len(res.Notes) != 1 {
		t.Fatalf("first import %+v", res)
	}
	res, err = e.svc.ImportPlatformWatchlist(ctx, e.admin, "w1")
	if err != nil || len(res.Added) != 0 || !slices.Equal(res.Already, []string{"AAPL", "BARC.L"}) {
		t.Fatalf("second import %+v %v", res, err)
	}
	if _, err := e.svc.ImportPlatformWatchlist(ctx, e.admin, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown list: %v", err)
	}
	if _, err := e.svc.ImportPlatformWatchlist(ctx, apiKey(e.admin), "w1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an API key imported: %v", err)
	}
	viewer := e.user(t, "viewer1", domain.RoleViewer)
	if _, err := e.svc.PlatformWatchlists(ctx, viewer); err == nil {
		t.Fatal("a viewer read the eToro watchlists")
	}
	wl.Err = domain.ErrUnavailable
	if _, err := e.svc.ImportPlatformWatchlist(ctx, e.admin, "w1"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("eToro down: %v", err)
	}
	entries, _ := e.store.Audit().List(ctx, 50)
	if !slices.ContainsFunc(entries, func(a domain.AuditEntry) bool { return a.Action == "watchlist.import_platform" }) {
		t.Fatal("not audited")
	}
}

// TestImportPlatformWatchlistCap: at most MaxFileShares shares per import, with a note.
func TestImportPlatformWatchlistCap(t *testing.T) {
	e, _ := withBroker(t)
	ids := make([]domain.InstrumentID, domain.MaxFileShares+3)
	for i := range ids {
		ids[i] = domain.InstrumentID(1000 + i)
	}
	e.svc.d.Watchlists = &fakes.Watchlists{Lists: []domain.PlatformWatchlist{{ID: "big", Name: "Big", Instruments: ids}}}
	res, err := e.svc.ImportPlatformWatchlist(context.Background(), e.admin, "big")
	if err != nil || len(res.Skipped) != domain.MaxFileShares || len(res.Notes) != 1 {
		t.Fatalf("cap: %d skipped, notes %v, %v", len(res.Skipped), res.Notes, err)
	}
}
