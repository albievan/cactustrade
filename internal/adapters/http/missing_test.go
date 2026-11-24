package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

// TestProblemMissingKeepsStatus: a refusal that names missing setup (R22) is answered with the
// same status, title and field as the refusal it wraps, plus missing and canFix, and its
// message for people as the detail.
func TestProblemMissingKeepsStatus(t *testing.T) {
	s := &Server{log: slog.New(slog.DiscardHandler)}
	budget := "Set a trading budget before starting the agent"
	tests := []struct {
		name   string
		before error // the refusal as it was
		what   domain.Missing
		agent  bool // mapped by failAgent
		field  string
	}{
		{"budget (agent)", domain.Invalid("tradingBudgetCents", budget), domain.MissingTradingBudget, true, "tradingBudgetCents"},
		{"keys (agent)", domain.ErrCredentialsMissing, domain.MissingAccountKeys, true, ""},
		{"keys on reconcile (agent)", fmt.Errorf("%w: %w", domain.ErrCredentialsMissing, domain.ErrUnavailable), domain.MissingAccountKeys, true, ""},
		{"kill switch (agent)", domain.ErrConflict, domain.MissingKillReset, true, ""},
		{"not reconciled (agent)", domain.ErrConflict, domain.MissingReconcile, true, ""},
		{"no account (agent)", domain.ErrNoAccount, domain.MissingAccount, true, ""},
		{"no account", domain.ErrNoAccount, domain.MissingAccount, false, ""},
		{"no mail server", domain.Invalid("enabled", "no mail server"), domain.MissingEmailServer, false, "enabled"},
		{"LLM key", domain.Invalid("apiKey", "Desk has no API key yet"), domain.MissingLLMProfile, false, "apiKey"},
		{"public URL", fmt.Errorf("x: %w", domain.ErrUnavailable), domain.MissingPublicURL, false, ""},
	}
	write := func(agent bool, err error) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/agent/start", nil)
		if agent {
			s.failAgent(w, r, err)
		} else {
			s.fail(w, r, err)
		}
		return w
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			was := write(tt.agent, fmt.Errorf("app.X: %w", tt.before))
			for _, canFix := range []bool{true, false} {
				me := &domain.MissingError{What: tt.what, Msg: "Fix it here", CanFix: canFix, Err: tt.before}
				w := write(tt.agent, fmt.Errorf("app.X: %w", me))
				if w.Code != was.Code || w.Header().Get("Content-Type") != "application/problem+json" {
					t.Fatalf("status %d (%s), was %d", w.Code, w.Header().Get("Content-Type"), was.Code)
				}
				var p Problem
				if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
					t.Fatal(err)
				}
				var old Problem
				_ = json.Unmarshal(was.Body.Bytes(), &old)
				if p.Status != w.Code || p.Title != old.Title || p.Detail == nil || *p.Detail != "Fix it here" {
					t.Fatalf("problem %+v, was %+v", p, old)
				}
				if p.Missing == nil || string(*p.Missing) != string(tt.what) || p.CanFix == nil || *p.CanFix != canFix {
					t.Fatalf("missing %v canFix %v", p.Missing, p.CanFix)
				}
				field := ""
				if p.Field != nil {
					field = *p.Field
				}
				if field != tt.field {
					t.Fatalf("field %q, want %q", field, tt.field)
				}
			}
			if containsKey(was.Body.String(), "missing") {
				t.Fatalf("a plain refusal names something missing: %s", was.Body.String())
			}
		})
	}
}

func containsKey(body, key string) bool {
	var m map[string]any
	_ = json.Unmarshal([]byte(body), &m)
	_, ok := m[key]
	return ok
}

// TestAgentMissingEndpoints: the agent panel lists what is missing, and Start/Stop/Reconcile
// refuse with the same status as before, naming what is missing, with a message without a
// field name.
func TestAgentMissingEndpoints(t *testing.T) {
	x := newAgentTC(t)
	csrf := x.csrfHdr()
	missing := func(m map[string]any) []string {
		var out []string
		for _, it := range m["missing"].([]any) {
			item := it.(map[string]any)
			if item["canFix"] != true {
				t.Fatalf("owner admin cannot fix %v", item)
			}
			out = append(out, item["what"].(string))
		}
		return out
	}
	code, m, _ := x.do("GET", "/api/v1/agent", "")
	if got := fmt.Sprint(missing(m)); code != 200 || got != "[trading_budget reconcile watchlist]" {
		t.Fatalf("agent %d missing %s", code, got)
	}
	check := func(name, path string, wantCode int, what, detail string) {
		t.Helper()
		code, m, _ := x.do("POST", path, "", csrf...)
		if code != wantCode || m["missing"] != what || m["canFix"] != true || (detail != "" && m["detail"] != detail) {
			t.Fatalf("%s: %d %v", name, code, m)
		}
	}
	check("start without a budget", "/api/v1/agent/start", 422, "trading_budget", "Set a trading budget before starting the agent")
	limits := `{"tradingBudgetCents":500000,"dailyLossBp":200,"drawdownBp":1000,"maxPositionBp":2000,"maxOpenPositions":5,` +
		`"noEntryBeforeCloseMinutes":30,"flattenBeforeCloseMinutes":10,"maxPortfolioAgeSeconds":5,"maxQuoteAgeSeconds":5,"maxShortBp":2500}`
	if code, m, _ := x.do("PUT", "/api/v1/risk/limits", limits, csrf...); code != 200 {
		t.Fatalf("limits %d %v", code, m)
	}
	check("start before reconciliation", "/api/v1/agent/start", 409, "reconcile", "")
	if code, m, _ := x.do("POST", "/api/v1/agent/reconcile", "", csrf...); code != 200 || m["ok"] != true {
		t.Fatalf("reconcile %d %v", code, m)
	}
	if code, _, _ := x.do("POST", "/api/v1/agent/kill", "", csrf...); code != 200 {
		t.Fatalf("kill %d", code)
	}
	check("start while killed", "/api/v1/agent/start", 409, "kill_reset", "The kill switch is on: an admin must reset it first")
	check("stop while killed", "/api/v1/agent/stop", 409, "kill_reset", "")
	a, err := x.store.Accounts().Get(context.Background(), x.acct)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := domain.AccountKeyNames(a)
	_ = x.sec.Delete(context.Background(), k)
	check("start without keys", "/api/v1/agent/start", 503, "account_keys", "The account's eToro keys are not set")
	check("reconcile without keys", "/api/v1/agent/reconcile", 503, "account_keys", "")
	code, m, _ = x.do("GET", "/api/v1/agent", "")
	if got := fmt.Sprint(missing(m)); code != 200 || got != "[account_keys kill_reset watchlist]" {
		t.Fatalf("agent %d missing %s", code, got)
	}
}

// TestNoAccountProblem: a user without an eToro account gets the 404 as before, naming the
// missing account.
func TestNoAccountProblem(t *testing.T) {
	x := newTC(t, nil)
	x.setupAndLogin()
	code, m, _ := x.do("GET", "/api/v1/watchlist", "")
	if code != 404 || m["missing"] != "account" || m["canFix"] != true || m["title"] != "No eToro account" {
		t.Fatalf("watchlist %d %v", code, m)
	}
}
