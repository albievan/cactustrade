package trading212

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Fragments no allowlisted path may contain: orders (placing, cancelling), pies, exports,
// and anything touching money movement.
var forbidden = []string{
	"/equity/orders", "/pies", "/export", "/transfer", "/deposit", "/withdraw", "/cash", "/money",
}

// TestAllowlistIsReadOnly: every path is a v0 equity read, none is an order or pie path.
func TestAllowlistIsReadOnly(t *testing.T) {
	if len(Allowlist) != 3 {
		t.Fatalf("the allowlist has %d endpoints; adding one needs a reviewed reason (docs/trading212-api.md)", len(Allowlist))
	}
	for op, e := range Allowlist {
		if !strings.HasPrefix(e.Path, "/api/v0/equity/") {
			t.Errorf("%s: %q is not an /api/v0/equity/ path", op, e.Path)
		}
		for _, f := range forbidden {
			if strings.Contains(strings.ToLower(e.Path), f) {
				t.Errorf("%s: %q contains forbidden fragment %q", op, e.Path, f)
			}
		}
		if e.Every <= 0 {
			t.Errorf("%s: no rate limit", op)
		}
	}
	if DemoBaseURL != "https://demo.trading212.com" || LiveBaseURL != "https://live.trading212.com" {
		t.Fatal("Trading 212 hosts changed")
	}
}

// TestNoPathOutsideAllowlist parses every non-test Go file of this package and fails on any
// string literal that mentions /api/ or /equity/ but is not exactly an allowlisted path, and
// on any HTTP method but GET (as a literal or a net/http constant): the client is read-only.
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
			switch x := n.(type) {
			case *ast.SelectorExpr:
				switch x.Sel.Name {
				case "MethodPost", "MethodPut", "MethodPatch", "MethodDelete", "Post", "PostForm":
					t.Errorf("%s: %s — the Trading 212 client only reads", fset.Position(x.Pos()), x.Sel.Name)
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
				case "POST", "PUT", "PATCH", "DELETE":
					t.Errorf("%s: HTTP method %q — the Trading 212 client only reads", fset.Position(x.Pos()), s)
				}
				if strings.Contains(s, "/api/") || strings.Contains(s, "/equity/") || strings.Contains(s, "api/v") {
					seen++
					if !ok[s] {
						t.Errorf("%s: Trading 212 path %q is not in the allowlist", fset.Position(x.Pos()), s)
					}
				}
			}
			return true
		})
	}
	if seen < len(ok) {
		t.Fatalf("scanned only %d path literals; the allowlist itself has %d", seen, len(ok))
	}
}
