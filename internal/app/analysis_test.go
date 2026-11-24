package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// seedAnalysis adds to the statement week (seed) a trade opened by a decision made before
// the week (its playbook joins by request id) and a theme holding AAPL; the clock moves to
// Saturday 28 November, so "this week" is 23–27 November.
func (s *stmtEnv) seedAnalysis(t *testing.T) domain.AccountView {
	t.Helper()
	isa := s.seed(t)
	ctx, st, id := s.ctx, s.store, s.a.ID
	tr := domain.Trade{AccountID: id, Mode: domain.ModeDemo, Ours: true, OpenRequestID: "r10", SyncedAt: at(26, 15, 0), ClosedTrade: domain.ClosedTrade{
		PositionID: 10, Instrument: 1, ClosedAt: at(26, 15, 0), NetProfit: 12_00, FeesKnown: true, Long: false, Leverage: 1,
	}}
	if err := st.Trades().Upsert(ctx, tr); err != nil {
		t.Fatal(err)
	}
	d := domain.Decision{
		ID: "d10", AccountID: id, At: at(20, 14, 0), Outcome: domain.DecisionOrdered, OrderRequestID: "r10",
		Signal: domain.DecisionSignal{Direction: domain.Short, Playbook: "trend_breakdown_short"}, Risk: domain.DecisionRisk{Allowed: true, Size: 100_00},
	}
	if err := st.Decisions().Add(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := st.Themes().Create(ctx, domain.Theme{ID: "th1", AccountID: id, Name: "Big tech", Enabled: true, Instruments: []domain.InstrumentID{1}}); err != nil {
		t.Fatal(err)
	}
	s.clock.T = at(28, 12, 0)
	return isa
}

func labels(gs []domain.AnalysisGroup) string {
	var out []string
	for _, g := range gs {
		out = append(out, g.Label+"="+domain.FormatMoney("USD", g.Stats.Net))
	}
	return strings.Join(out, ", ")
}

func TestAnalysisFromStoredRecords(t *testing.T) {
	s := newStmtEnv(t)
	isa := s.seedAnalysis(t)
	an, err := s.svc.Analysis(s.ctx, s.admin, AnalysisRequest{Preset: "week"})
	if err != nil {
		t.Fatal(err)
	}
	if an.Title != "23–27 November 2026" || !an.AllAccounts || len(an.Accounts) != 2 || an.Accounts[0].ID != isa.ID || an.Window.Bucket != domain.BucketDay {
		t.Fatalf("header %q %+v", an.Title, an.Accounts)
	}
	tot := an.Totals.Stats
	if tot.Count != 3 || tot.Net != 32_00 || tot.FeesUnknown != 1 || an.ForeignTrades != 1 || an.OursShareBp != 7500 {
		t.Fatalf("totals %+v foreign %d", tot, an.ForeignTrades)
	}
	for d, want := range map[domain.AnalysisDimension]string{
		domain.DimExchange:  "LSE=-$10.00, NYSE=$42.00", // BARC.L by its suffix
		domain.DimTheme:     "Big tech=$42.00, No theme=-$10.00",
		domain.DimPlaybook:  "trend_breakdown_short=$12.00, Not recorded=$20.00",
		domain.DimDirection: "Long=$20.00, Short=$12.00",
		domain.DimAccount:   "Main demo=$32.00",
	} {
		if got := labels(an.Grouping(d)); got != want {
			t.Errorf("%s = %s, want %s", d, got, want)
		}
	}
	if len(an.Grouping(domain.DimDate)) != 5 {
		t.Errorf("days %s", labels(an.Grouping(domain.DimDate)))
	}
	if an.Risk.KillSwitches != 1 || an.Risk.DailyStops != 1 || an.Risk.RiskRefusals != 1 || an.DecisionsTotal != 2 || an.LLM.Calls != 1 {
		t.Fatalf("risk %+v decisions %d llm %+v", an.Risk, an.DecisionsTotal, an.LLM)
	}
	if len(an.EquityTotals) != 2 || an.EquityTotals[0].Currency != "GBP" || an.EquityTotals[1].Closing != 10_020_00 || len(an.Curves) != 2 {
		t.Fatalf("equity %+v", an.EquityTotals)
	}

	// The same as a PDF, through the renderer.
	if _, err := s.svc.AnalysisPDF(s.ctx, s.admin, AnalysisRequest{Preset: "week"}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("no renderer: %v", err)
	}
	r := &fakes.AnalysisRenderer{}
	s.svc.d.Analysis = r
	f, err := s.svc.AnalysisPDF(s.ctx, s.admin, AnalysisRequest{From: "2026-11-23", To: "2026-11-27", AccountIDs: []string{s.a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "cactusTrade-analysis-2026-11-23-to-2026-11-27.pdf" || !strings.HasPrefix(string(f.Data), "%PDF-fake 23–27 November 2026") {
		t.Fatalf("file %s %q", f.Name, f.Data)
	}
	if got := r.All(); len(got) != 1 || got[0].AllAccounts || got[0].Window.Preset != domain.AnalysisPresetCustom || len(got[0].Accounts) != 1 {
		t.Fatalf("rendered %+v", got)
	}
	r.Err = errors.New("boom")
	if _, err := s.svc.AnalysisPDF(s.ctx, s.admin, AnalysisRequest{}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("renderer error: %v", err)
	}
	if _, err := s.svc.AnalysisPDF(s.ctx, Principal{}, AnalysisRequest{}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("anonymous PDF: %v", err)
	}
}

func TestAnalysisAccess(t *testing.T) {
	s := newStmtEnv(t)
	s.seedAnalysis(t)
	ctx := s.ctx
	olivia := s.user(t, "olivia", domain.RoleOperator)
	victor := s.user(t, "victor", domain.RoleViewer)
	ov, err := s.svc.CreateAccount(ctx, olivia, "Olivia demo", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.svc.ShareAccount(ctx, s.admin, s.a.ID, "victor", domain.AccessView); err != nil {
		t.Fatal(err)
	}
	key := s.admin
	key.Via, key.APIKeyID = ViaAPIKey, "key-1"
	many := make([]string, 51)
	for i := range many {
		many[i] = s.a.ID
	}
	tests := []struct {
		name     string
		p        Principal
		req      AnalysisRequest
		want     error
		accounts int
	}{
		{"admin, everything they can see", s.admin, AnalysisRequest{}, nil, 2},
		{"admin, an account twice", s.admin, AnalysisRequest{AccountIDs: []string{s.a.ID, s.a.ID}}, nil, 1},
		{"admin, another user's account", s.admin, AnalysisRequest{AccountIDs: []string{ov.ID}}, domain.ErrNotFound, 0},
		{"operator, own account", olivia, AnalysisRequest{AccountIDs: []string{ov.ID}}, nil, 1},
		{"operator, an account not shared with them", olivia, AnalysisRequest{AccountIDs: []string{s.a.ID}}, domain.ErrNotFound, 0},
		{"operator, everything they can see", olivia, AnalysisRequest{Preset: "all"}, nil, 1},
		{"viewer, a shared account", victor, AnalysisRequest{AccountIDs: []string{s.a.ID}}, nil, 1},
		{"viewer, an unknown account", victor, AnalysisRequest{AccountIDs: []string{"nope"}}, domain.ErrNotFound, 0},
		{"API key", key, AnalysisRequest{Preset: "today"}, nil, 2},
		{"nobody", Principal{}, AnalysisRequest{}, domain.ErrUnauthenticated, 0},
		{"too many accounts", s.admin, AnalysisRequest{AccountIDs: many}, domain.ErrInvalid, 0},
		{"unknown preset", s.admin, AnalysisRequest{Preset: "decade"}, domain.ErrInvalid, 0},
		{"a range in the future", s.admin, AnalysisRequest{From: "2026-11-20", To: "2026-12-01"}, domain.ErrInvalid, 0},
		{"custom without to", s.admin, AnalysisRequest{Preset: "custom", From: "2026-11-20"}, domain.ErrInvalid, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			an, err := s.svc.Analysis(ctx, tc.p, tc.req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if err == nil && len(an.Accounts) != tc.accounts {
				t.Fatalf("accounts %+v", an.Accounts)
			}
		})
	}
	// The default is this month (November).
	an, err := s.svc.Analysis(ctx, s.admin, AnalysisRequest{})
	if err != nil || an.Window.Preset != domain.AnalysisPresetMonth || an.Title != "November 2026" {
		t.Fatalf("default %+v %v", an.Window, err)
	}
}
