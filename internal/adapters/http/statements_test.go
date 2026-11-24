package httpapi

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// TestStatementEndpoints covers R30's routes: CRUD with CSRF, send-now (no mail server:
// 503 with the email_server fix; then once a minute), the PDF preview's headers, refusals
// for API keys and bad parameters, and the rate class of the heavy routes.
func TestStatementEndpoints(t *testing.T) {
	r := &fakes.StatementRenderer{}
	mailOn := false
	// More exports than OD33's 10 a minute: this test makes about a dozen.
	x := newAgentTCServer(t, func(d *app.Deps) {
		d.Statements, d.Mailer = r, &fakes.Mailer{}
		d.MailFallback = func() (app.FallbackMail, bool) {
			return app.FallbackMail{Server: domain.SMTPSettings{Enabled: true, Host: "mail.example.com", Port: 587, TLS: domain.SMTPStartTLS, From: "ct@example.com"}}, mailOn
		}
	}, Config{RateLimits: RateLimits{Export: 100}}, slog.New(slog.DiscardHandler))
	var page StatementsPage
	if code := x.getJSON("/api/v1/statements/schedules", &page); code != 200 || len(page.Schedules) != 0 || len(page.Accounts) != 1 ||
		page.Accounts[0].Name != "Main demo" || page.MailServer || !page.CanFixMail {
		t.Fatalf("page %d %+v", code, page)
	}
	body := `{"period":"weekly","accountIds":[],"recipients":["Me@Example.com"],"includeCombined":true,"includeAccounts":false,"includeExchanges":true,"enabled":true}`
	if code, _, _ := x.do("POST", "/api/v1/statements/schedules", body); code != 403 {
		t.Fatalf("without CSRF %d", code)
	}
	code, m, _ := x.do("POST", "/api/v1/statements/schedules", body, x.csrfHdr()...)
	if code != 201 || m["period"] != "weekly" || m["recipients"].([]any)[0] != "me@example.com" || m["ownerName"] != "admin" || m["nextSendAt"] == nil ||
		m["lastSentAt"] != nil {
		t.Fatalf("create %d %v", code, m)
	}
	id := m["id"].(string)
	bad := strings.Replace(body, `"weekly"`, `"hourly"`, 1)
	if code, _, _ := x.do("POST", "/api/v1/statements/schedules", bad, x.csrfHdr()...); code != 400 {
		t.Fatalf("bad period %d", code)
	}
	upd := strings.Replace(body, `"enabled":true`, `"enabled":false`, 1)
	if code, m, _ := x.do("PUT", "/api/v1/statements/schedules/"+id, upd, x.csrfHdr()...); code != 200 || m["enabled"] != false || m["nextSendAt"] != nil {
		t.Fatalf("update %d %v", code, m)
	}

	// Send now without a mail server: 503 naming the fix; with one: queued, then 429.
	code, m, _ = x.do("POST", "/api/v1/statements/schedules/"+id+"/send-now", "", x.csrfHdr()...)
	if code != 503 || m["missing"] != "email_server" || m["canFix"] != true {
		t.Fatalf("send now without mail %d %v", code, m)
	}
	mailOn = true
	code, m, _ = x.do("POST", "/api/v1/statements/schedules/"+id+"/send-now", "", x.csrfHdr()...)
	if code != 200 || m["recipients"].(float64) != 1 || m["period"] != "2026-W47" || m["title"] != "16–20 November 2026" || len(m["files"].([]any)) != 1 {
		t.Fatalf("send now %d %v", code, m)
	}
	if code, _, h := x.do("POST", "/api/v1/statements/schedules/"+id+"/send-now", "", x.csrfHdr()...); code != 429 || h.Get("Retry-After") == "" {
		t.Fatalf("send now again %d", code)
	}

	// Preview: a PDF attachment, never cached.
	code, h, pdf := x.raw("/api/v1/statements/preview.pdf?period=weekly&from=2026-11-16", "")
	if code != 200 || h.Get("Content-Type") != "application/pdf" || h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Content-Disposition") != `attachment; filename="cactusTrade-statement-2026-w47-weekly-combined.pdf"` || !strings.HasPrefix(pdf, "%PDF") {
		t.Fatalf("preview %d %v %q", code, h, pdf)
	}
	code, h, _ = x.raw("/api/v1/statements/preview.pdf?period=daily&scope=account&accountId="+x.acct, "")
	if code != 200 || !strings.Contains(h.Get("Content-Disposition"), "-daily-account-main-demo.pdf") {
		t.Fatalf("account preview %d %v", code, h)
	}
	for q, want := range map[string]int{
		"period=yearly":                          400,
		"period=daily&scope=all":                 400,
		"period=daily&scope=exchange&exchange=X": 400,
		"period=daily&from=2026-12-24":           400,
		"period=daily&scope=account&accountId=x": 404,
		"":                                       400,
	} {
		code, h, b := x.raw("/api/v1/statements/preview.pdf?"+q, "")
		if code != want || !strings.HasPrefix(h.Get("Content-Type"), "application/problem+json") {
			t.Errorf("preview ?%s: %d %s", q, code, b)
		}
	}

	// API keys manage no statements (the recipients are typed addresses).
	code, m, _ = x.do("POST", "/api/v1/api-keys", `{"name":"k","role":"admin","expiresInDays":30}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("key %d %v", code, m)
	}
	key := m["secret"].(string)
	if code, _ := x.bearer("GET", "/api/v1/statements/schedules", "", key); code != 403 {
		t.Fatalf("API key list %d", code)
	}
	if code, _, _ := x.raw("/api/v1/statements/preview.pdf?period=daily", key); code != 403 {
		t.Fatalf("API key preview %d", code)
	}
	if code, _ := x.bearer("POST", "/api/v1/statements/schedules", body, key); code != 403 {
		t.Fatalf("API key create %d", code)
	}

	if code, _, _ := x.do("DELETE", "/api/v1/statements/schedules/"+id, "", x.csrfHdr()...); code != 204 {
		t.Fatalf("delete %d", code)
	}
	if code, _, _ := x.do("DELETE", "/api/v1/statements/schedules/"+id, "", x.csrfHdr()...); code != 404 {
		t.Fatalf("delete again %d", code)
	}
	var after StatementsPage
	if code := x.getJSON("/api/v1/statements/schedules", &after); code != 200 || len(after.Schedules) != 0 || !after.MailServer {
		t.Fatalf("after delete %d %+v", code, after)
	}
	raw, _ := json.Marshal(after)
	if !strings.Contains(string(raw), `"schedules":[]`) {
		t.Fatalf("schedules not an empty list: %s", raw)
	}

	for _, p := range []string{"GET /api/v1/statements/preview.pdf", "POST /api/v1/statements/schedules/{id}/send-now"} {
		method, path, _ := strings.Cut(p, " ")
		if c := classify(method, path, p); c != classExport {
			t.Errorf("%s: class %q, want export", p, c)
		}
	}
	if c := classify("POST", "/api/v1/statements/schedules", "POST /api/v1/statements/schedules"); c != classMutation {
		t.Errorf("create: class %q", c)
	}
}
