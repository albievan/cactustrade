package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/app"
)

func TestRateLimitClassify(t *testing.T) {
	tests := []struct {
		method, path, pattern string
		want                  rateClass
	}{
		{"POST", "/api/v1/agent/kill", "POST /api/v1/agent/kill", classNever},
		{"POST", "/api/v1/agent/force-close", "POST /api/v1/agent/force-close", classNever},
		{"POST", "/api/v1/agent/force-close-all", "POST /api/v1/agent/force-close-all", classNever},
		{"POST", "/api/v1/agent/pause", "POST /api/v1/agent/pause", classNever},
		{"POST", "/api/v1/agent/stop", "POST /api/v1/agent/stop", classNever},
		{"POST", "/api/v1/accounts/a1/real-gate/disarm", "POST /api/v1/accounts/{accountId}/real-gate/disarm", classNever},
		{"POST", "/api/v1/agent/start", "POST /api/v1/agent/start", classMutation},
		{"POST", "/api/v1/agent/reset", "POST /api/v1/agent/reset", classMutation},
		{"POST", "/api/v1/auth/login", "POST /api/v1/auth/login", classMutation},
		{"DELETE", "/api/v1/users/u1", "DELETE /api/v1/users/{userId}", classMutation},
		{"GET", "/api/v1/agent", "GET /api/v1/agent", classRead},
		{"GET", "/api/openapi.yaml", "GET /api/openapi.yaml", classRead},
		{"GET", "/api/v1/nope", "/api/", classRead},
		{"GET", "/api/v1/history/export/trades.csv", "GET /api/v1/history/export/{file}", classExport},
		{"GET", "/api/v1/watchlist/export", "GET /api/v1/watchlist/export", classExport},
		{"GET", "/api/v1/watchlist/theme-proposals", "GET /api/v1/watchlist/theme-proposals", classExport},
		{"POST", "/api/v1/watchlist/import", "POST /api/v1/watchlist/import", classMutation},
		{"GET", "/api/v1/watchlist/overview", "GET /api/v1/watchlist/overview", classRead},
		{"GET", "/api/v1/live", "GET /api/v1/live", classStream},
		{"GET", "/healthz", "GET /healthz", classNever},
		{"GET", "/metrics", "GET /metrics", classNever},
		{"GET", "/", "/", classNever},
	}
	for _, tc := range tests {
		if got := classify(tc.method, tc.path, tc.pattern); got != tc.want {
			t.Errorf("classify(%s %s) = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

// TestRateLimitPerClass: each class allows its number per minute (OD33), refills one token
// every minute/n, and takes a token from all of a request's buckets or from none.
func TestRateLimitPerClass(t *testing.T) {
	tests := []struct {
		class rateClass
		n     int
	}{
		{classRead, 600}, {classMutation, 60}, {classExport, 10}, {classStream, 30},
	}
	for _, tc := range tests {
		t.Run(string(tc.class), func(t *testing.T) {
			l := newRateLimiter(RateLimits{})
			now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
			l.now = func() time.Time { return now }
			for i := range tc.n {
				if ok, _ := l.Allow(tc.class, "ip:1.1.1.1", "user:u1"); !ok {
					t.Fatalf("refused at %d of %d", i+1, tc.n)
				}
			}
			ok, wait := l.Allow(tc.class, "ip:1.1.1.1", "user:u1")
			if step := time.Minute / time.Duration(tc.n); ok || wait != step {
				t.Fatalf("over the limit: ok=%v wait=%s, want %s", ok, wait, step)
			}
			// the same user from another address: the user's bucket is empty
			if ok, _ := l.Allow(tc.class, "ip:2.2.2.2", "user:u1"); ok {
				t.Fatal("the principal's bucket follows it across addresses")
			}
			// another user from the first address: the address's bucket is empty
			if ok, _ := l.Allow(tc.class, "ip:1.1.1.1", "user:u2"); ok {
				t.Fatal("the address's bucket holds for every principal")
			}
			// neither refusal took a token from the other buckets
			if ok, _ := l.Allow(tc.class, "ip:2.2.2.2", "user:u2"); !ok {
				t.Fatal("a refused request took tokens")
			}
			// other classes are untouched
			other := classRead
			if tc.class == classRead {
				other = classMutation
			}
			if ok, _ := l.Allow(other, "ip:1.1.1.1", "user:u1"); !ok {
				t.Fatal("classes share a bucket")
			}
			now = now.Add(wait)
			if ok, _ := l.Allow(tc.class, "ip:1.1.1.1", "user:u1"); !ok {
				t.Fatal("no refill")
			}
		})
	}
}

// TestRateLimitBoundedMemory: 10 001 keys keep at most 10 000; Sweep drops refilled ones.
func TestRateLimitBoundedMemory(t *testing.T) {
	l := newRateLimiter(RateLimits{})
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	for i := range maxLimiterKeys + 1 {
		now = now.Add(time.Microsecond)
		if ok, _ := l.Allow(classRead, fmt.Sprintf("ip:%d", i)); !ok {
			t.Fatalf("key %d refused", i)
		}
	}
	if n := l.Len(); n > maxLimiterKeys {
		t.Fatalf("%d keys held, want ≤ %d", n, maxLimiterKeys)
	}
	if _, held := l.tat["read:ip:0"]; held {
		t.Fatal("the fullest bucket should have been dropped")
	}
	now = now.Add(time.Minute)
	if n := l.Sweep(); n == 0 || l.Len() != 0 {
		t.Fatalf("swept %d, %d left", n, l.Len())
	}
}

// TestRateLimitHTTP: per class over HTTP; 429 is problem+json with Retry-After and counted.
func TestRateLimitHTTP(t *testing.T) {
	x := newAgentTCServer(t, func(*app.Deps) {}, Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, MetricsToken: "mt"}, slog.New(slog.DiscardHandler))
	x.h.rates = newRateLimiter(RateLimits{Read: 3, Mutation: 2, Export: 1, Stream: 1})
	codes := func(n int, method, path string, hdr ...string) string {
		var out []string
		for range n {
			code, m, h := x.do(method, path, "", hdr...)
			if code == 429 && (h.Get("Retry-After") == "" || m["title"] != "Too many requests" || h.Get("Content-Type") != "application/problem+json") {
				t.Fatalf("429 %v %v", m, h)
			}
			out = append(out, fmt.Sprint(code))
		}
		return strings.Join(out, " ")
	}
	if got := codes(4, "GET", "/api/v1/agent"); got != "200 200 200 429" {
		t.Fatalf("reads: %s", got)
	}
	if got := codes(3, "POST", "/api/v1/agent/reconcile", x.csrfHdr()...); got != "200 200 429" {
		t.Fatalf("mutations: %s", got)
	}
	if got := codes(2, "GET", "/api/v1/history/export/trades.csv"); got != "200 429" {
		t.Fatalf("exports: %s", got)
	}
	// the user's buckets follow them to another address
	if got := codes(1, "GET", "/api/v1/agent", "X-Forwarded-For", "198.51.100.7"); got != "429" {
		t.Fatalf("user from elsewhere: %s", got)
	}
	// the address's buckets hold for anonymous callers too (here: the sign-in page)
	if code := x.anon("GET", "/api/v1/public/status", ""); code != 429 {
		t.Fatalf("anonymous from the same address: %d", code)
	}
	// the web app, health and metrics are not limited
	if code, _, _ := x.do("GET", "/healthz", ""); code != 200 {
		t.Fatalf("healthz %d", code)
	}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", x.srv.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer mt")
	resp, err := x.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{`cactustrade_http_rate_limited_total{class="read"}`, `cactustrade_http_rate_limited_total{class="mutation"} 1`, `cactustrade_http_rate_limited_total{class="export"} 1`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("metrics lack %s", want)
		}
	}
}

// TestRateLimitNeverThrottlesStopping (§2.6): with every bucket of every class exhausted for
// the address, the user and the API key, pause, stop and kill still work, from the web UI
// and with an API key.
func TestRateLimitNeverThrottlesStopping(t *testing.T) {
	x := newAgentTC(t)
	x.ready(t)
	code, m, _ := x.do("POST", "/api/v1/api-keys", `{"name":"ops","role":"admin","expiresInDays":30}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create key %d %v", code, m)
	}
	key, keyID := m["secret"].(string), m["apiKey"].(map[string]any)["id"].(string)
	_, me, _ := x.do("GET", "/api/v1/auth/me", "")
	uid := me["user"].(map[string]any)["id"].(string)
	if code, m, _ := x.do("POST", "/api/v1/agent/start", "", x.csrfHdr()...); code != 200 {
		t.Fatalf("start %d %v", code, m)
	}
	for _, c := range []rateClass{classRead, classMutation, classExport, classStream} {
		for _, id := range []string{"ip:127.0.0.1", "user:" + uid, "key:" + keyID} {
			for {
				if ok, _ := x.h.rates.Allow(c, id); !ok {
					break
				}
			}
		}
	}
	if code, _, _ := x.do("GET", "/api/v1/agent", ""); code != 429 {
		t.Fatalf("reads not exhausted: %d", code)
	}
	if code, _ := x.bearer("POST", "/api/v1/agent/reconcile", "", key); code != 429 {
		t.Fatalf("key mutations not exhausted: %d", code)
	}
	if code, m, _ := x.do("POST", "/api/v1/agent/pause", "", x.csrfHdr()...); code != 200 || m["state"] != "paused" {
		t.Fatalf("pause %d %v", code, m)
	}
	if code, body := x.bearer("POST", "/api/v1/agent/stop", "", key); code != 200 || !strings.Contains(body, `"state":"stopped"`) {
		t.Fatalf("stop with a key %d %s", code, body)
	}
	if code, m, _ := x.do("POST", "/api/v1/agent/kill", `{"reason":"limits exhausted"}`, x.csrfHdr()...); code != 200 || m["already"] != false {
		t.Fatalf("kill %d %v", code, m)
	}
	if code, body := x.bearer("POST", "/api/v1/agent/kill", `{"reason":"again"}`, key); code != 200 || !strings.Contains(body, `"already":true`) {
		t.Fatalf("kill with a key %d %s", code, body)
	}
}
