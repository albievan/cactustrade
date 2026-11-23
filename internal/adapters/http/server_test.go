package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

type tc struct {
	t     *testing.T
	srv   *httptest.Server
	c     *http.Client
	csrf  string
	store *fakes.Store
	svc   *app.Service
}

func newTC(t *testing.T, web fstest.MapFS) *tc {
	t.Helper()
	st := fakes.NewStore()
	clk := &fakes.Clock{T: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
	svc, err := app.New(app.Deps{Store: st, Secrets: &fakes.SecretStore{Now: clk.Now}, Hasher: fakes.Hasher{}, Random: &fakes.Random{}, Clock: clk},
		app.Config{Version: "9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{CookieSecure: true, MetricsToken: "mt", OpenAPI: []byte("openapi: 3.0.3\n")}
	if web != nil {
		cfg.Web = web
	}
	srv := httptest.NewTLSServer(New(svc, slog.New(slog.DiscardHandler), cfg, nil))
	t.Cleanup(srv.Close)
	c := srv.Client()
	c.Jar, _ = cookiejar.New(nil)
	return &tc{t: t, srv: srv, c: c, store: st, svc: svc}
}

func (x *tc) do(method, path, body string, hdr ...string) (int, map[string]any, http.Header) {
	x.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, x.srv.URL+path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := x.c.Do(req)
	if err != nil {
		x.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return resp.StatusCode, m, resp.Header
}

// anon returns a client without the cookie jar.
func (x *tc) anon(method, path, body string) int {
	x.t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), method, x.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Transport: x.c.Transport}).Do(req)
	if err != nil {
		x.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// login signs in and checks the session cookie's attributes.
func (x *tc) login(user, pw string) map[string]any {
	x.t.Helper()
	code, m, h := x.do("POST", "/api/v1/auth/login", `{"username":"`+user+`","password":"`+pw+`"}`)
	if code != 200 {
		x.t.Fatalf("login %d %v", code, m)
	}
	ck := h.Get("Set-Cookie")
	for _, want := range []string{SessionCookie + "=", "HttpOnly", "Secure", "SameSite=Strict"} {
		if !strings.Contains(ck, want) {
			x.t.Fatalf("cookie %q lacks %q", ck, want)
		}
	}
	x.csrf = m["csrfToken"].(string)
	return m
}

// setupAndLogin is a first start: the default admin/admin signs in, is held to changing
// the password, changes it, and signs in again with the new one.
func (x *tc) setupAndLogin() {
	x.t.Helper()
	if _, err := x.svc.EnsureDefaultAdmin(context.Background()); err != nil {
		x.t.Fatal(err)
	}
	m := x.login("admin", "admin")
	if u, _ := m["user"].(map[string]any); u["mustChangePassword"] != true {
		x.t.Fatalf("default admin login %v", m)
	}
	if code, m, _ := x.do("GET", "/api/v1/system/status", ""); code != 403 || m["title"] != "Password change required" {
		x.t.Fatalf("status before change %d %v", code, m)
	}
	if code, m, _ := x.do("GET", "/api/v1/auth/me", ""); code != 200 || m["user"].(map[string]any)["mustChangePassword"] != true {
		x.t.Fatalf("me before change %d %v", code, m)
	}
	if code, m, _ := x.do("POST", "/api/v1/auth/password", `{"currentPassword":"admin","newPassword":"a long password"}`, "X-CSRF-Token", x.csrf); code != 204 {
		x.t.Fatalf("change password %d %v", code, m)
	}
	if u := x.login("admin", "a long password")["user"].(map[string]any); u["mustChangePassword"] != false {
		x.t.Fatalf("after change %v", u)
	}
}

func TestDefaultAdminLoginCSRFAndRBAC(t *testing.T) {
	x := newTC(t, nil)
	if code, m, _ := x.do("GET", "/api/v1/public/status", ""); code != 200 || m["version"] != "9.9.9" {
		t.Fatalf("public %d %v", code, m)
	}
	if code, _, _ := x.do("GET", "/api/v1/system/status", ""); code != 401 {
		t.Fatalf("anonymous %d", code)
	}
	x.setupAndLogin()
	if code := x.anon("POST", "/api/v1/setup", `{"username":"x","password":"a long password"}`); code == 201 {
		t.Fatal("a setup endpoint still exists")
	}
	if code, m, _ := x.do("GET", "/api/v1/auth/me", ""); code != 200 || m["via"] != "session" || m["csrfToken"] != x.csrf {
		t.Fatalf("me %d %v", code, m)
	}
	if code, m, _ := x.do("GET", "/api/v1/system/status", ""); code != 200 || m["mode"] != "demo" || m["database"] != "fake" {
		t.Fatalf("status %d %v", code, m)
	}
	// CSRF: missing, wrong, cross-origin
	body := `{"username":"olivia","password":"olivia password!","role":"operator"}`
	if code, _, _ := x.do("POST", "/api/v1/users", body); code != 403 {
		t.Fatalf("no csrf %d", code)
	}
	if code, _, _ := x.do("POST", "/api/v1/users", body, "X-CSRF-Token", "nope"); code != 403 {
		t.Fatalf("wrong csrf %d", code)
	}
	if code, _, _ := x.do("POST", "/api/v1/users", body, "X-CSRF-Token", x.csrf, "Origin", "https://evil.example"); code != 403 {
		t.Fatalf("cross-origin %d", code)
	}
	code, m, _ := x.do("POST", "/api/v1/users", body, "X-CSRF-Token", x.csrf)
	if code != 201 || m["role"] != "operator" {
		t.Fatalf("create user %d %v", code, m)
	}
	uid := m["id"].(string)
	if code, _, _ := x.do("POST", "/api/v1/users", body, "X-CSRF-Token", x.csrf); code != 409 {
		t.Fatalf("duplicate %d", code)
	}
	if code, m, _ := x.do("POST", "/api/v1/users", `{"username":"zz","password":"short","role":"viewer"}`, "X-CSRF-Token", x.csrf); code != 400 || m["field"] != "password" {
		t.Fatalf("invalid %d %v", code, m)
	}
	if code, _, _ := x.do("POST", "/api/v1/users", `{"username":"zz","bogus":1}`, "X-CSRF-Token", x.csrf); code != 400 {
		t.Fatalf("unknown field %d", code)
	}
	if code, _, _ := x.do("POST", "/api/v1/users", "x", "X-CSRF-Token", x.csrf, "Content-Type", "text/plain"); code != 415 {
		t.Fatalf("content type %d", code)
	}
	if code, m, _ := x.do("PATCH", "/api/v1/users/"+uid, `{"role":"viewer"}`, "X-CSRF-Token", x.csrf); code != 200 || m["role"] != "viewer" {
		t.Fatalf("update %d %v", code, m)
	}
	if code, m, _ := x.do("PATCH", "/api/v1/users/nope", `{"disabled":true}`, "X-CSRF-Token", x.csrf); code != 404 {
		t.Fatalf("update missing %d %v", code, m)
	}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", x.srv.URL+"/api/v1/users", nil)
	resp, err := x.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var users []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&users)
	resp.Body.Close()
	if len(users) != 2 {
		t.Fatalf("users %v", users)
	}

	// API keys
	code, m, _ = x.do("POST", "/api/v1/api-keys", `{"name":"ci","role":"viewer","expiresInDays":30}`, "X-CSRF-Token", x.csrf)
	if code != 201 {
		t.Fatalf("create key %d %v", code, m)
	}
	secret := m["secret"].(string)
	kid := m["apiKey"].(map[string]any)["id"].(string)
	anon := &http.Client{Transport: x.c.Transport}
	get := func(path, bearer string) int {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", x.srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := anon.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if get("/api/v1/system/status", secret) != 200 || get("/api/v1/users", secret) != 403 || get("/api/v1/system/status", "ctk_bad_key") != 401 {
		t.Fatal("api key auth")
	}
	// an API key needs no CSRF token, but cannot create keys or set credentials
	req, _ = http.NewRequestWithContext(context.Background(), "POST", x.srv.URL+"/api/v1/api-keys", strings.NewReader(`{"name":"x","role":"viewer"}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	resp, _ = anon.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("api key creating keys: %d", resp.StatusCode)
	}
	if code, _, _ := x.do("GET", "/api/v1/api-keys", ""); code != 200 {
		t.Fatal("list keys")
	}
	if code, _, _ := x.do("DELETE", "/api/v1/api-keys/"+kid, "", "X-CSRF-Token", x.csrf); code != 204 {
		t.Fatal("revoke")
	}
	if get("/api/v1/system/status", secret) != 401 {
		t.Fatal("revoked key still works")
	}

	// credentials: write-only
	if code, _, _ := x.do("PUT", "/api/v1/credentials/llm_api_key", `{"value":"top-secret"}`, "X-CSRF-Token", x.csrf); code != 204 {
		t.Fatal("set credential")
	}
	if code, _, _ := x.do("PUT", "/api/v1/credentials/aws", `{"value":"x"}`, "X-CSRF-Token", x.csrf); code != 400 {
		t.Fatal("unknown credential")
	}
	req, _ = http.NewRequestWithContext(context.Background(), "GET", x.srv.URL+"/api/v1/credentials", nil)
	resp, _ = x.c.Do(req)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), "top-secret") || !strings.Contains(string(b), `"set":true`) {
		t.Fatalf("credentials leak or missing: %s", b)
	}
	if code, _, _ := x.do("DELETE", "/api/v1/credentials/llm_api_key", "", "X-CSRF-Token", x.csrf); code != 204 {
		t.Fatal("delete credential")
	}
	if code, _, _ := x.do("DELETE", "/api/v1/credentials/llm_api_key", "", "X-CSRF-Token", x.csrf); code != 404 {
		t.Fatal("delete missing credential")
	}
	for _, a := range x.store.AuditEntries() {
		if strings.Contains(a.Details, "top-secret") {
			t.Fatal("secret in audit")
		}
	}
	if code, _, _ := x.do("GET", "/api/v1/audit?limit=5", ""); code != 200 {
		t.Fatal("audit")
	}
	if code, _, _ := x.do("GET", "/api/v1/audit?limit=abc", ""); code != 400 {
		t.Fatal("bad param")
	}

	// self-delete refused with a readable reason, then delete the other user
	_, mm, _ := x.do("GET", "/api/v1/auth/me", "")
	me := mm["user"].(map[string]any)["id"].(string)
	if code, m, _ := x.do("DELETE", "/api/v1/users/"+me, "", "X-CSRF-Token", x.csrf); code != 409 || m["detail"] != "you cannot delete yourself" {
		t.Fatalf("self delete %d %v", code, m)
	}
	if code, _, _ := x.do("DELETE", "/api/v1/users/"+uid, "", "X-CSRF-Token", x.csrf); code != 204 {
		t.Fatal("delete user")
	}

	// password change ends the session
	if code, _, _ := x.do("POST", "/api/v1/auth/password", `{"currentPassword":"a long password","newPassword":"an even longer password"}`, "X-CSRF-Token", x.csrf); code != 204 {
		t.Fatal("change password")
	}
	if code, _, _ := x.do("GET", "/api/v1/auth/me", ""); code != 401 {
		t.Fatal("session survived password change")
	}
	if code, _, _ := x.do("POST", "/api/v1/auth/logout", ""); code != 204 {
		t.Fatal("logout")
	}
}

func TestLoginRateLimit(t *testing.T) {
	x := newTC(t, nil)
	x.setupAndLogin()
	x.c.Jar, _ = cookiejar.New(nil) // signed out
	codes := ""
	for range 6 {
		code, _, h := x.do("POST", "/api/v1/auth/login", `{"username":"admin","password":"wrong password!"}`)
		codes += fmt.Sprint(code, " ")
		if code == 429 && h.Get("Retry-After") == "" {
			t.Fatal("429 without Retry-After")
		}
	}
	if codes != "401 401 401 401 401 429 " {
		t.Fatalf("codes: %s", codes)
	}
	// the right password is refused too while limited (no oracle)
	if code := x.anon("POST", "/api/v1/auth/login", `{"username":"admin","password":"a long password"}`); code != 429 {
		t.Fatalf("limited login = %d", code)
	}
}

func TestHealthMetricsWebAndHeaders(t *testing.T) {
	web := fstest.MapFS{
		"index.html":         {Data: []byte("<html>app</html>")},
		"api-docs.html":      {Data: []byte("<html>docs</html>")},
		"assets/app-1.js":    {Data: []byte("console.log(1)")},
		"assets/style-1.css": {Data: []byte("body{}")},
	}
	x := newTC(t, web)
	if code, m, _ := x.do("GET", "/healthz", ""); code != 200 || m["status"] != "ok" {
		t.Fatal("healthz")
	}
	if code, _, _ := x.do("GET", "/readyz", ""); code != 200 {
		t.Fatal("readyz")
	}
	x.store.PingErr = context.DeadlineExceeded
	if code, m, _ := x.do("GET", "/readyz", ""); code != 503 || m["status"] != "unavailable" {
		t.Fatal("readyz down")
	}
	x.store.PingErr = nil
	if code, _, _ := x.do("GET", "/metrics", ""); code != 401 {
		t.Fatal("metrics without token")
	}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", x.srv.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer mt")
	resp, _ := x.c.Do(req)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `cactustrade_http_requests_total{code="200",method="GET",route="GET /healthz"}`) {
		t.Fatalf("metrics %d %s", resp.StatusCode, b)
	}
	page := func(path string) (int, string, http.Header) {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", x.srv.URL+path, nil)
		resp, err := x.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	if code, body, h := page("/"); code != 200 || body != "<html>app</html>" || h.Get("X-Frame-Options") != "DENY" || !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("index %d %q %v", code, body, h)
	}
	if _, body, _ := page("/settings/users"); body != "<html>app</html>" {
		t.Fatal("SPA fallback")
	}
	if _, body, h := page("/api/docs"); body != "<html>docs</html>" || !strings.Contains(h.Get("Content-Security-Policy"), "unsafe-inline") {
		t.Fatal("api docs")
	}
	if _, _, h := page("/assets/app-1.js"); !strings.Contains(h.Get("Cache-Control"), "immutable") {
		t.Fatal("asset caching")
	}
	if code, body, h := page("/api/openapi.yaml"); code != 200 || !strings.HasPrefix(body, "openapi:") || h.Get("Cache-Control") != "no-store" {
		t.Fatal("openapi")
	}
	if code, _, _ := x.do("GET", "/api/v1/nope", ""); code != 404 {
		t.Fatal("unknown api")
	}
	if code, _, _ := x.do("POST", "/somewhere", "{}"); code != 405 {
		t.Fatal("POST to web")
	}
}

func TestNoWebAndPanics(t *testing.T) {
	x := newTC(t, nil)
	req, _ := http.NewRequestWithContext(context.Background(), "GET", x.srv.URL+"/", nil)
	resp, _ := x.c.Do(req)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "built without the web app") {
		t.Fatal("no-web fallback")
	}
	s := &Server{log: slog.New(slog.DiscardHandler)}
	h := s.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("panic → %d %s", rec.Code, rec.Body)
	}
}

func TestClientIP(t *testing.T) {
	s := &Server{cfg: Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16")}}}
	tests := []struct{ remote, xff, want string }{
		{"203.0.113.9:1234", "1.2.3.4", "203.0.113.9"},              // untrusted peer: XFF ignored
		{"10.42.0.7:1234", "198.51.100.1", "198.51.100.1"},          // trusted proxy
		{"10.42.0.7:1234", "6.6.6.6, 198.51.100.1", "198.51.100.1"}, // last untrusted hop
		{"10.42.0.7:1234", "", "10.42.0.7"},
		{"10.42.0.7:1234", "garbage", "10.42.0.7"},
		{"bad", "", "bad"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tt.remote
		if tt.xff != "" {
			r.Header.Set("X-Forwarded-For", tt.xff)
		}
		if got := s.clientIP(r); got != tt.want {
			t.Errorf("clientIP(%s, %q) = %s, want %s", tt.remote, tt.xff, got, tt.want)
		}
	}
}

func TestLimiterWindowAndDetail(t *testing.T) {
	l := newLoginLimiter()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	for range 5 {
		l.Fail("1.1.1.1", "bob")
	}
	if ok, wait := l.Allow("2.2.2.2", "bob"); ok || wait <= 0 {
		t.Fatal("user limit")
	}
	if ok, _ := l.Allow("2.2.2.2", "alice"); !ok {
		t.Fatal("other user limited")
	}
	now = now.Add(16 * time.Minute)
	if ok, _ := l.Allow("2.2.2.2", "bob"); !ok {
		t.Fatal("window did not slide")
	}
	l.Fail("3.3.3.3", "carol")
	l.Success("carol")
	if ok, _ := l.Allow("3.3.3.3", "carol"); !ok {
		t.Fatal("success must clear the user counter")
	}
	if d := detail(fmt.Errorf("app.UpdateUser: %w", fmt.Errorf("the last enabled admin must stay: %w", domain.ErrConflict)), domain.ErrConflict); d != "the last enabled admin must stay" {
		t.Fatalf("detail = %q", d)
	}
}
