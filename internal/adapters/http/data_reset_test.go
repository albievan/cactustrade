package httpapi

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// TestDataResetEndpoints covers R49's routes: the preview, CSRF, a web session only, 409
// naming the failing checks, 422 for a wrong phrase, 403 for a wrong password, and the 200
// body naming the backup.
func TestDataResetEndpoints(t *testing.T) {
	b := &fakes.Backups{}
	x := newAgentTCServer(t, func(d *app.Deps) { d.Backups = b }, Config{RateLimits: RateLimits{Export: 100}}, slog.New(slog.DiscardHandler))
	csrf := x.csrfHdr()
	x.ready(t)
	if code, m, _ := x.do("PUT", "/api/v1/backups/recipients", `{"recipients":["`+testRecipient+`"]}`, csrf...); code != 200 {
		t.Fatalf("recipients %d %v", code, m)
	}
	var pv DataResetPreview
	if code := x.getJSON("/api/v1/system/data-reset", &pv); code != 200 || !pv.CanReset || len(pv.Checks) != 8 ||
		pv.Phrase != "DELETE ALL TRADING DATA" || !pv.BackupReady || pv.BackupFolder == nil {
		t.Fatalf("preview %d %+v", code, pv)
	}
	if _, ok := pv.Wiped["orders"]; !ok {
		t.Fatalf("wiped %v", pv.Wiped)
	}
	good := `{"confirm":"DELETE ALL TRADING DATA","password":"a long password"}`

	// A running agent: 409 naming the check.
	if code, m, _ := x.do("POST", "/api/v1/agent/start", "", csrf...); code != 200 {
		t.Fatalf("start %d %v", code, m)
	}
	if code, m, _ := x.do("POST", "/api/v1/system/data-reset", good, csrf...); code != 409 || !strings.Contains(m["detail"].(string), "Main demo is running") {
		t.Fatalf("running agent %d %v", code, m)
	}
	if code, m, _ := x.do("POST", "/api/v1/agent/stop", "", csrf...); code != 200 {
		t.Fatalf("stop %d %v", code, m)
	}
	for _, s := range []agentStep{
		{"without CSRF", "POST", "/api/v1/system/data-reset", good, nil, 403, ""},
		{"an unknown field", "POST", "/api/v1/system/data-reset", `{"confirm":"x","password":"y","all":true}`, csrf, 400, ""},
		{"wrong phrase", "POST", "/api/v1/system/data-reset", `{"confirm":"RESET ALL DATA","password":"a long password"}`, csrf, 422, ""},
		{"wrong password", "POST", "/api/v1/system/data-reset", `{"confirm":"DELETE ALL TRADING DATA","password":"not it"}`, csrf, 403, ""},
	} {
		if code, m, _ := x.do(s.method, s.path, s.body, s.hdr...); code != s.code {
			t.Fatalf("%s: %d %v", s.name, code, m)
		}
	}
	if len(b.Writes) != 0 {
		t.Fatal("backed up before every check passed")
	}

	// An admin API key may do neither.
	code, m, _ := x.do("POST", "/api/v1/api-keys", `{"name":"k","role":"admin","expiresInDays":30}`, csrf...)
	if code != 201 {
		t.Fatalf("key %d %v", code, m)
	}
	key := m["secret"].(string)
	if code, _ := x.bearer("GET", "/api/v1/system/data-reset", "", key); code != 403 {
		t.Fatalf("API key preview %d", code)
	}
	if code, _ := x.bearer("POST", "/api/v1/system/data-reset", good, key); code != 403 {
		t.Fatalf("API key reset %d", code)
	}

	code, m, _ = x.do("POST", "/api/v1/system/data-reset", `{"confirm":"DELETE ALL TRADING DATA","password":"a long password","wipeWatchlists":true}`, csrf...)
	if code != 200 || !strings.HasSuffix(m["backupFile"].(string), ".db.age") || m["wipedWatchlists"] != true || m["counts"] == nil || m["at"] == nil {
		t.Fatalf("reset %d %v", code, m)
	}
	if len(b.Writes) != 1 {
		t.Fatalf("%d backups", len(b.Writes))
	}
}

func TestDataResetWrongPasswordsAreLimited(t *testing.T) {
	x := newAgentTC(t)
	csrf := x.csrfHdr()
	for i := range 5 {
		if code, m, _ := x.do("POST", "/api/v1/system/data-reset", `{"confirm":"DELETE ALL TRADING DATA","password":"guess"}`, csrf...); code != 403 {
			t.Fatalf("attempt %d: %d %v", i+1, code, m)
		}
	}
	if code, m, h := x.do("POST", "/api/v1/system/data-reset", `{"confirm":"DELETE ALL TRADING DATA","password":"a long password"}`, csrf...); code != 429 || h.Get("Retry-After") == "" {
		t.Fatalf("6th attempt %d %v", code, m)
	}
}
