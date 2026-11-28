package trading212

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestAllowlistExact pins the allowlist: exactly these 9 requests (method + path), the three
// changes marked as such, every one rate limited (CLAUDE.md §2.1, D41, D43). Adding one needs
// the owner's consent and a reviewed reason (docs/trading212-api.md).
func TestAllowlistExact(t *testing.T) {
	want := map[Op]string{
		OpAccountSummary: "GET /api/v0/equity/account/summary",
		OpPositions:      "GET /api/v0/equity/positions",
		OpOrderHistory:   "GET /api/v0/equity/history/orders",
		OpInstruments:    "GET /api/v0/equity/metadata/instruments",
		OpOrders:         "GET /api/v0/equity/orders",
		OpOrder:          "GET /api/v0/equity/orders/{id}",
		OpMarketOrder:    "POST /api/v0/equity/orders/market",
		OpStopOrder:      "POST /api/v0/equity/orders/stop",
		OpCancelOrder:    "DELETE /api/v0/equity/orders/{id}",
	}
	if len(Allowlist) != len(want) {
		t.Fatalf("the allowlist has %d endpoints, want exactly %d", len(Allowlist), len(want))
	}
	mutating := []Op{OpMarketOrder, OpStopOrder, OpCancelOrder}
	for op, e := range Allowlist {
		if got := e.Method + " " + e.Path; got != want[op] {
			t.Errorf("%s: %q, want %q", op, got, want[op])
		}
		if e.Every <= 0 {
			t.Errorf("%s: no rate limit", op)
		}
		if e.Mutates != slices.Contains(mutating, op) {
			t.Errorf("%s: Mutates = %v", op, e.Mutates)
		}
		if e.Method != http.MethodGet && !e.Mutates {
			t.Errorf("%s: a %s that is not marked as a change", op, e.Method)
		}
		for _, f := range []string{"/limit", "/stop_limit", "/pies", "/export", "/exchanges", "/transfer", "/deposit", "/withdraw", "/cash", "/money", "/transactions", "/dividends"} {
			if strings.Contains(strings.ToLower(e.Path), f) {
				t.Errorf("%s: %q contains forbidden fragment %q", op, e.Path, f)
			}
		}
	}
	rates := map[Op]time.Duration{
		OpAccountSummary: 5 * time.Second, OpPositions: time.Second, OpOrderHistory: 3 * time.Second, OpInstruments: 50 * time.Second,
		OpOrders: 5 * time.Second, OpOrder: time.Second, OpMarketOrder: 1200 * time.Millisecond, OpStopOrder: 2 * time.Second, OpCancelOrder: 1200 * time.Millisecond,
	}
	for op, d := range rates {
		if Allowlist[op].Every != d {
			t.Errorf("%s: every %s, want %s", op, Allowlist[op].Every, d)
		}
	}
	if DemoBaseURL != "https://demo.trading212.com" || LiveBaseURL != "https://live.trading212.com" {
		t.Fatal("Trading 212 hosts changed")
	}
	if extendedHours || stopValidity != "GOOD_TILL_CANCEL" {
		t.Fatal("the order shape constants changed")
	}
}

// TestNoPathOutsideAllowlist parses every non-test Go file of this package and fails on any
// string literal that mentions /api/ or /equity/ but is not exactly an allowlisted path, on
// any HTTP method (a net/http constant or a literal) outside allowlist.go, and on PUT or
// PATCH anywhere.
func TestNoPathOutsideAllowlist(t *testing.T) {
	ok := map[string]bool{}
	for _, e := range Allowlist {
		ok[e.Path] = true
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen := map[string]bool{}
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
		inAllowlist := f == "allowlist.go"
		ast.Inspect(af, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				switch x.Sel.Name {
				case "MethodPut", "MethodPatch":
					t.Errorf("%s: %s — never", fset.Position(x.Pos()), x.Sel.Name)
				case "MethodGet", "MethodPost", "MethodDelete", "MethodHead", "MethodOptions", "MethodConnect", "MethodTrace":
					if !inAllowlist {
						t.Errorf("%s: %s outside allowlist.go", fset.Position(x.Pos()), x.Sel.Name)
					}
				case "Post", "PostForm", "Get", "Head":
					if id, isID := x.X.(*ast.Ident); isID && id.Name == "http" {
						t.Errorf("%s: http.%s — requests go through the allowlist", fset.Position(x.Pos()), x.Sel.Name)
					}
				}
			case *ast.BasicLit:
				if x.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(x.Value)
				if err != nil {
					return true
				}
				switch strings.ToUpper(s) {
				case "PUT", "PATCH":
					t.Errorf("%s: HTTP method %q — never", fset.Position(x.Pos()), s)
				case "GET", "POST", "DELETE":
					if !inAllowlist {
						t.Errorf("%s: HTTP method %q outside allowlist.go", fset.Position(x.Pos()), s)
					}
				}
				if strings.Contains(s, "/api/") || strings.Contains(s, "/equity/") || strings.Contains(s, "api/v") {
					seen[s] = true
					if !ok[s] {
						t.Errorf("%s: Trading 212 path %q is not in the allowlist", fset.Position(x.Pos()), s)
					}
				}
			}
			return true
		})
	}
	if len(seen) < len(ok) {
		t.Fatalf("scanned only %d path literals (%v); the allowlist itself has %d", len(seen), slices.Sorted(maps.Keys(seen)), len(ok))
	}
}
