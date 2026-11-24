package httpapi

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/adapters/logctx"
	"github.com/albievan/cactustrade/internal/app"
)

// TestRequestLogNamesAccountAndMode (PLAN 7.2): the request log line of a request about an
// account names the account and its mode; no cookie, CSRF token or password is logged.
func TestRequestLogNamesAccountAndMode(t *testing.T) {
	logs := &syncBuffer{}
	log := slog.New(logctx.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	x := newAgentTCServer(t, func(*app.Deps) {}, Config{}, log)
	code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"Real one","mode":"real"}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create real %d %v", code, m)
	}
	realID := m["id"].(string)
	if code, _, _ := x.do("GET", "/api/v1/agent", ""); code != 200 {
		t.Fatalf("agent %d", code)
	}
	if code, _, _ := x.do("GET", "/api/v1/agent?accountId="+realID, ""); code != 200 {
		t.Fatalf("agent real %d", code)
	}
	if code, _, _ := x.do("GET", "/api/v1/auth/me", ""); code != 200 {
		t.Fatalf("me %d", code)
	}
	want := map[string][2]string{
		"/api/v1/agent?":                    {x.acct, "demo"},
		"/api/v1/agent?accountId=" + realID: {realID, "real"},
		"/api/v1/auth/me?":                  {"", ""},
	}
	got := map[string][2]string{}
	raw := logs.String()
	for _, l := range strings.Split(strings.TrimSpace(raw), "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		if e["msg"] != "http" || e["method"] != "GET" {
			continue
		}
		key := e["path"].(string) + "?"
		if e["path"] == "/api/v1/agent" && e["account_id"] == realID {
			key = "/api/v1/agent?accountId=" + realID
		}
		id, _ := e["account_id"].(string)
		mode, _ := e["mode"].(string)
		got[key] = [2]string{id, mode}
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: account/mode = %v, want %v", k, got[k], w)
		}
	}
	for _, secret := range []string{x.csrf, SessionCookie, "a long password", "test-key"} {
		if strings.Contains(raw, secret) {
			t.Errorf("the log holds %q", secret)
		}
	}
}
