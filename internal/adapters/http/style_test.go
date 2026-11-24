package httpapi

import (
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/strategy"
)

// TestRiskPresetsAndAgentStyle (R28): GET presets, PUT limits with {preset} or a custom set
// (never both, never a partial set), the preset the limits match, GET/PUT the agent's style,
// CSRF on every mutation and no change with an API key or while the agent runs.
func TestRiskPresetsAndAgentStyle(t *testing.T) {
	x := newAgentTC(t)
	x.ready(t)
	csrf := x.csrfHdr()
	code, m, _ := x.do("GET", "/api/v1/risk/presets", "")
	if code != 200 || len(m["presets"].([]any)) != 5 || m["current"] != "custom" || m["clamped"] != false {
		t.Fatalf("presets %d %v", code, m)
	}
	low := m["presets"].([]any)[0].(map[string]any)
	if low["id"] != "low" || low["label"] != "Low" || low["maxOpenPositions"] != 3.0 || !strings.Contains(low["summary"].(string), "no shorts") {
		t.Fatalf("low = %v", low)
	}
	full := `{"tradingBudgetCents":500000,"dailyLossBp":200,"drawdownBp":1000,"maxPositionBp":2000,"maxOpenPositions":8,` +
		`"noEntryBeforeCloseMinutes":30,"flattenBeforeCloseMinutes":10,"maxPortfolioAgeSeconds":5,"maxQuoteAgeSeconds":30,"maxShortBp":1000}`
	steps := []struct {
		name, method, path, body string
		hdr                      []string
		code                     int
		preset                   string
	}{
		{"preset without CSRF", "PUT", "/api/v1/risk/limits", `{"preset":"low"}`, nil, 403, ""},
		{"unknown preset", "PUT", "/api/v1/risk/limits", `{"preset":"wild"}`, csrf, 400, ""},
		{"preset and a limit", "PUT", "/api/v1/risk/limits", `{"preset":"low","dailyLossBp":50}`, csrf, 400, ""},
		{"a partial custom set", "PUT", "/api/v1/risk/limits", `{"dailyLossBp":50}`, csrf, 400, ""},
		{"an unknown field", "PUT", "/api/v1/risk/limits", `{"preset":"low","bogus":1}`, csrf, 400, ""},
		{"low", "PUT", "/api/v1/risk/limits", `{"preset":"low"}`, csrf, 200, "low"},
		{"a custom set that is the medium preset", "PUT", "/api/v1/risk/limits", full, csrf, 200, "medium"},
		{"high", "PUT", "/api/v1/risk/limits", `{"preset":"high"}`, csrf, 200, "high"},
		{"read back", "GET", "/api/v1/risk/limits", "", nil, 200, "high"},
	}
	for _, s := range steps {
		code, m, _ := x.do(s.method, s.path, s.body, s.hdr...)
		if code != s.code || (s.preset != "" && m["preset"] != s.preset) {
			t.Fatalf("%s: %d %v", s.name, code, m)
		}
		if s.preset == "high" && (m["tradingBudgetCents"] != 500000.0 || m["maxOpenPositions"] != 15.0 || m["canEdit"] != true) {
			t.Fatalf("%s: the budget was not kept or the preset not applied: %v", s.name, m)
		}
	}

	code, m, _ = x.do("GET", "/api/v1/agent/style", "")
	if code != 200 || m["aggressiveness"] != "medium" || m["autonomy"] != "medium" || m["canEdit"] != true ||
		len(m["aggressivenessLevels"].([]any)) != 4 || len(m["autonomyLevels"].([]any)) != 4 {
		t.Fatalf("style %d %v", code, m)
	}
	if pol := m["policy"].(map[string]any); pol["researchMode"] != "auto_add" || pol["scanEveryMinutes"] != 15.0 || pol["autoStart"] != false {
		t.Fatalf("policy %v", pol)
	}
	for _, s := range []struct {
		name, body string
		hdr        []string
		code       int
	}{
		{"style without CSRF", `{"autonomy":"full"}`, nil, 403},
		{"unknown level", `{"autonomy":"total"}`, csrf, 400},
		{"empty", `{}`, csrf, 400},
		{"unknown field", `{"autonomy":"full","leverage":2}`, csrf, 400},
		{"full autonomy, high aggressiveness", `{"autonomy":"full","aggressiveness":"high"}`, csrf, 200},
	} {
		if code, m, _ := x.do("PUT", "/api/v1/agent/style", s.body, s.hdr...); code != s.code {
			t.Fatalf("%s: %d %v", s.name, code, m)
		}
	}
	code, m, _ = x.do("GET", "/api/v1/agent/style", "")
	if code != 200 || m["autonomy"] != "full" || m["aggressiveness"] != "high" || m["effectiveAggressiveness"] != "high" ||
		m["policy"].(map[string]any)["autoStart"] != true || m["policy"].(map[string]any)["scanEveryMinutes"] != 5.0 {
		t.Fatalf("style after %d %v", code, m)
	}
	// With an API key: may read, never change.
	code, m, _ = x.do("POST", "/api/v1/api-keys", `{"name":"ops","role":"admin","expiresInDays":30}`, csrf...)
	if code != 201 {
		t.Fatalf("create key %d %v", code, m)
	}
	key := m["secret"].(string)
	if code, body := x.bearer("GET", "/api/v1/agent/style", "", key); code != 200 || !strings.Contains(body, `"canEdit":false`) {
		t.Fatalf("style with a key %d %s", code, body)
	}
	for path, body := range map[string]string{"/api/v1/agent/style": `{"autonomy":"low"}`, "/api/v1/risk/limits": `{"preset":"low"}`} {
		if code, out := x.bearer("PUT", path, body, key); code != 403 {
			t.Fatalf("PUT %s with a key %d %s", path, code, out)
		}
	}
	// While the agent runs, neither changes.
	if code, m, _ := x.do("POST", "/api/v1/agent/start", "", csrf...); code != 200 {
		t.Fatalf("start %d %v", code, m)
	}
	for path, body := range map[string]string{"/api/v1/agent/style": `{"autonomy":"low"}`, "/api/v1/risk/limits": `{"preset":"low"}`} {
		if code, m, _ := x.do("PUT", path, body, csrf...); code != 409 {
			t.Fatalf("PUT %s while running %d %v", path, code, m)
		}
	}
}

// TestReviewBoardCanAcceptAndLiveRegimeWords: the review board says whether the caller may
// accept proposals, and the live snapshot carries the market moods for people (R28).
func TestReviewBoardCanAcceptAndLiveRegimeWords(t *testing.T) {
	x := newAgentTC(t)
	x.ready(t)
	code, m, _ := x.do("GET", "/api/v1/reviews", "")
	if code != 200 || m["canAccept"] != true {
		t.Fatalf("reviews %d %v", code, m)
	}
	mood, _ := domain.RegimeTextOf(domain.RegimeChoppy)
	out := toLiveSnapshot(app.LiveSnapshot{
		RegimeLegend: domain.RegimeLegend,
		Regimes: []app.LiveRegime{
			{
				Instrument: 1, Symbol: "AAPL", Known: true, Regime: domain.RegimeChoppy, Probs: map[domain.Regime]float64{domain.RegimeChoppy: 0.7},
				Mood: mood, Stance: strategy.StanceReversion, MayDo: "May buy a dip back to the average",
			},
			{Instrument: 2, Symbol: "MSFT", Stance: strategy.StanceUnknown, MayDo: "Not reviewed yet"},
		},
	})
	if out.RegimeLegend == nil || len(*out.RegimeLegend) != 3 || (*out.RegimeLegend)[2].Label != "Volatile" {
		t.Fatalf("legend = %+v", out.RegimeLegend)
	}
	r0, r1 := out.Regimes[0], out.Regimes[1]
	if r0.Mood == nil || r0.Mood.Label != "Choppy" || *r0.Stance != Reversion || *r0.MayDo != "May buy a dip back to the average" {
		t.Fatalf("regime = %+v", r0)
	}
	if r1.Mood != nil || *r1.Stance != Unknown || *r1.MayDo != "Not reviewed yet" {
		t.Fatalf("unknown regime = %+v", r1)
	}
}
