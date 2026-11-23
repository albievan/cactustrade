package etoro

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

// Forbidden path fragments: money movement, balances, cash accounts, sub-accounts, copy
// trading, agent portfolios, SSO, social and the Partners API (CLAUDE.md §2.1).
var forbidden = []string{
	"/money", "/balances", "/transfer", "/deposit", "/withdraw", "/cash", "sub-accounts",
	"/copy", "agent-portfolios", "/sso", "watchlist", "curated-lists", "price-alerts",
	"notifications", "user-info", "pi-data", "/portfolios", "/clubs", "/me", "keydata",
	"/data/", "aggregate-portfolio", "partners", "kyc", "onboarding", "mirror",
}

func allowedPaths() map[string]bool {
	ok := map[string]bool{}
	for _, e := range Allowlist {
		ok[e.Demo], ok[e.Real] = true, true
	}
	return ok
}

// TestAllowlistHasNoMoneyPaths: no allowlisted path touches money, copy trading or social.
func TestAllowlistHasNoMoneyPaths(t *testing.T) {
	for op, e := range Allowlist {
		for _, p := range []string{e.Demo, e.Real} {
			if !strings.HasPrefix(p, "/api/") {
				t.Errorf("%s: %q is not an /api/ path", op, p)
			}
			for _, f := range forbidden {
				if strings.Contains(strings.ToLower(p), f) {
					t.Errorf("%s: %q contains forbidden fragment %q", op, p, f)
				}
			}
		}
		if _, ok := QuotaLimits[e.Quota]; !ok {
			t.Errorf("%s: unknown quota group %q", op, e.Quota)
		}
		switch e.Method {
		case "GET", "POST", "DELETE", "PATCH":
		default:
			t.Errorf("%s: method %q", op, e.Method)
		}
	}
}

// TestAllowlistDemoIsDemo: every account-specific demo path carries "demo", no real path
// does, and the mode switch falls back to demo.
func TestAllowlistDemoIsDemo(t *testing.T) {
	for op, e := range Allowlist {
		account := strings.Contains(e.Real, "/trading/")
		if account && (!strings.Contains(e.Demo, "/demo/") || strings.Contains(e.Real, "demo")) {
			t.Errorf("%s: demo %q / real %q", op, e.Demo, e.Real)
		}
		if !account && e.Demo != e.Real {
			t.Errorf("%s: market-data paths must not differ by mode", op)
		}
		if e.Path(domain.TradingMode("bogus")) != e.Demo || e.Path(domain.ModeDemo) != e.Demo || e.Path(domain.ModeReal) != e.Real {
			t.Errorf("%s: Path mode selection", op)
		}
	}
}

// TestOrderConstants pins the guardrail constants: long only, leverage 1, real shares.
func TestOrderConstants(t *testing.T) {
	if OrderTransaction != "buy" || OrderSettlementType != "real" || OrderTransactionShort != "sellShort" ||
		OrderSettlementTypeShort != "cfd" || OrderLeverage != 1 || OrderAction != "open" || OrderType != "mkt" ||
		OrderCurrency != "usd" || RealAssetSettlementTypeID != 1 || CFDSettlementTypeID != 0 {
		t.Fatal("order guardrail constants changed (CLAUDE.md §2.3)")
	}
	if BaseURL != "https://public-api.etoro.com" || WebSocketURL != "wss://ws.etoro.com/ws" {
		t.Fatal("eToro hosts changed")
	}
	for d, want := range map[domain.Direction][2]string{domain.Long: {"buy", "real"}, domain.Short: {"sellShort", "cfd"}} {
		tr, st, ok := orderShape(d)
		if !ok || tr != want[0] || st != want[1] {
			t.Fatalf("%s → %s %s", d, tr, st)
		}
	}
	if _, _, ok := orderShape("buyToCover"); ok {
		t.Fatal("a third order shape")
	}
}

// TestNoPathOutsideAllowlist parses every non-test Go file of this package and fails on any
// string literal that mentions /api/ but is not exactly an allowlisted path template. A
// client that builds a URL any other way cannot hide a new endpoint from this test.
func TestNoPathOutsideAllowlist(t *testing.T) {
	ok := allowedPaths()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			lit, isLit := n.(*ast.BasicLit)
			if !isLit || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if strings.Contains(s, "/api/") || strings.Contains(s, "api/v") {
				seen++
				if !ok[s] {
					t.Errorf("%s: eToro path %q is not in the allowlist (CLAUDE.md §2.1)", fset.Position(lit.Pos()), s)
				}
			}
			for _, fr := range []string{"money", "transfer", "deposit", "withdraw", "sub-accounts", "copy"} {
				if strings.Contains(strings.ToLower(s), "/"+fr) {
					t.Errorf("%s: forbidden fragment in %q", fset.Position(lit.Pos()), s)
				}
			}
			return true
		})
	}
	if seen < len(ok) {
		t.Fatalf("scanned only %d path literals; the allowlist itself has %d", seen, len(ok))
	}
}
