package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/adapters/argon2"
	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// TestGlobalCredentialWritesRemoved is PLAN 7.1.3 (F3, decision D27): eToro keys are set per
// account only; the global credential writes no longer exist, the status list stays.
func TestGlobalCredentialWritesRemoved(t *testing.T) {
	x := newTC(t, nil)
	x.setupAndLogin()
	for _, name := range []string{"etoro_demo_api_key", "etoro_demo_user_key", "etoro_real_api_key"} {
		if code, _, _ := x.do("PUT", "/api/v1/credentials/"+name, `{"value":"swapped"}`, "X-CSRF-Token", x.csrf); code != 404 && code != 405 {
			t.Fatalf("PUT %s = %d, want 404/405", name, code)
		}
		if code, _, _ := x.do("DELETE", "/api/v1/credentials/"+name, "", "X-CSRF-Token", x.csrf); code != 404 && code != 405 {
			t.Fatalf("DELETE %s = %d, want 404/405", name, code)
		}
	}
	code, _, _ := x.do("GET", "/api/v1/credentials", "")
	if code != 200 {
		t.Fatalf("GET credentials = %d", code)
	}
	for _, a := range x.store.AuditEntries() {
		if strings.HasPrefix(a.Action, "credential.") {
			t.Fatalf("a credential was written: %+v", a)
		}
	}
}

// newSecTC is a test server that believes X-Forwarded-For from the test client (so a test
// can sign in "from" any address), with hasher h (nil = the fake) and an e-mail fallback so
// that password reset is available.
func newSecTC(t *testing.T, h ports.PasswordHasher) (*tc, *Server) {
	t.Helper()
	if h == nil {
		h = fakes.Hasher{}
	}
	st := fakes.NewStore()
	clk := &fakes.Clock{T: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
	svc, err := app.New(app.Deps{
		Store: st, Secrets: &fakes.SecretStore{Now: clk.Now}, Hasher: h, Random: &longTokens{}, Clock: clk, Mailer: &fakes.Mailer{},
		MailFallback: func() (domain.SMTPSettings, string, bool) {
			return domain.SMTPSettings{Enabled: true, Host: "smtp.example.com", Port: 587, TLS: domain.SMTPStartTLS, From: "bot@example.com"}, "", true
		},
	}, app.Config{Version: "9.9.9", PublicURL: "https://trader.example"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CookieSecure: true, OpenAPI: []byte("openapi: 3.0.3\n"),
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")},
	}
	s := New(svc, slog.New(slog.DiscardHandler), cfg, nil)
	s.limiter.sleep = func(context.Context, time.Duration) error { return nil } // tests read slept
	srv := httptest.NewTLSServer(s)
	t.Cleanup(srv.Close)
	c := srv.Client()
	c.Jar, _ = cookiejar.New(nil)
	x := &tc{t: t, srv: srv, c: c, store: st, svc: svc}
	x.setupAndLogin()
	x.c.Jar, _ = cookiejar.New(nil) // signed out
	return x, s
}

// from sends an anonymous request from address ip (through the trusted proxy).
func (x *tc) from(ip, method, path, body string) (int, http.Header) {
	x.t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), method, x.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", ip)
	resp, err := (&http.Client{Transport: x.c.Transport}).Do(req)
	if err != nil {
		x.t.Error(err)
		return 0, nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode, resp.Header
}

const adminPassword = "a long password" // setupAndLogin's

func signIn(user, pw string) string {
	return `{"username":"` + user + `","password":"` + pw + `"}`
}

// TestSignInLimitPerUserAndIP is PLAN 7.1.5 (F5): failures for a user from one address limit
// that address only; the user still signs in from another.
func TestSignInLimitPerUserAndIP(t *testing.T) {
	x, srv := newSecTC(t, nil)
	codes := ""
	for range 6 {
		code, _ := x.from("198.51.100.7", "POST", "/api/v1/auth/login", signIn("admin", "wrong password!"))
		codes += fmt.Sprint(code, " ")
	}
	if codes != "401 401 401 401 401 429 " {
		t.Fatalf("from A: %s", codes)
	}
	var slept []time.Duration
	srv.limiter.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	if code, _ := x.from("203.0.113.9", "POST", "/api/v1/auth/login", signIn("admin", adminPassword)); code != 200 {
		t.Fatalf("admin from B = %d, want 200 (no global lock-out)", code)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("slow-down before the attempt from B: %v, want [1s]", slept)
	}
	if code, _ := x.from("198.51.100.7", "POST", "/api/v1/auth/login", signIn("admin", adminPassword)); code != 429 {
		t.Fatalf("admin from A = %d, want 429 (still limited there)", code)
	}
}

// TestResetRequestsAreNotSignInFailures is PLAN 7.1.5 (F5): anonymous reset requests for a
// user neither lock the user out nor use up the address's sign-in attempts.
func TestResetRequestsAreNotSignInFailures(t *testing.T) {
	x, _ := newSecTC(t, nil)
	limited := false
	for i := range 25 {
		user := "admin"
		if i >= 6 {
			user = fmt.Sprintf("someone%d", i) // other users, same address
		}
		if code, _ := x.from("198.51.100.7", "POST", "/api/v1/public/password-reset", `{"username":"`+user+`"}`); code == 429 {
			limited = true
		}
	}
	if !limited {
		t.Fatal("reset requests are not limited")
	}
	if code, _ := x.from("203.0.113.9", "POST", "/api/v1/auth/login", signIn("admin", adminPassword)); code != 200 {
		t.Fatalf("admin from another address = %d", code)
	}
	if code, _ := x.from("198.51.100.7", "POST", "/api/v1/auth/login", signIn("admin", adminPassword)); code != 200 {
		t.Fatalf("admin from the requesting address = %d", code)
	}
}

// TestResetConfirmLimitPerIP is PLAN 7.1.5 (F5): bad reset tokens from one address do not
// block another.
func TestResetConfirmLimitPerIP(t *testing.T) {
	x, _ := newSecTC(t, nil)
	bad := `{"token":"` + strings.Repeat("x", 40) + `","password":"brand new pass 1"}`
	limited := false
	for range 25 {
		if code, h := x.from("198.51.100.7", "POST", "/api/v1/public/password-reset/confirm", bad); code == 429 {
			if h.Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("bad tokens are not limited")
	}
	if code, _ := x.from("203.0.113.9", "POST", "/api/v1/public/password-reset/confirm", bad); code != 400 {
		t.Fatalf("a bad token from another address = %d, want 400", code)
	}
}

// TestChangePasswordLimited is PLAN 7.1.10 (F10): guessing the current password through
// ChangePassword is limited like a sign-in.
func TestChangePasswordLimited(t *testing.T) {
	x, _ := newSecTC(t, nil)
	x.login("admin", adminPassword)
	codes := ""
	for range 6 {
		code, _, _ := x.do("POST", "/api/v1/auth/password", `{"currentPassword":"wrong password!","newPassword":"an even longer password"}`, "X-CSRF-Token", x.csrf)
		codes += fmt.Sprint(code, " ")
	}
	if codes != "400 400 400 400 400 429 " {
		t.Fatalf("codes: %s", codes)
	}
}

// slowHasher counts the hashes running at once.
type slowHasher struct {
	fakes.Hasher
	running, peak atomic.Int32
}

func (h *slowHasher) enter() func() {
	n := h.running.Add(1)
	for {
		p := h.peak.Load()
		if n <= p || h.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	return func() { h.running.Add(-1) }
}

func (h *slowHasher) Hash(pw string) (string, error) { defer h.enter()(); return h.Hasher.Hash(pw) }

func (h *slowHasher) Verify(hash, pw string) (bool, error) {
	defer h.enter()()
	return h.Hasher.Verify(hash, pw)
}

// TestSignInHashesBounded is PLAN 7.1.4 (F4): 50 concurrent sign-ins never run more than
// argon2.DefaultSlots hashes at once; the rest are answered 429 with Retry-After.
func TestSignInHashesBounded(t *testing.T) {
	h := &slowHasher{}
	x, _ := newSecTC(t, argon2.NewLimited(h, argon2.DefaultSlots, 30*time.Millisecond))
	h.peak.Store(0)
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	retry := true
	for i := range 50 {
		wg.Go(func() {
			code, hdr := x.from(fmt.Sprintf("198.51.100.%d", i+1), "POST", "/api/v1/auth/login", signIn(fmt.Sprintf("user%d", i), "wrong password!"))
			mu.Lock()
			defer mu.Unlock()
			codes[code]++
			if code == 429 && hdr.Get("Retry-After") == "" {
				retry = false
			}
		})
	}
	wg.Wait()
	if p := h.peak.Load(); p > argon2.DefaultSlots {
		t.Fatalf("%d hashes ran at once, want ≤ %d", p, argon2.DefaultSlots)
	}
	if codes[401]+codes[429] != 50 || codes[429] == 0 || codes[401] == 0 || !retry {
		t.Fatalf("codes %v (Retry-After on every 429: %v)", codes, retry)
	}
}

// TestLimiterSweepAndCap is PLAN 7.1.5 (F5, MINOR): stale keys are swept, and the map never
// holds more than maxLimiterKeys.
func TestLimiterSweepAndCap(t *testing.T) {
	l := newLoginLimiter()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	for i := range maxLimiterKeys + 1 {
		l.Failed(confirmIP, fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255))
		now = now.Add(time.Millisecond)
	}
	if n := l.Len(); n > maxLimiterKeys {
		t.Fatalf("%d keys, cap %d", n, maxLimiterKeys)
	}
	if ok, _ := l.Allowed(confirmIP, "10.0.0.0"); !ok {
		t.Fatal("the oldest key was not the one dropped")
	}
	now = now.Add(16 * time.Minute)
	if n := l.Sweep(); n != maxLimiterKeys || l.Len() != 0 {
		t.Fatalf("swept %d, left %d", n, l.Len())
	}
}

// TestProfileEmailNeedsPassword is PLAN 7.1.11 (F11): the e-mail address (where reset links
// go) changes only with the current password; notification choices need none.
func TestProfileEmailNeedsPassword(t *testing.T) {
	x, _ := newSecTC(t, nil)
	x.login("admin", adminPassword)
	csrf := []string{"X-CSRF-Token", x.csrf}
	notify := `"notify":{"trades":true,"watchlist":false,"alerts":true}`
	if code, p, _ := x.do("PUT", "/api/v1/me/profile", `{"email":"mallory@example.com",`+notify+`}`, csrf...); code != 400 || p["field"] != "currentPassword" {
		t.Fatalf("no password: %d %v", code, p)
	}
	if code, p, _ := x.do("PUT", "/api/v1/me/profile", `{"email":"mallory@example.com","currentPassword":"wrong password!",`+notify+`}`, csrf...); code != 400 || p["field"] != "currentPassword" {
		t.Fatalf("wrong password: %d %v", code, p)
	}
	if code, p, _ := x.do("PUT", "/api/v1/me/profile", `{"email":"admin@example.com","currentPassword":"`+adminPassword+`",`+notify+`}`, csrf...); code != 200 || p["email"] != "admin@example.com" {
		t.Fatalf("right password: %d %v", code, p)
	}
	if code, p, _ := x.do("PUT", "/api/v1/me/profile", `{"email":"admin@example.com","notify":{"trades":false,"watchlist":true,"alerts":true}}`, csrf...); code != 200 || p["notify"].(map[string]any)["watchlist"] != true {
		t.Fatalf("notify only: %d %v", code, p)
	}
	if code, p, _ := x.do("PUT", "/api/v1/me/profile", `{"email":"admin@example.com","currentPassword":"`+adminPassword+`",`+notify+`}`, csrf...); code != 200 || p["currentPassword"] != nil {
		t.Fatalf("the password is echoed: %d %v", code, p)
	}
	// wrong passwords are limited like sign-ins (PLAN 7.1.10)
	codes := ""
	for range 6 {
		code, _, _ := x.do("PUT", "/api/v1/me/profile", `{"email":"mallory@example.com","currentPassword":"wrong password!",`+notify+`}`, csrf...)
		codes += fmt.Sprint(code, " ")
	}
	if codes != "400 400 400 400 400 429 " { // the right password above cleared the earlier wrong one
		t.Fatalf("codes: %s", codes)
	}
}
