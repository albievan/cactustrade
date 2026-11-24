package etoro

import (
	"net/http"
	"sync"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

// TestRecordAnswerReadsOnly: the RecordAnswer hook (cactustrade etoro-probe, PLAN 2.7) gets
// every successful read answer exactly as received, and never an account change's answer nor
// a refused read's.
func TestRecordAnswerReadsOnly(t *testing.T) {
	var mu sync.Mutex
	got := map[Op][]string{}
	e := newEnv(t, func(c *Config) {
		c.RecordAnswer = func(op Op, body []byte) {
			mu.Lock()
			defer mu.Unlock()
			got[op] = append(got[op], string(body))
		}
	})
	const rates = `{"results":[{"rate":{"instrumentId":1001,"bid":245.30,"ask":245.50,"date":"2026-11-23T15:00:00Z","quoteType":"realtime"}}]}`
	e.fake.route = func(*http.Request, int) (int, string, http.Header) { return 200, rates, nil }
	if _, err := e.c.Quotes(e.ctx(), []domain.InstrumentID{1001}); err != nil {
		t.Fatal(err)
	}
	// An account change answered 200 is never recorded.
	e.fake.route = func(*http.Request, int) (int, string, http.Header) {
		return 200, `{"token":"ad74d0fd-ca92-4974-a932-9668d29ec366"}`, nil
	}
	if err := e.c.Cancel(e.ctx(), 700001, testRequestID); err != nil {
		t.Fatal(err)
	}
	// A refused read is not recorded either (there is no answer to keep).
	e.fake.route = func(*http.Request, int) (int, string, http.Header) {
		return 401, `{"errorCode":"Unauthorized","errorMessage":"bad key"}`, nil
	}
	if _, err := e.c.Portfolio(e.ctx()); err == nil {
		t.Fatal("401 accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || len(got[OpRates]) != 1 || got[OpRates][0] != rates {
		t.Fatalf("recorded %v, want exactly the rates answer as received", got)
	}
}
