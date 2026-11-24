package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

type longTokens struct{ fakes.Random }

func (r *longTokens) Token() string { return strings.Repeat("t", 40) + r.Random.Token() }

func newMailTC(t *testing.T) (*tc, *fakes.Mailer) {
	t.Helper()
	st := fakes.NewStore()
	clk := &fakes.Clock{T: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
	m := &fakes.Mailer{}
	svc, err := app.New(app.Deps{
		Store: st, Secrets: &fakes.SecretStore{Now: clk.Now}, Hasher: fakes.Hasher{}, Random: &longTokens{}, Clock: clk, Mailer: m,
	}, app.Config{Version: "9.9.9", PublicURL: "https://trader.example"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(New(svc, slog.New(slog.DiscardHandler), Config{CookieSecure: true, OpenAPI: []byte("openapi: 3.0.3\n")}, nil))
	t.Cleanup(srv.Close)
	c := srv.Client()
	c.Jar, _ = cookiejar.New(nil)
	x := &tc{t: t, srv: srv, c: c, store: st, svc: svc}
	x.setupAndLogin()
	return x, m
}

func TestEmailEndpoints(t *testing.T) {
	x, m := newMailTC(t)
	csrf := []string{"X-CSRF-Token", x.csrf}
	if _, p, _ := x.do("GET", "/api/v1/public/status", ""); p["passwordResetAvailable"] != false {
		t.Fatalf("reset available without a server: %v", p)
	}
	code, s, _ := x.do("GET", "/api/v1/email", "")
	if code != 200 || s["host"] != "smtp.gmail.com" || len(s["presets"].([]any)) != 3 || s["passwordSet"] != false {
		t.Fatalf("setup %d %v", code, s)
	}
	body := `{"enabled":true,"host":"smtp.gmail.com","port":587,"username":"me@gmail.com","from":"Bot <bot@example.com>","tls":"starttls","password":"app-pass"}`
	code, s, _ = x.do("PUT", "/api/v1/email", body, csrf...)
	if code != 200 || s["passwordSet"] != true || len(s["warnings"].([]any)) != 1 || s["password"] != nil {
		t.Fatalf("save %d %v", code, s)
	}
	if code, s, _ := x.do("PUT", "/api/v1/email", `{"enabled":true,"host":"","port":587,"username":"","from":"","tls":"starttls"}`, csrf...); code != 400 || s["field"] != "host" {
		t.Fatalf("invalid %d %v", code, s)
	}
	if code, _, _ := x.do("POST", "/api/v1/email/test", `{"to":"me@example.com"}`, csrf...); code != 204 || m.Count() != 1 {
		t.Fatalf("test %d", code)
	}
	m.Err = errors.New("535 5.7.8 bad credentials")
	if code, p, _ := x.do("POST", "/api/v1/email/test", `{"to":"me@example.com"}`, csrf...); code != 502 || !strings.Contains(p["detail"].(string), "535") {
		t.Fatalf("refused test %d %v", code, p)
	}
	m.Err = nil

	// profile and notifications
	if code, p, _ := x.do("GET", "/api/v1/me/profile", ""); code != 200 || p["email"] != "" || p["notify"].(map[string]any)["trades"] != true {
		t.Fatalf("profile %d %v", code, p)
	}
	if code, p, _ := x.do("PUT", "/api/v1/me/profile", `{"email":"nope","notify":{"trades":true,"watchlist":false,"alerts":true}}`, csrf...); code != 400 || p["field"] != "email" {
		t.Fatalf("bad profile %d %v", code, p)
	}
	if code, p, _ := x.do("PUT", "/api/v1/me/profile", `{"email":"admin@example.com","currentPassword":"a long password","notify":{"trades":false,"watchlist":true,"alerts":true}}`, csrf...); code != 200 || p["notify"].(map[string]any)["watchlist"] != true {
		t.Fatalf("profile %d %v", code, p)
	}
	var out []map[string]any
	if code := x.getJSON("/api/v1/email/outbox", &out); code != 200 || len(out) != 0 {
		t.Fatalf("outbox %d %v", code, out)
	}

	// password reset (signed out)
	if _, p, _ := x.do("GET", "/api/v1/public/status", ""); p["passwordResetAvailable"] != true {
		t.Fatalf("reset not available: %v", p)
	}
	signedIn := x.c.Jar
	x.c.Jar, _ = cookiejar.New(nil) // signed out, as on the sign-in page
	if code, _, _ := x.do("POST", "/api/v1/public/password-reset", `{"username":"ghost"}`); code != 202 {
		t.Fatalf("unknown user %d", code)
	}
	if code, _, _ := x.do("POST", "/api/v1/public/password-reset", `{"username":"admin"}`); code != 202 {
		t.Fatalf("reset %d", code)
	}
	if n, err := x.svc.DeliverMail(context.Background()); n != 1 || err != nil {
		t.Fatalf("deliver %d %v", n, err)
	}
	x.c.Jar = signedIn
	if code := x.getJSON("/api/v1/email/outbox", &out); code != 200 || len(out) != 1 || out[0]["sentAt"] == nil {
		t.Fatalf("outbox after send %d %v", code, out)
	}
	x.c.Jar, _ = cookiejar.New(nil)
	link := m.Sent[len(m.Sent)-1].Body
	token := strings.Fields(link[strings.Index(link, "#token=")+len("#token="):])[0]
	if code, p, _ := x.do("POST", "/api/v1/public/password-reset/confirm", `{"token":"`+strings.Repeat("x", 40)+`","password":"brand new pass 1"}`); code != 400 || p["field"] != "token" {
		t.Fatalf("bad token %d %v", code, p)
	}
	if code, _, _ := x.do("POST", "/api/v1/public/password-reset/confirm", `{"token":"`+token+`","password":"brand new pass 1"}`); code != 204 {
		t.Fatalf("confirm %d", code)
	}
	// every session ended
	x.c.Jar = signedIn
	if code, _, _ := x.do("GET", "/api/v1/me/profile", ""); code != 401 {
		t.Fatalf("old session still works: %d", code)
	}
	x.c.Jar, _ = cookiejar.New(nil)
	// the request limiter: repeated requests are refused
	limited := false
	for range 20 {
		if code, _, _ := x.do("POST", "/api/v1/public/password-reset", `{"username":"admin"}`); code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("reset requests not rate-limited")
	}
	for _, a := range x.store.AuditEntries() {
		if strings.Contains(a.Details, "app-pass") || strings.Contains(a.Details, token) {
			t.Fatal("secret in the audit log")
		}
	}
	_ = domain.NotifyAlerts
}
