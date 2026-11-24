package tiger

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// testKey is a throwaway RSA key generated for these tests only.
var (
	keyOnce sync.Once
	testKey *rsa.PrivateKey
)

func key(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		testKey = k
	})
	return testKey
}

func pkcs8PEM(t *testing.T) string {
	der, err := x509.MarshalPKCS8PrivateKey(key(t))
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// fakeTiger is the gateway: it checks every request's signature with the test key's public
// half and answers by method.
type fakeTiger struct {
	t      *testing.T
	pub    *rsa.PublicKey
	mu     sync.Mutex
	params []map[string]string
	// route overrides the answer: status 0 = serve the fixture.
	route func(method string, n int) (int, string)
}

func (f *fakeTiger) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json;charset=UTF-8" {
		f.t.Errorf("request %s %s", r.Method, r.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(r.Body)
	var p map[string]string
	if err := json.Unmarshal(body, &p); err != nil {
		f.t.Errorf("body: %v", err)
	}
	sig, _ := base64.StdEncoding.DecodeString(p["sign"])
	unsigned := map[string]string{}
	for k, v := range p {
		if k != "sign" {
			unsigned[k] = v
		}
	}
	sum := sha1.Sum([]byte(signContent(unsigned)))
	if err := rsa.VerifyPKCS1v15(f.pub, crypto.SHA1, sum[:], sig); err != nil {
		f.t.Errorf("signature: %v", err)
	}
	f.mu.Lock()
	f.params = append(f.params, p)
	n := len(f.params)
	f.mu.Unlock()
	if f.route != nil {
		if status, b := f.route(p["method"], n); status != 0 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, b)
			return
		}
	}
	data, err := os.ReadFile("testdata/" + p["method"] + ".json")
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, _ = w.Write(data)
}

func (f *fakeTiger) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.params) }

type env struct {
	c     *Client
	gw    *fakeTiger
	sec   *fakes.SecretStore
	mu    sync.Mutex
	now   time.Time
	slept []time.Duration
	logs  strings.Builder
}

var (
	paper = domain.Account{ID: "g-paper", Name: "Tiger paper", Mode: domain.ModeDemo, Platform: domain.PlatformTiger}
	live  = domain.Account{ID: "g-live", Name: "Tiger live", Mode: domain.ModeReal, Platform: domain.PlatformTiger}
)

func cred(a domain.Account, k string) domain.CredentialName {
	n, _ := domain.AccountCredentialName(a, k)
	return n
}

func setCreds(t *testing.T, sec *fakes.SecretStore, a domain.Account, privateKey string) {
	t.Helper()
	ctx := context.Background()
	_ = sec.Put(ctx, cred(a, domain.TigerID), "tigerid-20150001")
	_ = sec.Put(ctx, cred(a, domain.TigerAccount), "21234567")
	_ = sec.Put(ctx, cred(a, domain.TigerPrivateKey), privateKey)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{now: time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC), sec: &fakes.SecretStore{}}
	e.gw = &fakeTiger{t: t, pub: &key(t).PublicKey}
	srv := httptest.NewServer(e.gw)
	t.Cleanup(srv.Close)
	setCreds(t, e.sec, paper, pkcs8PEM(t))
	setCreds(t, e.sec, live, pkcs8PEM(t))
	c, err := New(Config{
		GatewayURL: srv.URL + "/gateway", HTTP: srv.Client(), Secrets: e.sec,
		Log: slog.New(slog.NewTextHandler(&lockedWriter{e: e}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now: func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
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

type lockedWriter struct{ e *env }

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.e.mu.Lock()
	defer w.e.mu.Unlock()
	return w.e.logs.Write(p)
}

func ctxFor(a domain.Account) context.Context { return ports.WithAccount(context.Background(), a) }

// secretsIn reports whether s holds a credential: the Tiger ID or any line of the key.
func secretsIn(t *testing.T, s string) bool {
	if strings.Contains(s, "tigerid-20150001") {
		return true
	}
	for _, line := range strings.Split(pkcs8PEM(t), "\n") {
		if len(line) > 20 && strings.Contains(s, line) {
			return true
		}
	}
	return false
}

func TestHoldings(t *testing.T) {
	e := newEnv(t)
	h, err := e.c.Holdings(ctxFor(paper))
	if err != nil {
		t.Fatal(err)
	}
	if h.Platform != domain.PlatformTiger || h.Currency != "USD" || h.Cash != 523_450 || h.Equity != 1_543_210 ||
		h.UnrealisedPnL != 31_240 || h.RealisedPnL != -1_225 || h.Invested != 295_900 || len(h.Positions) != 4 || !h.AsOf.Equal(e.now) {
		t.Fatalf("holdings = %+v", h)
	}
	aapl, tencent, tsla, spy := h.Positions[0], h.Positions[1], h.Positions[2], h.Positions[3]
	if aapl.Symbol != "AAPL" || aapl.Name != "Apple Inc" || aapl.Currency != "USD" || aapl.Quantity != 20_000_000 || aapl.AvgPrice != 180_500_000 ||
		aapl.CurrentPrice != 190_120_000 || aapl.Value != 380_240 || aapl.PnL != 19_240 || aapl.Cost != 361_000 || !aapl.Long || aapl.Unknown {
		t.Fatalf("AAPL = %+v", aapl)
	}
	// an HKD position on a USD account: Tiger reports no USD value, so it is unknown
	if !tencent.Unknown || tencent.Value != 0 || tencent.Currency != "HKD" || tencent.Quantity != 100_000_000 {
		t.Fatalf("Tencent = %+v", tencent)
	}
	if tsla.Long || tsla.Quantity != 3_000_000 || tsla.Value != -72_000 || tsla.Name != "US" {
		t.Fatalf("TSLA = %+v", tsla)
	}
	if spy.Quantity != 15_000_000 || spy.Value != 10_000 || spy.Unknown {
		t.Fatalf("SPY (position/scale) = %+v", spy)
	}
	// the envelope: signed (checked by the fake), the account in biz_content, UTC timestamp
	p := e.gw.params[0]
	if p["method"] != "prime_assets" || p["tiger_id"] != "tigerid-20150001" || p["charset"] != "UTF-8" || p["sign_type"] != "RSA" ||
		p["version"] != "2.0" || p["timestamp"] != "2026-11-23 15:00:00" || p["biz_content"] != `{"account":"21234567"}` {
		t.Fatalf("envelope = %v", p)
	}
	if _, has := p["license"]; has {
		t.Fatal("the licence is never sent")
	}
	if e.gw.params[1]["method"] != "positions" || e.gw.count() != 2 {
		t.Fatalf("calls = %d", e.gw.count())
	}
	if secretsIn(t, e.logs.String()) {
		t.Fatal("a credential reached the log")
	}
	// the live account uses the same gateway with its own credentials
	if _, err := e.c.Holdings(ctxFor(live)); err != nil || e.gw.count() != 4 {
		t.Fatalf("live: %v", err)
	}
}

func TestFallbackToAssets(t *testing.T) {
	assets := `{"code":0,"data":{"items":[{"account":"21234567","currency":"HKD","cashValue":1000.5,"netLiquidation":2000,"unrealizedPnL":5,"realizedPnL":1}]}}`
	tests := []struct {
		name  string
		prime string
	}{
		{"prime refused for this account", `{"code":1200,"message":"not a prime account"}`},
		{"no securities segment", `{"code":0,"data":{"segments":[{"category":"C","currency":"USD","cashBalance":1,"netLiquidation":1}]}}`},
	}
	for _, tt := range tests {
		e := newEnv(t)
		e.gw.route = func(m string, _ int) (int, string) {
			switch m {
			case "prime_assets":
				return 200, tt.prime
			case "assets":
				return 200, assets
			}
			return 0, ""
		}
		h, err := e.c.Holdings(ctxFor(paper))
		if err != nil || h.Currency != "HKD" || h.Cash != 100_050 || h.Equity != 200_000 || h.UnrealisedPnL != 500 || h.RealisedPnL != 100 {
			t.Fatalf("%s: %+v %v", tt.name, h, err)
		}
		if e.gw.params[1]["method"] != "assets" || e.gw.params[1]["biz_content"] != `{"account":"21234567","segment":true}` {
			t.Fatalf("%s: assets call = %v", tt.name, e.gw.params[1])
		}
		// every position is in USD or HKD; on an HKD account only the HKD one is known
		if h.Positions[1].Unknown || !h.Positions[0].Unknown {
			t.Fatalf("%s: positions = %+v", tt.name, h.Positions)
		}
	}
	// a permission problem or a rate limit does not fall back
	for _, code := range []int{4001, 1000, 5} {
		e := newEnv(t)
		e.gw.route = func(m string, _ int) (int, string) {
			if m == "prime_assets" {
				return 200, `{"code":` + itoa(code) + `,"message":"refused for 21234567"}`
			}
			return 0, ""
		}
		_, err := e.c.Holdings(ctxFor(paper))
		if !errors.Is(err, domain.ErrUnavailable) || strings.Contains(err.Error(), "21234567") {
			t.Fatalf("code %d: %v", code, err)
		}
		for _, p := range e.gw.params {
			if p["method"] == "assets" {
				t.Fatalf("code %d fell back", code)
			}
		}
	}
	// assets with nothing in it
	e := newEnv(t)
	e.gw.route = func(m string, _ int) (int, string) {
		if m == "prime_assets" {
			return 200, `{"code":1100}`
		}
		return 200, `{"code":0,"data":{"items":[]}}`
	}
	if _, err := e.c.Holdings(ctxFor(paper)); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("empty assets: %v", err)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestKeyFormats(t *testing.T) {
	k := key(t)
	pkcs1 := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	bare := base64.StdEncoding.EncodeToString(der)
	for name, s := range map[string]string{"pkcs8": pkcs8PEM(t), "pkcs1": pkcs1, "bare base64": bare[:40] + "\n" + bare[40:]} {
		got, err := parseKey(s)
		if err != nil || !got.Equal(k) {
			t.Errorf("%s: %v", name, err)
		}
	}
	ecDER, _ := x509.MarshalPKCS8PrivateKey(mustEd(t))
	for name, s := range map[string]string{
		"garbage": "not a key", "empty": "", "bad pem": "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----",
		"not RSA": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER})),
	} {
		if _, err := parseKey(s); !errors.Is(err, errKey) || strings.Contains(err.Error(), "AAAA") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a bad stored key fails before anything is sent, without quoting the key
	e := newEnv(t)
	_ = e.sec.Put(context.Background(), cred(paper, domain.TigerPrivateKey), "-----BEGIN PRIVATE KEY-----\nSECRETBYTES\n-----END PRIVATE KEY-----")
	if _, err := e.c.Holdings(ctxFor(paper)); !errors.Is(err, domain.ErrUnavailable) || strings.Contains(err.Error(), "SECRETBYTES") || e.gw.count() != 0 {
		t.Fatalf("bad key: %v (%d calls)", err, e.gw.count())
	}
	if signContent(map[string]string{"b": "2", "a": "1&x", "c": ""}) != "a=1&x&b=2&c=" {
		t.Fatal("signContent")
	}
}

func mustEd(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestFailsClosedWithoutAccountOrKeys(t *testing.T) {
	e := newEnv(t)
	half := domain.Account{ID: "g-half", Name: "Half", Platform: domain.PlatformTiger}
	_ = e.sec.Put(context.Background(), cred(half, domain.TigerID), "tigerid-20150001")
	_ = e.sec.Put(context.Background(), cred(half, domain.TigerAccount), "")
	tests := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"no account", context.Background(), domain.ErrUnavailable},
		{"an eToro account", ctxFor(domain.Account{ID: "e1", Name: "eToro"}), domain.ErrUnavailable},
		{"a Saxo account", ctxFor(domain.Account{ID: "s1", Name: "Saxo", Platform: domain.PlatformSaxo}), domain.ErrUnavailable},
		{"no keys", ctxFor(domain.Account{ID: "g-new", Name: "New", Platform: domain.PlatformTiger}), domain.ErrCredentialsMissing},
		{"some keys", ctxFor(half), domain.ErrCredentialsMissing},
	}
	for _, tt := range tests {
		if _, err := e.c.Holdings(tt.ctx); !errors.Is(err, tt.want) || (err != nil && secretsIn(t, err.Error())) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
	if e.gw.count() != 0 {
		t.Fatal("called Tiger without an account or keys")
	}
}

func TestErrorsNeverCarrySecrets(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404} {
		e := newEnv(t)
		e.gw.route = func(string, int) (int, string) { return status, `{"tiger_id":"tigerid-20150001"}` }
		_, err := e.c.Holdings(ctxFor(paper))
		if !errors.Is(err, domain.ErrUnavailable) || secretsIn(t, err.Error()) || !strings.Contains(err.Error(), "HTTP "+itoa(status)) || e.gw.count() != 1 {
			t.Fatalf("%d: %v", status, err)
		}
	}
	e := newEnv(t)
	e.gw.route = func(string, int) (int, string) {
		return 200, `{"code":1000,"message":"bad sign for tiger_id tigerid-20150001 account 21234567\u0000"}`
	}
	_, err := e.c.Holdings(ctxFor(paper))
	if !errors.Is(err, domain.ErrUnavailable) || secretsIn(t, err.Error()) || strings.Contains(err.Error(), "21234567") ||
		!strings.Contains(err.Error(), "code 1000: the request was refused") || secretsIn(t, e.logs.String()) {
		t.Fatalf("business error: %v", err)
	}
}

func TestRateLimits(t *testing.T) {
	for name, answer := range map[string]struct {
		status int
		body   string
	}{"HTTP 429": {429, ""}, "code 5": {200, `{"code":5,"message":"rate limit"}`}} {
		e := newEnv(t)
		e.gw.route = func(m string, n int) (int, string) {
			if n == 1 {
				return answer.status, answer.body
			}
			return 0, ""
		}
		if _, err := e.c.Holdings(ctxFor(paper)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(e.slept) == 0 || e.slept[0] != rateBackoff {
			t.Fatalf("%s: slept %v", name, e.slept)
		}
	}
	// always limited: the second wait would be too long, so the caller gets "unavailable"
	e := newEnv(t)
	e.gw.route = func(string, int) (int, string) { return 429, "" }
	if _, err := e.c.Holdings(ctxFor(paper)); !errors.Is(err, domain.ErrUnavailable) || e.gw.count() != 2 {
		t.Fatalf("always limited: %v (%d calls)", err, e.gw.count())
	}
	e.advance(time.Second)
	e.gw.route = nil
	// the method is still held back beyond maxRateWait? no: 10 s backoff is within the 15 s wait
	if _, err := e.c.Holdings(ctxFor(paper)); err != nil {
		t.Fatalf("after the backoff: %v", err)
	}
	// 5xx is retried once
	e2 := newEnv(t)
	e2.gw.route = func(string, int) (int, string) { return 502, "" }
	if _, err := e2.c.Holdings(ctxFor(paper)); !errors.Is(err, domain.ErrUnavailable) || e2.gw.count() != 2 {
		t.Fatalf("5xx: %v", err)
	}
	// the limiter refuses a slot too far away
	l := newLimiter(func() time.Time { return e.now })
	l.Block(MethodPositions, e.now.Add(time.Minute))
	l.Block(MethodPositions, e.now)
	if w, ok := l.Reserve(MethodPositions, time.Second, maxRateWait); ok || w != time.Minute {
		t.Fatalf("blocked: %v %v", w, ok)
	}
	if a, b := e.c.limiter("a"), e.c.limiter("a"); a != b || a == e.c.limiter("b") {
		t.Fatal("one limiter per account")
	}
}

func (e *env) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }

func TestBadResponsesFailClosed(t *testing.T) {
	tests := map[string]struct{ prime, positions string }{
		"not JSON":           {`<html>`, ""},
		"no code":            {`{"data":{}}`, ""},
		"no data":            {`{"code":0}`, ""},
		"data not an object": {`{"code":0,"data":[1]}`, ""},
		"no currency":        {`{"code":0,"data":{"segments":[{"category":"S","cashBalance":1,"netLiquidation":1}]}}`, ""},
		"no cash":            {`{"code":0,"data":{"segments":[{"category":"S","currency":"USD","netLiquidation":1}]}}`, ""},
		"no positions items": {"", `{"code":0,"data":{}}`},
		"huge cash":          {`{"code":0,"data":{"segments":[{"category":"S","currency":"USD","cashBalance":1e300,"netLiquidation":1}]}}`, ""},
		"huge quantity":      {"", `{"code":0,"data":{"items":[{"symbol":"X","currency":"USD","positionQty":1e300}]}}`},
	}
	for name, tt := range tests {
		e := newEnv(t)
		e.gw.route = func(m string, _ int) (int, string) {
			if m == "prime_assets" && tt.prime != "" {
				return 200, tt.prime
			}
			if m == "positions" && tt.positions != "" {
				return 200, tt.positions
			}
			return 0, ""
		}
		if _, err := e.c.Holdings(ctxFor(paper)); !errors.Is(err, domain.ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	e := newEnv(t)
	e.gw.route = func(string, int) (int, string) { return 200, `"` + strings.Repeat("x", maxResponse) + `"` }
	if _, err := e.c.Holdings(ctxFor(paper)); !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("cap: %v", err)
	}
	// data sent as a JSON string holding the JSON is understood
	e2 := newEnv(t)
	e2.gw.route = func(m string, _ int) (int, string) {
		if m == "positions" {
			return 200, `{"code":0,"data":"{\"items\":[]}"}`
		}
		return 0, ""
	}
	if h, err := e2.c.Holdings(ctxFor(paper)); err != nil || len(h.Positions) != 0 {
		t.Fatalf("string data: %v", err)
	}
}

func TestPositionDefensive(t *testing.T) {
	var cv conv
	x := holding(&cv, positionWire{}, "USD")
	if !x.Unknown || x.Symbol != "?" || x.Quantity != 0 {
		t.Fatalf("empty = %+v", x)
	}
	pos := int64(5)
	if x := holding(&cv, positionWire{Symbol: "X", Currency: "USD", Position: &pos, PositionScale: 99}, "USD"); !x.Unknown || x.Quantity != 0 {
		t.Fatalf("absurd scale = %+v", x)
	}
	// a JPY account: whole yen
	v, pnl, qty := 1_250_000.0, -3_500.0, 100.0
	if x := holding(&cv, positionWire{Symbol: "7203", Currency: "JPY", PositionQty: &qty, MarketValue: &v, UnrealizedPnl: &pnl}, "JPY"); x.Value != 1_250_000 || x.PnL != -3_500 {
		t.Fatalf("JPY = %+v", x)
	}
}

func TestTransportErrorIsUnavailable(t *testing.T) {
	sec := &fakes.SecretStore{}
	setCreds(t, sec, paper, pkcs8PEM(t))
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	c, err := New(Config{GatewayURL: srv.URL, Secrets: sec, Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Holdings(ctxFor(paper)); !errors.Is(err, domain.ErrUnavailable) || secretsIn(t, err.Error()) {
		t.Fatalf("%v", err)
	}
	ctx, cancel := context.WithCancel(ctxFor(paper))
	cancel()
	if _, err := c.Holdings(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	c2, _ := New(Config{Secrets: &failingSecrets{}})
	if _, err := c2.Holdings(ctxFor(paper)); err == nil || !strings.Contains(err.Error(), "reading the Tiger credentials") {
		t.Fatalf("secret store down: %v", err)
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
	for _, u := range []string{"::", "nohost", "ftp://x/gateway"} {
		if _, err := New(Config{Secrets: &fakes.SecretStore{}, GatewayURL: u}); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
	c, err := New(Config{Secrets: &fakes.SecretStore{}})
	if err != nil || c.gateway.String() != GatewayURL || c.cfg.RequestTimeout != 15*time.Second {
		t.Fatalf("defaults: %v", err)
	}
	if err := c.call(ctxFor(paper), Method("place_"+"order"), nil, nil); err == nil || !strings.Contains(err.Error(), "not allowlisted") {
		t.Fatalf("unknown method: %v", err)
	}
	for _, tt := range []struct {
		err  error
		want string
	}{
		{&APIError{Method: MethodPositions, Status: 429}, "Tiger positions: HTTP 429: rate limit reached, try again shortly"},
		{&BusinessError{Method: MethodAssets, Code: 5}, "Tiger assets: code 5: rate limit reached, try again shortly"},
		{&BusinessError{Method: MethodAssets, Code: 4001, Message: "x"}, "Tiger assets: code 4001: no permission or the sign-in was refused (x)"},
		{&BusinessError{Method: MethodAssets, Code: 1234}, "Tiger assets: code 1234"},
	} {
		if tt.err.Error() != tt.want {
			t.Errorf("%q", tt.err.Error())
		}
	}
	if s := scrub(strings.Repeat("é", 200), creds{}); len(s) > 162 {
		t.Fatalf("scrub length %d", len(s))
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal("sleepCtx ignores ctx")
	}
	if _, err := scaled(1e300, 100); !errors.Is(err, errRange) {
		t.Fatal("range")
	}
}
