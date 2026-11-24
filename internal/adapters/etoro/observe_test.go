package etoro

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

func TestStatusClass(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{0, "error"},
		{101, "error"},
		{200, "2xx"},
		{204, "2xx"},
		{302, "3xx"},
		{400, "4xx"},
		{404, "4xx"},
		{429, "429"},
		{500, "5xx"},
		{503, "5xx"},
		{600, "error"},
		{-1, "error"},
	}
	for _, tt := range tests {
		if got := StatusClass(tt.status); got != tt.want {
			t.Errorf("StatusClass(%d) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

type observed struct {
	mu    sync.Mutex
	calls []string
	waits []time.Duration
}

func (o *observed) request(op Op, mode domain.TradingMode, class string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, string(op)+"/"+string(mode)+"/"+class)
}

// TestObserveEveryRequest (PLAN 7.5): every request that left is reported once with its
// operation, mode and outcome class: answered or not, retried or not, reads and account
// changes alike; nothing that did not leave is reported.
func TestObserveEveryRequest(t *testing.T) {
	o := &observed{}
	e := newEnv(t, func(c *Config) { c.Observe, c.RequestTimeout = o.request, 50*time.Millisecond })
	e.fake.route = func(_ *http.Request, n int) (int, string, http.Header) {
		switch n {
		case 1:
			return 429, `{"errorCode":"TooManyRequests"}`, http.Header{"Retry-After": {"1"}}
		case 2:
			return stall, "", nil // no answer before the timeout
		case 3:
			return 503, `{}`, nil
		case 5:
			return 404, `{}`, nil
		}
		return 0, "", nil
	}
	if _, err := e.c.Portfolio(e.ctx()); err == nil {
		t.Fatal("three failures should give up")
	}
	if _, err := e.c.Portfolio(e.ctx()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.Portfolio(e.ctx()); err == nil {
		t.Fatal("404 is an error")
	}
	// an account change that is not sent (bad request id) is not reported; a sent one is
	if _, err := e.c.Close(e.ctx(), 1001, 1001, "not-a-uuid"); err == nil {
		t.Fatal("a bad request id was accepted")
	}
	if _, err := e.c.Close(e.ctx(), 1001, 1001, testRequestID); err != nil {
		t.Fatal(err)
	}
	want := []string{"portfolio/demo/429", "portfolio/demo/error", "portfolio/demo/5xx", "portfolio/demo/2xx", "portfolio/demo/4xx", "close_position/demo/2xx"}
	if !slices.Equal(o.calls, want) {
		t.Fatalf("observed %v, want %v", o.calls, want)
	}
	if len(e.fake.requests()) != len(want) {
		t.Fatalf("%d requests, %d observed", len(e.fake.requests()), len(o.calls))
	}
}

// TestObserveLimiterWaits: the client hands ObserveWait to every account's limiter, which
// reports each time it holds a request back.
func TestObserveLimiterWaits(t *testing.T) {
	o := &observed{}
	e := newEnv(t, func(c *Config) {
		c.ObserveWait = func(q QuotaGroup, d time.Duration) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.calls = append(o.calls, string(q))
			o.waits = append(o.waits, d)
		}
	})
	l := e.c.limiter("acct-1")
	ctx := context.Background()
	for range QuotaLimits[QuotaExecution] {
		if err := l.Wait(ctx, QuotaExecution); err != nil {
			t.Fatal(err)
		}
	}
	if len(o.calls) != 0 {
		t.Fatalf("waited within quota: %v", o.calls)
	}
	if err := l.Wait(ctx, QuotaExecution); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(o.calls, []string{string(QuotaExecution)}) || o.waits[0] != window {
		t.Fatalf("observed %v %v", o.calls, o.waits)
	}
}
