package etoro

import (
	"net/http"
	"slices"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

// TestWatchlistsReadOnly (R46/D38): one GET of the user's watchlists, instruments kept in
// order without duplicates, people counted apart, nothing else asked of eToro.
func TestWatchlistsReadOnly(t *testing.T) {
	e := newEnv(t)
	e.fake.route = func(r *http.Request, _ int) (int, string, http.Header) {
		return 200, `{"status":200,"isSucceeded":true,"meta":{"pageNumber":0,"itemsPerPage":1000},"watchlists":[
			{"watchlistId":"w1","name":"Tech","watchlistType":"Static","isDefault":true,"totalItems":4,"items":[
				{"itemId":1001,"itemType":"Instrument","market":{"symbolName":"AAPL","exchangeId":5}},
				{"itemId":77,"itemType":"Person"},
				{"itemId":2002,"itemType":"Instrument"},
				{"itemId":1001,"itemType":"Instrument"}]},
			{"watchlistId":"w2","name":"Empty","watchlistType":"RecentlyInvested","isDefault":false,"totalItems":0,"items":[]}]}`, nil
	}
	ws, err := e.c.Watchlists(e.ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 || ws[0].ID != "w1" || ws[0].Name != "Tech" || !ws[0].IsDefault || ws[0].Others != 1 ||
		!slices.Equal(ws[0].Instruments, []domain.InstrumentID{1001, 2002}) || len(ws[1].Instruments) != 0 || ws[1].Type != "RecentlyInvested" {
		t.Fatalf("watchlists %+v", ws)
	}
	reqs := e.fake.requests()
	if len(reqs) != 1 || reqs[0].Method != http.MethodGet || reqs[0].URL.Path != "/api/v1/watchlists" {
		t.Fatalf("requests %v", reqs)
	}
	q := reqs[0].URL.Query()
	if q.Get("itemsPerPageForSingle") != "1000" || q.Get("ensureBuiltinWatchlists") != "false" || reqs[0].Header.Get("x-request-id") == "" {
		t.Fatalf("query %v headers %v", q, reqs[0].Header)
	}
}
