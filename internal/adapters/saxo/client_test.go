package saxo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// fakeSaxo is one environment: the API under /sim/openapi or /openapi and the sign-in host
// under /auth. It records every request.
type fakeSaxo struct {
	t       *testing.T
	apiBase string
	mu      sync.Mutex
	reqs    []*http.Request
	forms   []url.Values
	tokens  int
	// route overrides the answer: status 0 = default behaviour.
	route func(r *http.Request, n int) (int, string, http.Header)
}

func (f *fakeSaxo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	n := len(f.reqs)
	if r.URL.Path == "/auth/token" {
		form, _ := url.ParseQuery(string(body))
		f.forms = append(f.forms, form)
	}
	f.mu.Unlock()
	if f.route != nil {
		if status, b, h := f.route(r, n); status != 0 {
			for k, v := range h {
				w.Header()[k] = v
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, b)
			return
		}
	}
	switch r.URL.Path {
	case "/auth/token":
		f.mu.Lock()
		f.tokens++
		k := f.tokens
		f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"access_token":"access-%d","token_type":"Bearer","expires_in":1200,"refresh_token":"refresh-%d","refresh_token_expires_in":2400}`, k, k)
		return
	case f.apiBase + "/port/v1/balances/me":
		f.serve(w, "balances.json")
	case f.apiBase + "/port/v1/positions/me":
		f.serve(w, "positions.json")
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeSaxo) serve(w http.ResponseWriter, name string) {
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		f.t.Errorf("fixture %s: %v", name, err)
	}
	_, _ = w.Write(data)
}

func (f *fakeSaxo) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if path == "" || r.URL.Path == path {
			n++
		}
	}
	return n
}

type env struct {
	c         *Client
	sim, live *fakeSaxo
	sec       *fakes.SecretStore
	mu        sync.Mutex
	now       time.Time
	slept     []time.Duration
	logs      strings.Builder
}

var (
	simAcct  = domain.Account{ID: "s-sim", Name: "Saxo SIM", Mode: domain.ModeDemo, Platform: domain.PlatformSaxo}
	liveAcct = domain.Account{ID: "s-live", Name: "Saxo live", Mode: domain.ModeReal, Platform: domain.PlatformSaxo}
)

func cred(a domain.Account, key string) domain.CredentialName {
	n, _ := domain.AccountCredentialName(a, key)
	return n
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{now: time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)}
	e.sim, e.live = &fakeSaxo{t: t, apiBase: "/sim/openapi"}, &fakeSaxo{t: t, apiBase: "/openapi"}
	e.sec = &fakes.SecretStore{Now: e.clock}
	ss, ls := httptest.NewServer(e.sim), httptest.NewServer(e.live)
	t.Cleanup(ss.Close)
	t.Cleanup(ls.Close)
	ctx := context.Background()
	for _, a := range []domain.Account{simAcct, liveAcct} {
		_ = e.sec.Put(ctx, cred(a, domain.SaxoAppKey), "appkey-"+a.ID)
		_ = e.sec.Put(ctx, cred(a, domain.SaxoAppSecret), "appsecret-"+a.ID)
		_ = e.sec.Put(ctx, cred(a, domain.SaxoRefreshToken), "refresh-0")
	}
	c, err := New(Config{
		SimBaseURL: ss.URL + "/sim/openapi", SimAuthURL: ss.URL + "/auth", LiveBaseURL: ls.URL + "/openapi", LiveAuthURL: ls.URL + "/auth",
		HTTP: ss.Client(), Secrets: e.sec,
		Log: slog.New(slog.NewTextHandler(&lockedWriter{e: e}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now: e.clock,
		Sleep: func(_ context.Context, d time.Duration) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.slept = append(e.slept, d)
			e.now = e.now.Add(d)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.c = c
	return e
}

func (e *env) clock() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }

func (e *env) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }

type lockedWriter struct{ e *env }

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.e.mu.Lock()
	defer w.e.mu.Unlock()
	return w.e.logs.Write(p)
}

func ctxFor(a domain.Account) context.Context { return ports.WithAccount(context.Background(), a) }

// secretsIn reports any secret of the env that appears in s.
func secretsIn(s string) bool {
	for _, x := range []string{"appsecret-", "refresh-", "access-", "devtoken-secret"} {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

func TestHoldingsWithRefreshToken(t *testing.T) {
	e := newEnv(t)
	h, err := e.c.Holdings(ctxFor(simAcct))
	if err != nil {
		t.Fatal(err)
	}
	if h.Platform != domain.PlatformSaxo || h.Currency != "AUD" || h.Cash != 1_023_456 || h.Equity != 2_589_012 ||
		h.Invested != 1_012_500+275_530 || h.UnrealisedPnL != 42_500+15_025 || len(h.Positions) != 3 || !h.AsOf.Equal(e.now) {
		t.Fatalf("holdings = %+v", h)
	}
	cba, aapl, msft := h.Positions[0], h.Positions[1], h.Positions[2]
	if cba.Symbol != "CBA:xasx" || cba.Name != "Commonwealth Bank of Australia" || cba.Currency != "AUD" || cba.Quantity != 100_000_000 ||
		cba.AvgPrice != 101_250_000 || cba.CurrentPrice != 105_500_000 || cba.Value != 1_055_000 || cba.PnL != 42_500 || cba.Cost != 1_012_500 ||
		!cba.Long || cba.CFD || cba.Unknown || !cba.OpenedAt.Equal(time.Date(2026, 11, 20, 23, 15, 2, 123_000_000, time.UTC)) {
		t.Fatalf("CBA = %+v", cba)
	}
	if aapl.Currency != "USD" || aapl.Value != 290_555 || aapl.PnL != 15_025 || aapl.Unknown || aapl.CurrentPrice != 190_000_000 {
		t.Fatalf("AAPL (base-currency values) = %+v", aapl)
	}
	// a USD position on an AUD account without base-currency values: unknown, not guessed
	if !msft.Unknown || msft.Value != 0 || msft.PnL != 0 || msft.Long || !msft.CFD || msft.Quantity != 5_000_000 || msft.AvgPrice != 192_400_000 {
		t.Fatalf("MSFT = %+v", msft)
	}
	// the refresh: a form POST to the SIM sign-in host with the app's key and secret
	if e.sim.count("/auth/token") != 1 || e.live.count("") != 0 {
		t.Fatalf("token requests: sim %d live %d", e.sim.count("/auth/token"), e.live.count(""))
	}
	tok := e.sim.reqs[0]
	if tok.Method != http.MethodPost || tok.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Fatalf("token request %s %s", tok.Method, tok.Header.Get("Content-Type"))
	}
	f := e.sim.forms[0]
	if f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "refresh-0" || f.Get("client_id") != "appkey-s-sim" || f.Get("client_secret") != "appsecret-s-sim" {
		t.Fatalf("token form = %v", f)
	}
	// rotation: the new refresh token is stored
	if v, _ := e.sec.Get(context.Background(), cred(simAcct, domain.SaxoRefreshToken)); v != "refresh-1" {
		t.Fatalf("stored refresh token = %q", v)
	}
	get := e.sim.reqs[1]
	if get.Method != http.MethodGet || get.Header.Get("Authorization") != "Bearer access-1" || get.URL.Path != "/sim/openapi/port/v1/balances/me" {
		t.Fatalf("GET %s %s %q", get.Method, get.URL.Path, get.Header.Get("Authorization"))
	}
	pos := e.sim.reqs[2].URL.Query()
	if pos.Get("FieldGroups") != "PositionBase,PositionView,DisplayAndFormat" || pos.Get("$top") != "100" || pos.Get("$skip") != "0" {
		t.Fatalf("positions query = %v", pos)
	}
	if secretsIn(e.logs.String()) {
		t.Fatal("a token or secret reached the log")
	}

	// the access token is reused while fresh, refreshed early (5 min before it expires)
	if _, err := e.c.Holdings(ctxFor(simAcct)); err != nil || e.sim.count("/auth/token") != 1 {
		t.Fatalf("second read: %v, %d token requests", err, e.sim.count("/auth/token"))
	}
	e.advance(16 * time.Minute)
	if _, err := e.c.Holdings(ctxFor(simAcct)); err != nil || e.sim.count("/auth/token") != 2 || e.sim.forms[1].Get("refresh_token") != "refresh-1" {
		t.Fatalf("early refresh: %v, %d token requests", err, e.sim.count("/auth/token"))
	}
}

func TestRealAccountUsesLive(t *testing.T) {
	e := newEnv(t)
	if _, err := e.c.Holdings(ctxFor(liveAcct)); err != nil {
		t.Fatal(err)
	}
	if e.sim.count("") != 0 || e.live.count("/auth/token") != 1 || e.live.count("/openapi/port/v1/balances/me") != 1 {
		t.Fatalf("sim %d live %d", e.sim.count(""), e.live.count(""))
	}
	if e.live.forms[0].Get("client_id") != "appkey-s-live" {
		t.Fatal("the live account's own app key")
	}
}

func TestRefreshTokenStoredBeforeUse(t *testing.T) {
	e := newEnv(t)
	e.sec.Err = errors.New("database down")
	_, err := e.c.Holdings(ctxFor(simAcct))
	if err == nil || !strings.Contains(err.Error(), "storing the new refresh token") || secretsIn(err.Error()) {
		t.Fatalf("%v", err)
	}
	if e.sim.count("/sim/openapi/port/v1/balances/me") != 0 {
		t.Fatal("used an access token whose refresh token was not stored")
	}
}

func TestLapsedSignInNeedsReconnect(t *testing.T) {
	e := newEnv(t)
	e.sim.route = func(r *http.Request, _ int) (int, string, http.Header) {
		if r.URL.Path == "/auth/token" {
			return 400, `{"error":"invalid_grant","error_description":"refresh-0 expired"}`, nil
		}
		return 0, "", nil
	}
	_, err := e.c.Holdings(ctxFor(simAcct))
	if !errors.Is(err, domain.ErrReconnect) || !errors.Is(err, domain.ErrUnavailable) || secretsIn(err.Error()) || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("%v", err)
	}
	// the refused token is never sent again
	if _, err := e.c.Holdings(ctxFor(simAcct)); !errors.Is(err, domain.ErrReconnect) || e.sim.count("/auth/token") != 1 {
		t.Fatalf("retried a refused token: %v (%d)", err, e.sim.count("/auth/token"))
	}
	// connecting again stores a new one, which is used
	e.sim.route = nil
	if err := e.c.Exchange(ctxFor(simAcct), "code-1", "https://trader.example/api/v1/oauth/saxo/callback"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.Holdings(ctxFor(simAcct)); err != nil || e.sim.count("/auth/token") != 2 {
		t.Fatalf("after reconnecting: %v (%d)", err, e.sim.count("/auth/token"))
	}
	// a token server outage is not a lapsed sign-in
	e2 := newEnv(t)
	e2.sim.route = func(r *http.Request, _ int) (int, string, http.Header) {
		if r.URL.Path == "/auth/token" {
			return 503, "", nil
		}
		return 0, "", nil
	}
	if _, err := e2.c.Holdings(ctxFor(simAcct)); errors.Is(err, domain.ErrReconnect) || !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("outage: %v", err)
	}
}

func TestDeveloperToken(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	dev := domain.Account{ID: "s-dev", Name: "Dev", Mode: domain.ModeDemo, Platform: domain.PlatformSaxo}
	_ = e.sec.Put(ctx, cred(dev, domain.SaxoDevToken), "devtoken-secret")
	if _, err := e.c.Holdings(ctxFor(dev)); err != nil {
		t.Fatal(err)
	}
	if e.sim.reqs[0].Header.Get("Authorization") != "Bearer devtoken-secret" || e.sim.count("/auth/token") != 0 {
		t.Fatal("the developer token is the bearer, no sign-in request")
	}
	e.advance(DevTokenLifetime)
	if _, err := e.c.Holdings(ctxFor(dev)); !errors.Is(err, domain.ErrReconnect) || secretsIn(err.Error()) {
		t.Fatalf("a day-old developer token: %v", err)
	}
	realDev := domain.Account{ID: "s-realdev", Name: "Real dev", Mode: domain.ModeReal, Platform: domain.PlatformSaxo}
	_ = e.sec.Put(ctx, cred(realDev, domain.SaxoDevToken), "devtoken-secret")
	if _, err := e.c.Holdings(ctxFor(realDev)); !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), "SIM") {
		t.Fatalf("developer token on LIVE: %v", err)
	}
	if e.live.count("") != 0 {
		t.Fatal("a developer token went to LIVE")
	}
}

func TestFailsClosedWithoutAccountOrKeys(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	noApp := domain.Account{ID: "s-noapp", Name: "No app", Platform: domain.PlatformSaxo}
	_ = e.sec.Put(ctx, cred(noApp, domain.SaxoRefreshToken), "refresh-x")
	tests := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"no account", context.Background(), domain.ErrUnavailable},
		{"an eToro account", ctxFor(domain.Account{ID: "e1", Name: "eToro"}), domain.ErrUnavailable},
		{"a Tiger account", ctxFor(domain.Account{ID: "g1", Name: "Tiger", Platform: domain.PlatformTiger}), domain.ErrUnavailable},
		{"nothing set", ctxFor(domain.Account{ID: "s-new", Name: "New", Platform: domain.PlatformSaxo}), domain.ErrCredentialsMissing},
		{"refresh token without the app", ctxFor(noApp), domain.ErrCredentialsMissing},
	}
	for _, tt := range tests {
		if _, err := e.c.Holdings(tt.ctx); !errors.Is(err, tt.want) {
			t.Errorf("%s: %v", tt.name, err)
		}
		if _, err := e.c.AuthorizeURL(tt.ctx, "https://x/cb", "st"); err == nil {
			t.Errorf("%s: authorize URL without an app key", tt.name)
		}
	}
	if err := e.c.Exchange(context.Background(), "c", "r"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("exchange without an account")
	}
	if e.sim.count("")+e.live.count("") != 0 {
		t.Fatal("called Saxo without an account or keys")
	}
}

func TestConnect(t *testing.T) {
	e := newEnv(t)
	u, err := e.c.AuthorizeURL(ctxFor(liveAcct), "https://trader.example:8453/api/v1/oauth/saxo/callback", "st&1")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := url.Parse(u)
	q := p.Query()
	if !strings.HasSuffix(p.Host+p.Path, "/auth/authorize") || p.Host != strings.TrimPrefix(e.c.cfg.LiveAuthURL[:strings.LastIndex(e.c.cfg.LiveAuthURL, "/")], "http://") ||
		q.Get("response_type") != "code" || q.Get("client_id") != "appkey-s-live" || q.Get("state") != "st&1" ||
		q.Get("redirect_uri") != "https://trader.example:8453/api/v1/oauth/saxo/callback" || strings.Contains(u, "appsecret") {
		t.Fatalf("authorize URL = %s", u)
	}
	if err := e.c.Exchange(ctxFor(liveAcct), "the-code", "https://trader.example:8453/api/v1/oauth/saxo/callback"); err != nil {
		t.Fatal(err)
	}
	f := e.live.forms[0]
	if f.Get("grant_type") != "authorization_code" || f.Get("code") != "the-code" || f.Get("redirect_uri") != "https://trader.example:8453/api/v1/oauth/saxo/callback" ||
		f.Get("client_secret") != "appsecret-s-live" {
		t.Fatalf("exchange form = %v", f)
	}
	if v, _ := e.sec.Get(context.Background(), cred(liveAcct, domain.SaxoRefreshToken)); v != "refresh-1" {
		t.Fatal("refresh token not stored")
	}
	// the fresh access token is used at once: no refresh before the first read
	if _, err := e.c.Holdings(ctxFor(liveAcct)); err != nil || e.live.count("/auth/token") != 1 {
		t.Fatalf("%v (%d)", err, e.live.count("/auth/token"))
	}
	// a refused code; an answer without tokens
	e.live.route = func(r *http.Request, _ int) (int, string, http.Header) {
		return 400, `{"error":"invalid_request"}`, nil
	}
	if err := e.c.Exchange(ctxFor(liveAcct), "bad", "r"); !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), "HTTP 400 (invalid_request)") {
		t.Fatalf("refused code: %v", err)
	}
	e.live.route = func(r *http.Request, _ int) (int, string, http.Header) { return 200, `{"access_token":"x"}`, nil }
	if err := e.c.Exchange(ctxFor(liveAcct), "c", "r"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("no refresh token: %v", err)
	}
	e.live.route = func(r *http.Request, _ int) (int, string, http.Header) { return 200, `<html>`, nil }
	if err := e.c.Exchange(ctxFor(liveAcct), "c", "r"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("not JSON: %v", err)
	}
	e.live.route = func(r *http.Request, _ int) (int, string, http.Header) {
		return 200, `"` + strings.Repeat("x", maxTokenResponse) + `"`, nil
	}
	if err := e.c.Exchange(ctxFor(liveAcct), "c", "r"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("oversized: %v", err)
	}
}

func TestErrorsNeverCarrySecrets(t *testing.T) {
	for _, status := range []int{401, 403, 404, 400} {
		e := newEnv(t)
		e.sim.route = func(r *http.Request, _ int) (int, string, http.Header) {
			if r.URL.Path == "/auth/token" {
				return 0, "", nil
			}
			return status, `{"ErrorCode":"Unauthorized","Message":"token access-1 for appsecret-s-sim"}`, nil
		}
		_, err := e.c.Holdings(ctxFor(simAcct))
		if err == nil || secretsIn(err.Error()) || secretsIn(e.logs.String()) {
			t.Fatalf("%d: %v", status, err)
		}
		want := domain.ErrUnavailable
		if status == 404 {
			want = domain.ErrNotFound
		}
		if !errors.Is(err, want) || !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(status)+" (Unauthorized)") {
			t.Fatalf("%d: %v", status, err)
		}
	}
	// a 401 drops the access token and signs in again once
	e := newEnv(t)
	e.sim.route = func(r *http.Request, n int) (int, string, http.Header) {
		if r.URL.Path != "/auth/token" && r.Header.Get("Authorization") == "Bearer access-1" {
			return 401, "", nil
		}
		return 0, "", nil
	}
	if _, err := e.c.Holdings(ctxFor(simAcct)); err != nil || e.sim.count("/auth/token") != 2 {
		t.Fatalf("after a 401: %v (%d sign-ins)", err, e.sim.count("/auth/token"))
	}
}

func TestRateLimits(t *testing.T) {
	e := newEnv(t)
	e.sim.route = func(r *http.Request, n int) (int, string, http.Header) {
		if r.URL.Path == "/sim/openapi/port/v1/balances/me" && n == 2 {
			return 429, "", http.Header{"X-Ratelimit-Portfoliominute-Remaining": {"0"}, "X-Ratelimit-Portfoliominute-Reset": {"8"}}
		}
		return 0, "", nil
	}
	if _, err := e.c.Holdings(ctxFor(simAcct)); err != nil {
		t.Fatal(err)
	}
	if len(e.slept) == 0 || e.slept[0] != 8*time.Second {
		t.Fatalf("slept %v, want the 8 s to the reset", e.slept)
	}
	// a reset too far away: the caller gets "unavailable" now
	e2 := newEnv(t)
	e2.sim.route = func(r *http.Request, _ int) (int, string, http.Header) {
		if r.URL.Path == "/auth/token" {
			return 0, "", nil
		}
		return 429, "", http.Header{"X-Ratelimit-Portfoliominute-Remaining": {"0"}, "X-Ratelimit-Portfoliominute-Reset": {"50"}}
	}
	if _, err := e2.c.Holdings(ctxFor(simAcct)); !errors.Is(err, domain.ErrUnavailable) || e2.sim.count("/sim/openapi/port/v1/balances/me") != 1 {
		t.Fatalf("far reset: %v", err)
	}
	// a used-up window on a success holds the next request back
	e3 := newEnv(t)
	e3.sim.route = func(r *http.Request, _ int) (int, string, http.Header) {
		if r.URL.Path == "/sim/openapi/port/v1/balances/me" {
			b, _ := os.ReadFile("testdata/balances.json")
			return 200, string(b), http.Header{"X-Ratelimit-Portfoliominute-Remaining": {"0"}, "X-Ratelimit-Portfoliominute-Reset": {"3"}}
		}
		return 0, "", nil
	}
	_, _ = e3.c.Holdings(ctxFor(simAcct))
	_, _ = e3.c.Holdings(ctxFor(simAcct))
	if len(e3.slept) == 0 || e3.slept[0] != 3*time.Second {
		t.Fatalf("slept %v", e3.slept)
	}
	// 5xx is retried once
	e4 := newEnv(t)
	e4.sim.route = func(r *http.Request, _ int) (int, string, http.Header) {
		if r.URL.Path == "/auth/token" {
			return 0, "", nil
		}
		return 503, "", nil
	}
	if _, err := e4.c.Holdings(ctxFor(simAcct)); !errors.Is(err, domain.ErrUnavailable) || e4.sim.count("/sim/openapi/port/v1/balances/me") != 2 {
		t.Fatalf("5xx: %v", err)
	}
}

func TestPaging(t *testing.T) {
	e := newEnv(t)
	page := func(n int) string {
		items := make([]string, n)
		for i := range items {
			items[i] = `{"PositionBase":{"Amount":1,"AssetType":"Stock","Uic":` + strconv.Itoa(i) + `},"PositionView":{"MarketValue":1,"ProfitLossOnTrade":0},"DisplayAndFormat":{"Currency":"AUD"}}`
		}
		return `{"Data":[` + strings.Join(items, ",") + `]}`
	}
	e.sim.route = func(r *http.Request, _ int) (int, string, http.Header) {
		if r.URL.Path != "/sim/openapi/port/v1/positions/me" {
			return 0, "", nil
		}
		if r.URL.Query().Get("$skip") == "0" {
			return 200, page(pageSize), nil
		}
		return 200, page(1), nil
	}
	h, err := e.c.Holdings(ctxFor(simAcct))
	if err != nil || len(h.Positions) != pageSize+1 || h.Positions[0].Symbol != "#0" || h.Positions[0].Unknown || h.Positions[0].Value != 100 {
		t.Fatalf("paged = %d %v", len(h.Positions), err)
	}
	e.sim.route = func(r *http.Request, _ int) (int, string, http.Header) {
		if r.URL.Path != "/sim/openapi/port/v1/positions/me" {
			return 0, "", nil
		}
		return 200, page(pageSize), nil
	}
	e2 := e
	if _, err := e2.c.Holdings(ctxFor(simAcct)); !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("endless pages: %v", err)
	}
}

func TestBadResponsesFailClosed(t *testing.T) {
	ok := `{"Currency":"AUD","TotalValue":1,"CashBalance":1}`
	tests := map[string]struct{ balances, positions string }{
		"not JSON":       {`<html>`, `{"Data":[]}`},
		"no currency":    {`{"TotalValue":1,"CashBalance":1}`, `{"Data":[]}`},
		"no total":       {`{"Currency":"AUD","CashBalance":1}`, `{"Data":[]}`},
		"no cash":        {`{"Currency":"AUD","TotalValue":1}`, `{"Data":[]}`},
		"odd currency":   {`{"Currency":"AU","TotalValue":1,"CashBalance":1}`, `{"Data":[]}`},
		"no Data":        {ok, `{}`},
		"huge cash":      {`{"Currency":"AUD","TotalValue":1,"CashBalance":1e300}`, `{"Data":[]}`},
		"huge quantity":  {ok, `{"Data":[{"PositionBase":{"Amount":1e300}}]}`},
		"positions HTML": {ok, `<html>`},
	}
	for name, tt := range tests {
		e := newEnv(t)
		e.sim.route = func(r *http.Request, _ int) (int, string, http.Header) {
			switch r.URL.Path {
			case "/sim/openapi/port/v1/balances/me":
				return 200, tt.balances, nil
			case "/sim/openapi/port/v1/positions/me":
				return 200, tt.positions, nil
			}
			return 0, "", nil
		}
		if _, err := e.c.Holdings(ctxFor(simAcct)); !errors.Is(err, domain.ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	e := newEnv(t)
	e.sim.route = func(r *http.Request, _ int) (int, string, http.Header) {
		if r.URL.Path == "/auth/token" {
			return 0, "", nil
		}
		return 200, `"` + strings.Repeat("x", maxResponse) + `"`, nil
	}
	if _, err := e.c.Holdings(ctxFor(simAcct)); !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("cap: %v", err)
	}
}

func TestPositionFieldsDefensive(t *testing.T) {
	var cv conv
	// nothing at all: unknown, symbol "?"
	if x := holding(&cv, positionWire{}, "AUD"); !x.Unknown || x.Symbol != "?" || !x.Long {
		t.Fatalf("empty = %+v", x)
	}
	amount := 3.0
	p := positionWire{}
	p.PositionBase = &struct {
		Amount            *float64 `json:"Amount"`
		AssetType         string   `json:"AssetType"`
		Uic               *int64   `json:"Uic"`
		OpenPrice         *float64 `json:"OpenPrice"`
		ExecutionTimeOpen string   `json:"ExecutionTimeOpen"`
	}{Amount: &amount, AssetType: "Stock", ExecutionTimeOpen: "yesterday"}
	if x := holding(&cv, p, "AUD"); !x.Unknown || x.Quantity != 3_000_000 || !x.OpenedAt.IsZero() {
		t.Fatalf("no view = %+v", x)
	}
}

func TestJPYAccount(t *testing.T) {
	e := newEnv(t)
	e.sim.route = func(r *http.Request, _ int) (int, string, http.Header) {
		switch r.URL.Path {
		case "/sim/openapi/port/v1/balances/me":
			return 200, `{"Currency":"JPY","TotalValue":1500000,"CashBalance":250000.4}`, nil
		case "/sim/openapi/port/v1/positions/me":
			return 200, `{"Data":[{"PositionBase":{"Amount":100,"AssetType":"Stock"},"PositionView":{"MarketValue":1250000,"ProfitLossOnTrade":-3500},"DisplayAndFormat":{"Symbol":"7203:xtks","Currency":"JPY"}}]}`, nil
		}
		return 0, "", nil
	}
	h, err := e.c.Holdings(ctxFor(simAcct))
	if err != nil || h.Currency != "JPY" || h.Cash != 250_000 || h.Equity != 1_500_000 || h.Positions[0].Value != 1_250_000 || h.Positions[0].PnL != -3_500 {
		t.Fatalf("JPY = %+v %v", h, err)
	}
}

func TestTransportErrorIsUnavailable(t *testing.T) {
	sec := &fakes.SecretStore{}
	_ = sec.Put(context.Background(), cred(simAcct, domain.SaxoDevToken), "devtoken-secret")
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	c, err := New(Config{SimBaseURL: srv.URL, SimAuthURL: srv.URL, Secrets: sec, Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Holdings(ctxFor(simAcct)); !errors.Is(err, domain.ErrUnavailable) || secretsIn(err.Error()) {
		t.Fatalf("%v", err)
	}
	ctx, cancel := context.WithCancel(ctxFor(simAcct))
	cancel()
	if _, err := c.Holdings(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	// the sign-in host down
	_ = sec.Put(context.Background(), cred(simAcct, domain.SaxoAppKey), "k")
	_ = sec.Put(context.Background(), cred(simAcct, domain.SaxoAppSecret), "appsecret-x")
	_ = sec.Put(context.Background(), cred(simAcct, domain.SaxoRefreshToken), "refresh-x")
	if _, err := c.Holdings(ctxFor(simAcct)); !errors.Is(err, domain.ErrUnavailable) || errors.Is(err, domain.ErrReconnect) || secretsIn(err.Error()) {
		t.Fatalf("sign-in host down: %v", err)
	}
	if err := c.Exchange(ctx, "c", "r"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled exchange: %v", err)
	}
	// a secret store failure
	bad := &failingSecrets{}
	c2, _ := New(Config{Secrets: bad})
	if _, err := c2.Holdings(ctxFor(simAcct)); err == nil || !strings.Contains(err.Error(), "reading the Saxo") {
		t.Fatalf("secret store down: %v", err)
	}
	if _, err := c2.AuthorizeURL(ctxFor(simAcct), "r", "s"); err == nil {
		t.Fatal("authorize with the secret store down")
	}
}

type failingSecrets struct{ fakes.SecretStore }

func (*failingSecrets) Get(context.Context, domain.CredentialName) (string, error) {
	return "", errors.New("disk on fire")
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("no secrets")
	}
	for _, cfg := range []Config{
		{SimBaseURL: "::"}, {LiveBaseURL: "nohost"}, {SimAuthURL: "ftp://x"}, {LiveAuthURL: "%"},
	} {
		cfg.Secrets = &fakes.SecretStore{}
		if _, err := New(cfg); err == nil {
			t.Errorf("%+v accepted", cfg)
		}
	}
	c, err := New(Config{Secrets: &fakes.SecretStore{}})
	if err != nil || c.sim.String() != SimBaseURL || c.live.String() != LiveBaseURL || c.simAuth.String() != SimAuthURL ||
		c.liveAuth.String() != LiveAuthURL || c.cfg.RequestTimeout != 15*time.Second {
		t.Fatalf("defaults: %v", err)
	}
	if err := c.get(ctxFor(simAcct), Op("trade"), nil, nil); err == nil || !strings.Contains(err.Error(), "not allowlisted") {
		t.Fatalf("unknown op: %v", err)
	}
	if _, err := c.secret(context.Background(), simAcct, "nope"); err == nil {
		t.Fatal("unknown credential")
	}
	if s1, s2 := c.session("a"), c.session("a"); s1 != s2 || s1 == c.session("b") {
		t.Fatal("one session per account")
	}
}

func TestHelpers(t *testing.T) {
	now := time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		h         http.Header
		wait      time.Duration
		exhausted bool
	}{
		{http.Header{}, 5 * time.Second, false},
		{http.Header{"X-Ratelimit-Appday-Remaining": {"9000"}}, 5 * time.Second, false},
		{http.Header{"X-Ratelimit-Appday-Remaining": {"0"}}, 5 * time.Second, true},
		{http.Header{"X-Ratelimit-A-Remaining": {"0"}, "X-Ratelimit-A-Reset": {"12"}, "X-Ratelimit-B-Remaining": {"0"}, "X-Ratelimit-B-Reset": {"30"}}, 30 * time.Second, true},
		{http.Header{"X-Ratelimit-A-Remaining": {"0"}, "X-Ratelimit-A-Reset": {"86400"}}, time.Minute, true},
		{http.Header{"X-Ratelimit-A-Remaining": {"x"}}, 5 * time.Second, false},
	}
	for i, tt := range tests {
		until, ex := rateLimit(tt.h, now)
		if until.Sub(now) != tt.wait || ex != tt.exhausted {
			t.Errorf("%d: %v %v", i, until.Sub(now), ex)
		}
	}
	for body, want := range map[string]string{
		`{"ErrorCode":"InvalidModelState"}`:               "InvalidModelState",
		`{"ErrorInfo":{"ErrorCode":"Forbidden"}}`:         "Forbidden",
		`{"error":"invalid_grant"}`:                       "invalid_grant",
		`{"ErrorCode":"has space"}`:                       "",
		`{"ErrorCode":"` + strings.Repeat("a", 65) + `"}`: "",
		`nope`: "",
	} {
		if got := errorCode([]byte(body)); got != want {
			t.Errorf("errorCode(%s) = %q", body, got)
		}
	}
	for status, want := range map[int]string{
		401: "Saxo balances: HTTP 401: the sign-in was refused",
		403: "Saxo balances: HTTP 403: the app lacks a permission (it needs read access to the portfolio)",
		429: "Saxo balances: HTTP 429: rate limit reached, try again shortly",
		500: "Saxo balances: HTTP 500",
	} {
		if got := (&APIError{Op: "balances", Status: status}).Error(); got != want {
			t.Errorf("%d: %q", status, got)
		}
	}
	if _, err := scaled(1e300, 100); !errors.Is(err, errRange) {
		t.Fatal("range")
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal("sleepCtx ignores ctx")
	}
	l := newLimiter(func() time.Time { return now })
	l.Block(OpPositions, now.Add(time.Minute))
	l.Block(OpPositions, now)
	if w, ok := l.Reserve(OpPositions, time.Second, maxRateWait); ok || w != time.Minute {
		t.Fatalf("blocked: %v %v", w, ok)
	}
}
