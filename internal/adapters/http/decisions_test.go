package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// seedDecisions stores, for the demo account, an ordered decision and a decision the
// reviewer rejected with its logged LLM call, and returns their ids.
func (x *agentTC) seedDecisions(t *testing.T) (ordered, rejected string) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 11, 23, 15, 0, 5, 0, time.UTC)
	call := domain.LLMCall{
		ID: "call-1", AccountID: x.acct, Purpose: domain.LLMPurposeAgent, ProfileID: "prof", Provider: "openai", Model: "gpt-served",
		Prompt: `{"messages":[{"role":"user","content":"Proposed trade"}]}`, Response: `{"verdict":"reject"}`,
		InputTokens: 1_000, OutputTokens: 100, Latency: 1500 * time.Millisecond, CostMicros: 4_500, At: at,
	}
	if err := x.store.LLMCalls().Add(ctx, call); err != nil {
		t.Fatal(err)
	}
	sig := domain.DecisionSignal{
		Instrument: 101, Symbol: "AAPL", Direction: domain.Long, Playbook: "trend_breakout", Reason: "breakout",
		Entry: 100_000_000, StopLoss: 97_000_000, TakeProfit: 106_000_000, SuggestedSize: 2_000_00,
		RegimeProbs: map[domain.Regime]float64{domain.RegimeTrending: 0.8, domain.RegimeChoppy: 0.2},
	}
	ds := []domain.Decision{
		{
			ID: "dec-1", AccountID: x.acct, At: at, Signal: sig, Outcome: domain.DecisionRejectedReviewer,
			Verdict: domain.DecisionVerdict{Kind: domain.VerdictReject, Reason: "extended", LLMCallID: call.ID},
		},
		{
			ID: "dec-2", AccountID: x.acct, At: at.Add(5 * time.Minute), Signal: sig, Outcome: domain.DecisionOrdered, OrderRequestID: "req-1",
			Risk: domain.DecisionRisk{Allowed: true, Size: 1_000_00, Reason: "allowed"},
		},
	}
	for _, d := range ds {
		if err := x.store.Decisions().Add(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	return "dec-2", "dec-1"
}

func TestDecisionEndpoints(t *testing.T) {
	x := newAgentTC(t)
	ordered, rejected := x.seedDecisions(t)
	var list []Decision
	if code := x.getJSON("/api/v1/decisions", &list); code != 200 || len(list) != 2 || list[0].Id != ordered {
		t.Fatalf("list %d %+v", code, list)
	}
	d := list[1]
	if d.Symbol != "AAPL" || d.Entry != "100" || d.TakeProfit == nil || *d.TakeProfit != "106" || d.RegimeProbs["trending"] != 0.8 ||
		d.Verdict != "reject" || d.LlmCallId != "call-1" || d.Outcome != "rejected_reviewer" || d.OrderState != "" {
		t.Fatalf("decision %+v", d)
	}
	if code := x.getJSON("/api/v1/decisions?outcome=ordered&limit=5&accountId="+x.acct, &list); code != 200 || len(list) != 1 || list[0].RiskSizeCents != 1_000_00 {
		t.Fatalf("filtered %d %+v", code, list)
	}
	if code := x.getJSON("/api/v1/decisions?before=2026-11-23T15:01:00Z", &list); code != 200 || len(list) != 1 || list[0].Id != rejected {
		t.Fatalf("before %d %+v", code, list)
	}
	for _, bad := range []string{"limit=0", "limit=501", "outcome=won", "before=yesterday"} {
		if code, _, _ := x.do("GET", "/api/v1/decisions?"+bad, ""); code != 400 {
			t.Fatalf("%s: %d", bad, code)
		}
	}
	var det DecisionDetail
	if code := x.getJSON("/api/v1/decisions/"+rejected, &det); code != 200 || det.Call == nil || det.Call.Prompt == "" ||
		det.Call.Response != `{"verdict":"reject"}` || det.Call.CostUsd == nil || *det.Call.CostUsd != "0.0045" || det.Call.LatencyMs != 1500 {
		t.Fatalf("detail %d %+v", code, det)
	}
	det = DecisionDetail{}
	if code := x.getJSON("/api/v1/decisions/"+ordered, &det); code != 200 || det.Call != nil || det.Decision.OrderRequestId != "req-1" {
		t.Fatalf("detail without a call %d %+v", code, det)
	}
	// another account's decision is not found, and so is one of an unknown account
	code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"Second","mode":"demo"}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("second account %d %v", code, m)
	}
	other := m["id"].(string)
	for _, path := range []string{"/api/v1/decisions/" + rejected + "?accountId=" + other, "/api/v1/decisions/nope", "/api/v1/decisions?accountId=nope"} {
		if code, _, _ := x.do("GET", path, ""); code != 404 {
			t.Fatalf("%s: %d", path, code)
		}
	}
	if code := x.anon("GET", "/api/v1/decisions", ""); code != 401 {
		t.Fatalf("anonymous %d", code)
	}
}

func TestLlmUsageEndpoint(t *testing.T) {
	x := newAgentTC(t)
	x.seedDecisions(t)
	unpriced := domain.LLMCall{ID: "call-2", Purpose: domain.LLMPurposeTest, ProfileID: "gone", CostMicros: domain.CostUnknown, At: time.Date(2026, 11, 23, 14, 0, 0, 0, time.UTC)}
	if err := x.store.LLMCalls().Add(context.Background(), unpriced); err != nil {
		t.Fatal(err)
	}
	var u LlmUsage
	if code := x.getJSON("/api/v1/llm/usage?days=7", &u); code != 200 || u.Days != 7 || len(u.Rows) != 2 {
		t.Fatalf("usage %d %+v", code, u)
	}
	agent := u.Rows[1]
	if agent.Purpose != "agent" || agent.Calls != 1 || agent.InputTokens != 1_000 || agent.CostUsd == nil || *agent.CostUsd != "0.0045" {
		t.Fatalf("agent row %+v", agent)
	}
	if u.Rows[0].CostUsd != nil || u.Rows[0].CostUnknownCalls != 1 || u.Total.Calls != 2 || u.Total.CostUsd != nil || u.Total.KnownCostUsd != "0.0045" {
		t.Fatalf("unknown cost shown as known: %+v / %+v", u.Rows[0], u.Total)
	}
	if code := x.getJSON("/api/v1/llm/usage", &u); code != 200 || u.Days != 30 {
		t.Fatalf("default days %d %+v", code, u)
	}
	for _, bad := range []string{"0", "401", "x"} {
		if code, _, _ := x.do("GET", "/api/v1/llm/usage?days="+bad, ""); code != 400 {
			t.Fatalf("days=%s: %d", bad, code)
		}
	}
	code, m, _ := x.do("POST", "/api/v1/api-keys", `{"name":"ro","role":"viewer","expiresInDays":30}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create key %d %v", code, m)
	}
	key := m["secret"].(string)
	if code, _ := x.bearer("GET", "/api/v1/llm/usage", "", key); code != 403 {
		t.Fatalf("viewer usage %d", code)
	}
	if code, body := x.bearer("GET", "/api/v1/decisions/dec-1", "", key); code != 200 || !strings.Contains(body, "Proposed trade") || strings.Contains(body, "test-key") {
		t.Fatalf("viewer detail %d %s", code, body)
	}
}

func TestLlmProfilePrices(t *testing.T) {
	x := newAgentTC(t)
	csrf := x.csrfHdr()
	body := `{"name":"Priced","provider":"openai","model":"m","baseUrl":"","timeoutSeconds":30,"inputPricePerMTok":"3","outputPricePerMTok":"15.5"}`
	code, m, _ := x.do("POST", "/api/v1/llm/profiles", body, csrf...)
	if code != 201 || m["inputPricePerMTok"] != "3" || m["outputPricePerMTok"] != "15.5" {
		t.Fatalf("create %d %v", code, m)
	}
	id := m["id"].(string)
	if v, err := x.store.Settings().Get(context.Background(), "llm.profiles"); err != nil ||
		!strings.Contains(v, `"inputPriceMicros":3000000`) || !strings.Contains(v, `"outputPriceMicros":15500000`) {
		t.Fatalf("stored %s %v", v, err)
	}
	// a save with the prices the page shows keeps them; an empty price is unknown
	body = `{"name":"Renamed","provider":"openai","model":"m","baseUrl":"","timeoutSeconds":30,"inputPricePerMTok":"3","outputPricePerMTok":""}`
	if code, m, _ := x.do("PUT", "/api/v1/llm/profiles/"+id, body, csrf...); code != 200 || m["inputPricePerMTok"] != "3" || m["outputPricePerMTok"] != "" {
		t.Fatalf("update %d %v", code, m)
	}
	var setup LlmSetup
	if code := x.getJSON("/api/v1/llm", &setup); code != 200 || setup.Profiles[0].InputPricePerMTok != "3" || setup.Profiles[0].Name != "Renamed" {
		t.Fatalf("setup %d %+v", code, setup)
	}
	for _, bad := range []string{`"x"`, `"-1"`, `"1.0000001"`, `"1000001"`} {
		b := strings.Replace(body, `"inputPricePerMTok":"3"`, `"inputPricePerMTok":`+bad, 1)
		if code, m, _ := x.do("PUT", "/api/v1/llm/profiles/"+id, b, csrf...); code != 400 || m["field"] != "inputPricePerMTok" && m["field"] != "inputPrice" {
			t.Fatalf("price %s: %d %v", bad, code, m)
		}
	}
	raw, _ := json.Marshal(setup)
	if strings.Contains(string(raw), "apiKey") {
		t.Fatal("a key field in the setup")
	}
}

// servedReviewer answers every review with an approval from another model than asked for.
type servedReviewer struct{}

func (servedReviewer) Review(_ context.Context, cfg domain.LLMConfig, _ string, _ ports.ReviewInput) (domain.Verdict, domain.LLMCall, error) {
	return domain.Verdict{Kind: domain.VerdictApprove, Reason: "ok"},
		domain.LLMCall{Provider: cfg.Provider, Model: "fallback-model", Prompt: "{}", Response: "{}", InputTokens: 2_000, OutputTokens: 300}, nil
}

func (servedReviewer) Models(context.Context, domain.LLMConfig, string) ([]string, error) {
	return nil, nil
}

func TestLlmTestShowsModelTokensAndCost(t *testing.T) {
	x := newAgentTCWith(t, func(d *app.Deps) { d.LLM = servedReviewer{} })
	body := `{"name":"P","provider":"openai","model":"m","baseUrl":"","timeoutSeconds":30,"apiKey":"sk-abcdefgh","inputPricePerMTok":"3","outputPricePerMTok":"15"}`
	code, m, _ := x.do("POST", "/api/v1/llm/profiles", body, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("create %d %v", code, m)
	}
	code, m, _ = x.do("POST", "/api/v1/llm/profiles/"+m["id"].(string)+"/test", "", x.csrfHdr()...)
	// 2 000 × $3 + 300 × $15 per million = $0.0105
	if code != 200 || m["ok"] != true || m["model"] != "fallback-model" || m["inputTokens"] != 2000.0 || m["outputTokens"] != 300.0 ||
		m["costUsd"] != "0.0105" || m["callId"] == nil {
		t.Fatalf("test %d %v", code, m)
	}
	var u LlmUsage
	if code := x.getJSON("/api/v1/llm/usage", &u); code != 200 || len(u.Rows) != 1 || u.Rows[0].Purpose != "test" || u.Rows[0].ProfileName != "P" {
		t.Fatalf("usage %d %+v", code, u)
	}
}
