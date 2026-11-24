package pdf

import (
	"bytes"
	"compress/zlib"
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// update rewrites the committed sample: go test ./internal/adapters/pdf -run TestGolden -update
var update = flag.Bool("update", false, "rewrite testdata/statement-weekly-combined.pdf")

const samplePath = "testdata/statement-weekly-combined.pdf"

// fixture is a weekly combined statement over two eToro accounts (USD), a Trading 212 ISA
// (GBP) and a Saxo account (AUD) — every figure the golden test looks for.
func fixture(scope domain.StatementScope, exchange string) domain.Statement {
	w, _ := domain.WindowFor(domain.PeriodWeekly, "2026-11-24")
	at := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, time.UTC) }
	tr := func(acct string, pos int64, sym string, m domain.Market, net, fees domain.Cents, known, ours bool, closed time.Time) domain.StatementTrade {
		return domain.StatementTrade{
			Trade: domain.Trade{AccountID: acct, Mode: domain.ModeDemo, Ours: ours, ClosedTrade: domain.ClosedTrade{
				PositionID: pos, ClosedAt: closed, NetProfit: net, Fees: fees, FeesKnown: known, Long: true, Leverage: 1,
			}},
			Symbol: sym, Market: m,
		}
	}
	in := domain.StatementInput{
		Window: w, Scope: scope, Exchange: exchange, GeneratedAt: time.Date(2026, 11, 28, 6, 0, 0, 0, time.UTC),
		Accounts: []domain.StatementAccountInput{
			{
				Account: domain.Account{ID: "a1", Name: "Demo main", Mode: domain.ModeDemo},
				Trades: []domain.StatementTrade{
					tr("a1", 11, "BARC.L", domain.MarketLSE, 25_00, 1_00, true, true, at(24, 10)),
					tr("a1", 12, "AAPL", domain.MarketNYSE, -40_00, 0, false, true, at(24, 15)),
					tr("a1", 13, "MSFT", domain.MarketNYSE, 0, 50, true, true, at(25, 15)),
					tr("a1", 14, "TSLA", domain.MarketNYSE, 99_00, 0, true, false, at(25, 16)),
				},
				Snapshots: []domain.EquitySnapshot{
					{AccountID: "a1", At: at(23, 9), Currency: "USD", Equity: 10_000_00},
					{AccountID: "a1", At: at(24, 9), Currency: "USD", Equity: 10_100_00},
					{AccountID: "a1", At: at(27, 20), Currency: "USD", Equity: 9_985_00},
				},
				OpenPositions: []domain.StatementPosition{{PositionID: 99, Symbol: "CBA.ASX", Market: domain.MarketASX, Direction: domain.Long, OpenedAt: at(27, 1)}},
				Decisions:     map[domain.DecisionOutcome]int{domain.DecisionOrdered: 3, domain.DecisionRejectedRisk: 2},
				Orders:        map[domain.OrderState]int{domain.OrderFilled: 3, domain.OrderCancelled: 1},
				KillSwitches:  1,
				LLM:           domain.LLMUsage{Calls: 5, InputTokens: 6_000, OutputTokens: 400, Cost: 120_000},
			},
			{
				Account:   domain.Account{ID: "a2", Name: "Démo second 日本", Mode: domain.ModeDemo},
				Trades:    []domain.StatementTrade{tr("a2", 21, "CBA.ASX", domain.MarketASX, 60_00, 2_00, true, true, at(26, 3))},
				Snapshots: []domain.EquitySnapshot{{AccountID: "a2", At: at(26, 20), Currency: "USD", Equity: 500_00}},
			},
			{
				Account: domain.Account{ID: "t1", Name: "ISA", Mode: domain.ModeReal, Platform: domain.PlatformTrading212},
				Snapshots: []domain.EquitySnapshot{
					{AccountID: "t1", At: at(23, 9), Currency: "GBP", Equity: 1_000_00},
					{AccountID: "t1", At: at(25, 12), Currency: "GBP", Equity: 1_010_00},
				},
				Holdings: &domain.Holdings{
					Platform: domain.PlatformTrading212, Currency: "GBP", Cash: 510_00, Equity: 1_012_00, AsOf: at(28, 5),
					Positions: []domain.Holding{
						{Symbol: "VOD", Name: "Vodafone", Quantity: 400_000_000, Value: 502_00, Cost: 480_00, PnL: 22_00},
						{Symbol: "XYZ", Quantity: 1_000_000, Unknown: true},
					},
				},
			},
			{
				Account:       domain.Account{ID: "s1", Name: "Saxo AU", Mode: domain.ModeDemo, Platform: domain.PlatformSaxo},
				Snapshots:     []domain.EquitySnapshot{{AccountID: "s1", At: at(24, 1), Currency: "AUD", Equity: 2_500_00}, {AccountID: "s1", At: at(27, 5), Currency: "AUD", Equity: 2_450_50}},
				HoldingsError: "Reconnect to Saxo",
			},
		},
	}
	return domain.BuildStatement(in)
}

// pdfText checks the PDF's structure and returns its text (each shown string on its own
// line, decoded from Windows-1252) and its page count, with the standard library only.
func pdfText(t *testing.T, b []byte) (string, int) {
	t.Helper()
	if !bytes.HasPrefix(b, []byte("%PDF-1.")) || !bytes.Contains(b[max(0, len(b)-1024):], []byte("%%EOF")) {
		t.Fatalf("not a PDF: %q … %q", b[:min(len(b), 16)], b[max(0, len(b)-16):])
	}
	for _, want := range []string{"xref", "trailer", "/Root", "/Type /Catalog", "/BaseFont /Helvetica"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("PDF lacks %q", want)
		}
	}
	pages := len(regexp.MustCompile(`/Type /Page[^s]`).FindAll(b, -1))
	inv := map[byte]rune{}
	for r, c := range cp1252 {
		inv[c] = r
	}
	var out strings.Builder
	rest := b
	for {
		i := bytes.Index(rest, []byte("stream\n"))
		if i < 0 {
			break
		}
		rest = rest[i+len("stream\n"):]
		j := bytes.Index(rest, []byte("\nendstream"))
		if j < 0 {
			t.Fatal("unterminated stream")
		}
		raw := rest[:j]
		rest = rest[j+len("\nendstream"):]
		if zr, err := zlib.NewReader(bytes.NewReader(raw)); err == nil {
			if dec, err := io.ReadAll(zr); err == nil {
				raw = dec
			}
		}
		for _, s := range literalStrings(raw) {
			for _, c := range []byte(s) {
				if r, ok := inv[c]; ok {
					out.WriteRune(r)
				} else {
					out.WriteRune(rune(c)) // ASCII and Latin-1 are the same code points
				}
			}
			out.WriteByte('\n')
		}
	}
	return out.String(), pages
}

// literalStrings returns the (…) strings shown with Tj in a content stream, unescaped.
func literalStrings(c []byte) []string {
	var out []string
	for i := 0; i < len(c); i++ {
		if c[i] != '(' {
			continue
		}
		var s []byte
		depth := 1
		for i++; i < len(c) && depth > 0; i++ {
			switch ch := c[i]; {
			case ch == '\\' && i+1 < len(c):
				i++
				switch e := c[i]; {
				case e == 'n':
					s = append(s, '\n')
				case e == 'r':
					s = append(s, '\r')
				case e >= '0' && e <= '7':
					v, n := 0, 0
					for n < 3 && i < len(c) && c[i] >= '0' && c[i] <= '7' {
						v = v*8 + int(c[i]-'0')
						i++
						n++
					}
					i--
					s = append(s, byte(v))
				default:
					s = append(s, e)
				}
			case ch == '(':
				depth++
				s = append(s, ch)
			case ch == ')':
				depth--
				if depth > 0 {
					s = append(s, ch)
				}
			default:
				s = append(s, ch)
			}
		}
		if bytes.HasPrefix(bytes.TrimLeft(c[i:], " "), []byte("Tj")) {
			out = append(out, string(s))
		}
		i--
	}
	return out
}

func render(t *testing.T, st domain.Statement) []byte {
	t.Helper()
	b, err := New().Render(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestGoldenStatement renders the weekly combined statement: a valid PDF with pages
// numbered "Page n of N", the header, the key figures (USD, £ and A$ never added together,
// unknown fees, the summary), the footer's disclaimer on every page, and the same text as the
// committed sample (testdata/statement-weekly-combined.pdf, regenerated with -update).
func TestGoldenStatement(t *testing.T) {
	st := fixture(domain.ScopeCombined, "")
	b := render(t, st)
	if again := render(t, st); !bytes.Equal(b, again) {
		t.Fatal("the same statement gave different bytes")
	}
	text, pages := pdfText(t, b)
	if pages < 2 || pages > 4 {
		t.Fatalf("pages = %d", pages)
	}
	for _, want := range []string{
		"cactus", "Trade", "Weekly statement · 23–27 November 2026", "Combined statement",
		"Demo main (eToro, demo); Démo second ?? (eToro, demo);", "ISA (Trading 212, real, view-only)", "(Saxo, demo, view-only)",
		"Summary",
		// figures
		"$45.00", "$3.50 + unknown (1 trade)", "Fees unknown", "Recorded equity went from A$2,500.00 to A$2,450.50 (AUD, 1 account) and from £1,000.00 to £1,010.00", "50.00 %", "$60.00 (CBA.ASX, Démo second ??)", "-$40.00 (AAPL, Demo main)",
		"£1,000.00", "£1,010.00", "£10.00", "A$2,500.00", "A$2,450.50", "-A$49.50", "$10,500.00", "$10,485.00", "-$15.00",
		"Total GBP (1 account)", "Total USD (2 accounts)", "Total AUD (1 account)",
		"LSE", "NYSE", "ASX", "CBA.ASX", "Vodafone", "unknown", "Could not be read: Reconnect to Saxo.",
		"Kill switch used", "Order sent", "Filled", "$0.1200",
		"Page 1 of " + string(rune('0'+pages)),
		"Generated 28 November 2026 06:00 UTC · cactusTrade is software, not financial advice — trading can lose money",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q", want)
		}
	}
	if n := strings.Count(text, "software, not financial advice — trading can lose money"); n != pages {
		t.Errorf("disclaimer on %d of %d pages", n, pages)
	}
	if strings.Contains(text, "{nb}") {
		t.Error("page total not filled in")
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(samplePath), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(samplePath, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sample, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatalf("%v (create it with -update)", err)
	}
	if got, _ := pdfText(t, sample); got != text {
		t.Fatalf("the committed sample is stale: run go test ./internal/adapters/pdf -run TestGoldenStatement -update")
	}
}

// TestScopesAndPageBreaks: account and exchange statements render; a long table breaks over
// pages with its header repeated; nothing is drawn for a cancelled request.
func TestScopesAndPageBreaks(t *testing.T) {
	text, _ := pdfText(t, render(t, fixture(domain.ScopeExchange, "NYSE")))
	for _, want := range []string{"Exchange statement: NYSE", "Trades on NYSE across the accounts below", "belong to accounts, not exchanges"} {
		if !strings.Contains(text, want) {
			t.Errorf("exchange text lacks %q", want)
		}
	}
	if strings.Contains(text, "First recorded") || strings.Contains(text, "Kill switch used") || strings.Contains(text, "Holdings:") {
		t.Error("an exchange statement shows account-wide figures")
	}

	st := fixture(domain.ScopeAccount, "")
	st.Accounts = st.Accounts[:1]
	for i := range 120 {
		st.OpenPositions = append(st.OpenPositions, domain.StatementPosition{Account: "Demo main", PositionID: int64(1000 + i), Direction: domain.Short})
	}
	text, pages := pdfText(t, render(t, st))
	if pages < 4 || strings.Count(text, "Opened (UTC)") < 3 || !strings.Contains(text, "#1119") || !strings.Contains(text, "Other") {
		t.Fatalf("pages %d, header repeats %d", pages, strings.Count(text, "Opened (UTC)"))
	}
	if strings.Contains(text, "By account") {
		t.Error("an account statement has a per-account breakdown")
	}

	empty := domain.BuildStatement(domain.StatementInput{Window: st.Window, Scope: domain.ScopeCombined, GeneratedAt: st.GeneratedAt})
	text, pages = pdfText(t, render(t, empty))
	if pages != 1 || !strings.Contains(text, "closed no trades on 0 accounts") || !strings.Contains(text, "No accounts.") {
		t.Fatalf("empty: %d pages\n%s", pages, text)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Render(ctx, st); err == nil {
		t.Fatal("rendered for a cancelled request")
	}
}

func TestEncoding(t *testing.T) {
	for in, want := range map[string]string{
		"£1 €2 ¥3 A$4": "\xa31 \x802 \xa53 A$4",
		"a – b — c …":  "a \x96 b \x97 c \x85",
		"x\ny\tz":      "x y z",
		"−5 日本 ✓":      "-5 ?? ?",
		"Müller é ñ":   "M\xfcller \xe9 \xf1",
	} {
		if got := enc(in); got != want {
			t.Errorf("enc(%q) = %q, want %q", in, got, want)
		}
	}
}
