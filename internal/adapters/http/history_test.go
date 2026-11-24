package httpapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// raw sends a GET with the session cookie (key "") or an API key and returns the status,
// the headers and the body.
func (x *agentTC) raw(path, key string) (int, http.Header, string) {
	x.t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", x.srv.URL+path, nil)
	c := x.c
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
		c = &http.Client{Transport: x.c.Transport}
	}
	resp, err := c.Do(req)
	if err != nil {
		x.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

// historyClose is when the seeded trades closed (risk day 2026-11-23).
var historyClose = time.Date(2026, 11, 23, 14, 0, 0, 0, time.UTC)

// seedTrades stores three closed trades on the demo account: ours with fees, ours with
// unknown fees, and one cactusTrade did not open.
func (x *agentTC) seedTrades(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	trades := []domain.Trade{
		{AccountID: x.acct, Mode: domain.ModeDemo, Ours: true, OpenRequestID: "req-1", ClosedTrade: domain.ClosedTrade{
			PositionID: 1, Instrument: 101, OpenedAt: historyClose.Add(-time.Hour), ClosedAt: historyClose, OpenRate: 100_000_000, CloseRate: 103_000_000,
			NetProfit: 59_00, Long: true, Leverage: 1, Fees: 1_00, FeesKnown: true, Investment: 2_000_00, Units: 20_000_000, OrderID: 9,
			StopLoss: 97_000_000, TakeProfit: 106_000_000,
		}},
		{AccountID: x.acct, Mode: domain.ModeDemo, Ours: true, OpenRequestID: "req-2", ClosedTrade: domain.ClosedTrade{
			PositionID: 2, Instrument: 101, OpenedAt: historyClose.Add(-time.Hour), ClosedAt: historyClose.Add(time.Minute), OpenRate: 100_000_000,
			CloseRate: 99_000_000, NetProfit: -10_00, Long: false, Leverage: 1, Investment: 1_000_00, Units: 10_000_000,
		}},
		{AccountID: x.acct, Mode: domain.ModeDemo, ClosedTrade: domain.ClosedTrade{
			PositionID: 3, Instrument: 102, OpenedAt: historyClose.Add(-time.Hour), ClosedAt: historyClose.Add(2 * time.Minute), OpenRate: 5_000_000,
			CloseRate: 5_000_000, Long: true, Leverage: 1, FeesKnown: true, Investment: 500_00, Units: 100_000_000,
		}},
	}
	for _, tr := range trades {
		if err := x.store.Trades().Upsert(ctx, tr); err != nil {
			t.Fatal(err)
		}
	}
	for i, eq := range []domain.Cents{10_000_00, 10_100_00, 9_900_00} {
		if err := x.store.EquitySnapshots().Add(ctx, domain.EquitySnapshot{
			AccountID: x.acct, At: historyClose.Add(time.Duration(i) * 10 * time.Minute), Currency: "USD", Cash: eq, Equity: eq,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoryEndpoints(t *testing.T) {
	x := newAgentTC(t)
	x.seedTrades(t)

	var trades []HistoryTrade
	if code := x.getJSON("/api/v1/history/trades", &trades); code != 200 || len(trades) != 3 || trades[0].PositionId != 3 {
		t.Fatalf("trades %d %+v", code, trades)
	}
	if tr := trades[2]; tr.FeesCents == nil || *tr.FeesCents != 1_00 || !tr.Ours || tr.Units != "20" || tr.OpenRate != "100" ||
		tr.StopLoss == nil || *tr.StopLoss != "97" || tr.InvestedCents != 2_000_00 || tr.Direction != "long" || tr.EtoroOrderId != 9 {
		t.Fatalf("trade 1 = %+v", tr)
	}
	if tr := trades[1]; tr.FeesCents != nil || tr.FeesKnown || tr.Direction != "short" || tr.TakeProfit != nil {
		t.Fatalf("trade 2 (unknown fees) = %+v", tr)
	}
	if code := x.getJSON("/api/v1/history/trades?ours=false&limit=10&accountId="+x.acct, &trades); code != 200 || len(trades) != 1 || trades[0].Ours {
		t.Fatalf("foreign trades %d %+v", code, trades)
	}
	if code := x.getJSON("/api/v1/history/trades?before=2026-11-23T14:01:00Z", &trades); code != 200 || len(trades) != 1 || trades[0].PositionId != 1 {
		t.Fatalf("before %d %+v", code, trades)
	}

	var sum HistorySummary
	if code := x.getJSON("/api/v1/history/summary?days=7", &sum); code != 200 {
		t.Fatalf("summary %d", code)
	}
	if sum.Days != 7 || sum.Trades != 2 || sum.Wins != 1 || sum.Losses != 1 || sum.WinRateBp == nil || *sum.WinRateBp != 5_000 ||
		sum.NetCents != 49_00 || sum.FeesCents != 1_00 || sum.GrossCents != 50_00 || sum.FeesUnknownTrades != 1 || sum.ForeignTrades != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if dd := sum.Drawdown; !dd.Known || dd.PeakCents != 10_100_00 || dd.MaxDrawdownCents != 200_00 || dd.PeakAt == nil {
		t.Fatalf("drawdown = %+v", dd)
	}

	var daily DailyHistory
	if code := x.getJSON("/api/v1/history/daily", &daily); code != 200 || daily.Days != 30 || len(daily.Rows) != 1 {
		t.Fatalf("daily %d %+v", code, daily)
	}
	if d := daily.Rows[0]; d.Day != "2026-11-23" || d.Trades != 2 || d.RealisedCents != 49_00 || d.EquityEndCents == nil ||
		*d.EquityEndCents != 9_900_00 || d.EquityChangeCents == nil || *d.EquityChangeCents != -100_00 {
		t.Fatalf("day = %+v", d)
	}
}

func TestHistoryEmpty(t *testing.T) {
	x := newAgentTC(t)
	var sum HistorySummary
	if code := x.getJSON("/api/v1/history/summary", &sum); code != 200 || sum.Trades != 0 || sum.WinRateBp != nil || sum.Drawdown.Known {
		t.Fatalf("empty summary %d %+v", code, sum)
	}
	code, _, body := x.raw("/api/v1/history/trades", "")
	if code != 200 || strings.TrimSpace(body) != "[]" {
		t.Fatalf("empty trades %d %s", code, body)
	}
}

func TestHistoryStatusCodes(t *testing.T) {
	x := newAgentTC(t)
	x.seedTrades(t)
	code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"Tiger paper","mode":"demo","platform":"tiger"}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("tiger account %d %v", code, m)
	}
	tiger := m["id"].(string)
	tests := []struct {
		path string
		code int
	}{
		{"/api/v1/history/summary?days=0", 400},
		{"/api/v1/history/summary?days=401", 400},
		{"/api/v1/history/summary?days=x", 400},
		{"/api/v1/history/daily?days=-1", 400},
		{"/api/v1/history/trades?limit=0", 400},
		{"/api/v1/history/trades?limit=501", 400},
		{"/api/v1/history/trades?ours=maybe", 400},
		{"/api/v1/history/trades?from=2026-11-24T00:00:00Z&to=2026-11-23T00:00:00Z", 400},
		{"/api/v1/history/export/trades.csv?from=yesterday", 400},
		{"/api/v1/history/export/trades.csv?from=2026-11-24T00:00:00Z&to=2026-11-23T00:00:00Z", 400},
		{"/api/v1/history/summary?accountId=nope", 404},
		{"/api/v1/history/trades?accountId=nope", 404},
		{"/api/v1/history/export/daily.csv?accountId=nope", 404},
		{"/api/v1/history/export/positions.csv", 404},
		{"/api/v1/history/export/trades", 404},
		{"/api/v1/history/summary?accountId=" + tiger, 403},
		{"/api/v1/history/export/trades.csv?accountId=" + tiger, 403},
		{"/api/v1/history/summary", 200},
		{"/api/v1/history/daily?days=400", 200},
	}
	for _, tt := range tests {
		code, hdr, body := x.raw(tt.path, "")
		if code != tt.code {
			t.Fatalf("%s: %d %s, want %d", tt.path, code, body, tt.code)
		}
		if code >= 400 && !strings.HasPrefix(hdr.Get("Content-Type"), "application/problem+json") {
			t.Fatalf("%s: %s", tt.path, hdr.Get("Content-Type"))
		}
	}
	// Signed out: 401.
	resp, err := (&http.Client{Transport: x.c.Transport}).Get(x.srv.URL + "/api/v1/history/export/trades.csv")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("signed out: %d", resp.StatusCode)
	}
}

func readCSV(t *testing.T, body string) [][]string {
	t.Helper()
	recs, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("csv %v: %s", err, body)
	}
	return recs
}

func TestHistoryCSVExport(t *testing.T) {
	x := newAgentTC(t)
	x.seedTrades(t)
	x.seedDecisions(t)
	code, m, _ := x.do("POST", "/api/v1/api-keys", `{"name":"exports","role":"viewer","expiresInDays":30}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("viewer key %d %v", code, m)
	}
	viewerKey := m["secret"].(string)
	tests := []struct {
		file    string
		key     string
		header0 string
		rows    int
	}{
		{"trades.csv", "", "closed_at", 3},
		{"trades.csv", viewerKey, "closed_at", 3},
		{"daily.csv", viewerKey, "risk_day", 1},
		{"equity.csv", "", "at", 3},
		{"orders.csv", "", "created_at", 0},
		{"decisions.csv", viewerKey, "at", 2},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			code, hdr, body := x.raw("/api/v1/history/export/"+tt.file+"?accountId="+x.acct+"&from=2026-11-01T00:00:00Z&to=2026-11-24T00:00:00Z", tt.key)
			if code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			kind := strings.TrimSuffix(tt.file, ".csv")
			for h, want := range map[string]string{
				"Content-Type":           "text/csv; charset=utf-8",
				"Content-Disposition":    `attachment; filename="cactustrade-` + kind + `-` + x.acct + `.csv"`,
				"X-Content-Type-Options": "nosniff",
				"Cache-Control":          "no-store",
			} {
				if got := hdr.Get(h); got != want {
					t.Fatalf("%s = %q, want %q", h, got, want)
				}
			}
			recs := readCSV(t, body)
			if len(recs) != tt.rows+1 || recs[0][0] != tt.header0 {
				t.Fatalf("csv = %v", recs)
			}
		})
	}
	// The trades file: exact amounts, UTC times, unknown fees, the ours column.
	_, _, body := x.raw("/api/v1/history/export/trades.csv", "")
	recs := readCSV(t, body)
	col := func(name string) int { return slices.Index(recs[0], name) }
	first, second := recs[1], recs[2]
	if first[col("closed_at")] != "2026-11-23T14:00:00Z" || first[col("net_profit_usd")] != "59.00" || first[col("fees_usd")] != "1.00" ||
		first[col("invested_usd")] != "2000.00" || first[col("units")] != "20" || first[col("ours")] != "true" || first[col("stop_loss_rate")] != "97" {
		t.Fatalf("first trade = %v", first)
	}
	if second[col("fees_usd")] != "unknown" || second[col("net_profit_usd")] != "-10.00" || second[col("direction")] != "short" {
		t.Fatalf("second trade = %v", second)
	}
	if x.b.Opens != 0 || x.b.Closes != 0 || x.b.Cancels != 0 {
		t.Fatal("an export reached the broker")
	}
}

// bigEquity is a store whose account has one equity snapshot more than an export may hold.
type bigEquity struct{ ports.Store }

func (s bigEquity) EquitySnapshots() ports.EquitySnapshots {
	return bigSnapshots{s.Store.EquitySnapshots()}
}

type bigSnapshots struct{ ports.EquitySnapshots }

func (b bigSnapshots) List(_ context.Context, ids []string, _ time.Time) ([]domain.EquitySnapshot, error) {
	if len(ids) != 1 {
		return nil, nil
	}
	out := make([]domain.EquitySnapshot, domain.ExportMaxRows+1)
	for i := range out {
		out[i] = domain.EquitySnapshot{AccountID: ids[0], At: historyClose.Add(-time.Duration(i) * time.Second), Equity: 1}
	}
	slices.Reverse(out)
	return out, nil
}

func TestHistoryTooLarge(t *testing.T) {
	x := newAgentTCWith(t, func(d *app.Deps) { d.Store = bigEquity{d.Store} })
	code, hdr, body := x.raw("/api/v1/history/export/equity.csv", "")
	if code != 422 || !strings.HasPrefix(hdr.Get("Content-Type"), "application/problem+json") || !strings.Contains(body, "narrow the date range") {
		t.Fatalf("export over the cap %d %s", code, body)
	}
	if hdr.Get("Content-Disposition") != "" {
		t.Fatal("a refused export was sent as a file")
	}
}

func TestCSVInjectionNeutralised(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"=1+1", "'=1+1"},
		{"-2", "'-2"},
		{"+3", "'+3"},
		{"@SUM(A1:A2)", "'@SUM(A1:A2)"},
		{"\tcmd", "'\tcmd"},
		{"\r=1", "'\r=1"},
		{"=HYPERLINK(\"http://x\")", "'=HYPERLINK(\"http://x\")"},
		{"AAPL", "AAPL"},
		{"", ""},
		{"a=1", "a=1"},
		{" =1", " =1"},
	}
	for _, tt := range tests {
		if got := csvCell(tt.in); got != tt.want {
			t.Fatalf("csvCell(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	// Through the writer, read back as a spreadsheet would: every formula cell is text.
	var buf bytes.Buffer
	e := app.Export{Kind: app.ExportDecisions, Columns: []string{"reason", "symbol"}, Rows: [][]any{{"=1+1", "-2"}, {"@SUM(A1)", "\tx"}}}
	if err := writeCSV(&buf, e); err != nil {
		t.Fatal(err)
	}
	for _, rec := range readCSV(t, buf.String())[1:] {
		for _, cell := range rec {
			if !strings.HasPrefix(cell, "'") {
				t.Fatalf("cell %q not neutralised", cell)
			}
		}
	}
	// A cell type the export does not list is text too.
	if got := csvCell(domain.Direction("=x")); got != "'=x" {
		t.Fatalf("other type = %q", got)
	}
}

func TestCSVAmountsExact(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{domain.Cents(0), "0.00"},
		{domain.Cents(5), "0.05"},
		{domain.Cents(-5), "-0.05"},
		{domain.Cents(123_45), "123.45"},
		{domain.Cents(-100), "-1.00"},
		{domain.Cents(9_000_000_000_000_000), "90000000000000.00"},
		{domain.Cents(-9_000_000_000_000_000), "-90000000000000.00"},
		{domain.Cents(900_000_000_000_001), "9000000000000.01"},
		{domain.Cents(-900_000_000_000_001), "-9000000000000.01"},
		{domain.Cents(math.MaxInt64), "92233720368547758.07"},
		{domain.Cents(math.MinInt64), "-92233720368547758.08"},
		{domain.Micros(189_100_000), "189.1"},
		{domain.Micros(-1), "-0.000001"},
		{domain.Micros(3_000_000), "3"},
		{domain.Micros(0), "0"},
		{domain.Micros(math.MinInt64), "-9223372036854.775808"},
		{domain.BasisPoints(-250), "-250"},
		{int64(-7), "-7"},
		{42, "42"},
		{true, "true"},
		{nil, ""},
		{time.Time{}, ""},
		{time.Date(2026, 11, 23, 16, 0, 5, 0, time.FixedZone("CET", 3600)), "2026-11-23T15:00:05Z"},
		{time.Date(2026, 11, 23, 15, 0, 5, 1_500_000, time.UTC), "2026-11-23T15:00:05.0015Z"},
	}
	for _, tt := range tests {
		if got := csvCell(tt.in); got != tt.want {
			t.Fatalf("csvCell(%#v) = %q, want %q", tt.in, got, tt.want)
		}
	}
	// Every amount in ±9e15 cents round-trips exactly (no float anywhere).
	for _, c := range []domain.Cents{-9e15, -9e15 + 1, -1, 1, 9e15 - 1, 9e15, math.MaxInt64, math.MinInt64} {
		got := csvCell(c)
		whole, frac, ok := strings.Cut(got, ".")
		back, err := strconv.ParseInt(whole+frac, 10, 64)
		if !ok || len(frac) != 2 || err != nil || back != int64(c) {
			t.Fatalf("%d cents → %q → %d (%v)", c, got, back, err)
		}
	}
}

func TestCSVWriterRowShape(t *testing.T) {
	var buf bytes.Buffer
	err := writeCSV(&buf, app.Export{Kind: app.ExportTrades, Columns: []string{"a", "b"}, Rows: [][]any{{"x"}}})
	if err == nil {
		t.Fatal("a short row was written")
	}
	if got := exportFileName(app.ExportTrades, `a"b/../c\n`); got != "cactustrade-trades-abcn.csv" {
		t.Fatalf("file name = %q", got)
	}
	// The 422 mapping on its own.
	s := &Server{log: slog.New(slog.DiscardHandler)}
	rec := httptest.NewRecorder()
	s.fail(rec, httptest.NewRequest("GET", "/api/v1/history/export/trades.csv", nil), domain.ErrTooLarge)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ErrTooLarge → %d", rec.Code)
	}
}
