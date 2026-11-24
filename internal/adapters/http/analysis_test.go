package httpapi

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// TestAnalysisEndpoints covers R31's routes: the analysis JSON (groupings, narrative, absent
// win rate without trades), repeated accountId parameters, 404 for an account the caller
// cannot see, bad parameters, the PDF's headers, API keys, and the PDF's rate class.
func TestAnalysisEndpoints(t *testing.T) {
	r := &fakes.AnalysisRenderer{}
	x := newAgentTCServer(t, func(d *app.Deps) { d.Analysis = r }, Config{RateLimits: RateLimits{Export: 100}}, slog.New(slog.DiscardHandler))
	tr := domain.Trade{AccountID: x.acct, Mode: domain.ModeDemo, Ours: true, SyncedAt: time.Date(2026, 11, 23, 14, 0, 0, 0, time.UTC), ClosedTrade: domain.ClosedTrade{
		PositionID: 1, Instrument: 1, ClosedAt: time.Date(2026, 11, 23, 14, 0, 0, 0, time.UTC), NetProfit: 12_34, FeesKnown: true, Fees: 10, Long: true, Leverage: 1,
	}}
	if err := x.store.Trades().Upsert(context.Background(), tr); err != nil {
		t.Fatal(err)
	}

	var an Analysis
	if code := x.getJSON("/api/v1/analysis?preset=week", &an); code != 200 {
		t.Fatalf("analysis %d", code)
	}
	if an.Title != "23–27 November 2026" || an.Preset != "week" || an.Bucket != "day" || an.Days != 5 || !an.AllAccounts || len(an.Accounts) != 1 ||
		an.Totals.Trades != 1 || an.Totals.NetCents != 12_34 || an.Totals.WinRateBp == nil || *an.Totals.WinRateBp != 10_000 ||
		an.Totals.AvgNetCents == nil || *an.Totals.AvgNetCents != 12_34 || an.Totals.Best == nil || an.OursShareBp == nil || len(an.Groupings) != 7 ||
		len(an.Narrative) != 6 || an.Narrative[0].Heading != "Overview" || len(an.Notes) == 0 || an.Llm.KnownCostUsd == "" {
		t.Fatalf("analysis %+v", an)
	}
	if !strings.Contains(an.Narrative[0].Paragraphs[0], "cactusTrade closed 1 trade on 1 account") {
		t.Errorf("narrative %q", an.Narrative[0].Paragraphs)
	}

	// No trades: no win rate, no average, no share (absent, not 0).
	code, m, _ := x.do("GET", "/api/v1/analysis?preset=last_month&accountId="+x.acct+"&accountId="+x.acct, "")
	tot, _ := m["totals"].(map[string]any)
	if code != 200 || tot == nil || tot["trades"] != float64(0) || tot["winRateBp"] != nil || tot["avgNetCents"] != nil || m["oursShareBp"] != nil ||
		m["allAccounts"] != false || len(m["accounts"].([]any)) != 1 {
		t.Fatalf("empty analysis %d %v", code, m)
	}
	for q, want := range map[string]int{
		"accountId=someone-elses":             404,
		"preset=decade":                       400,
		"from=2026-11-01&to=2026-12-31":       400, // after today
		"preset=custom&from=2026-11-01":       400,
		"preset=custom&from=x&to=2026-11-02":  400,
		"from=2026-11-01&to=2026-11-20":       200,
		"preset=all&accountId=" + x.acct:      200,
		"preset=today&accountId=" + "missing": 404,
	} {
		code, _, _ := x.do("GET", "/api/v1/analysis?"+q, "")
		if code != want {
			t.Errorf("?%s: %d, want %d", q, code, want)
		}
	}

	// The PDF: an attachment, never cached; errors are problem+json.
	code, h, pdf := x.raw("/api/v1/analysis.pdf?preset=week", "")
	if code != 200 || h.Get("Content-Type") != "application/pdf" || h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Content-Disposition") != `attachment; filename="cactusTrade-analysis-2026-11-23-to-2026-11-27.pdf"` || !strings.HasPrefix(pdf, "%PDF") {
		t.Fatalf("pdf %d %v %q", code, h, pdf)
	}
	if code, h, _ := x.raw("/api/v1/analysis.pdf?accountId=nope", ""); code != 404 || !strings.HasPrefix(h.Get("Content-Type"), "application/problem+json") {
		t.Fatalf("pdf of another account %d %v", code, h)
	}
	if len(r.All()) != 1 {
		t.Fatalf("rendered %d", len(r.All()))
	}

	// A viewer's API key may read the analysis and its PDF.
	code, m, _ = x.do("POST", "/api/v1/api-keys", `{"name":"k","role":"viewer","expiresInDays":30}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("key %d %v", code, m)
	}
	key := m["secret"].(string)
	if code, _ := x.bearer("GET", "/api/v1/analysis?preset=week", "", key); code != 200 {
		t.Fatalf("API key analysis %d", code)
	}
	if code, _, _ := x.raw("/api/v1/analysis.pdf?preset=week", key); code != 200 {
		t.Fatalf("API key PDF %d", code)
	}

	if c := classify("GET", "/api/v1/analysis.pdf", "GET /api/v1/analysis.pdf"); c != classExport {
		t.Errorf("PDF: class %q, want export", c)
	}
	if c := classify("GET", "/api/v1/analysis", "GET /api/v1/analysis"); c != classRead {
		t.Errorf("analysis: class %q, want read", c)
	}
}
