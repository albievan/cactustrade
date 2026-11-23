package saxo

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

// TestAllowlistIsReadOnly: exactly two portfolio GETs, never a trading path, fixed hosts.
func TestAllowlistIsReadOnly(t *testing.T) {
	if len(Allowlist) != 2 {
		t.Fatalf("the allowlist has %d endpoints; adding one needs a reviewed reason (docs/saxo-api.md)", len(Allowlist))
	}
	for op, e := range Allowlist {
		if !strings.HasPrefix(e.Path, "/port/v1/") || strings.Contains(strings.ToLower(e.Path), "/trade/") {
			t.Errorf("%s: %q is not a /port/v1/ read", op, e.Path)
		}
		if e.Every <= 0 {
			t.Errorf("%s: no rate limit", op)
		}
	}
	if SimBaseURL != "https://gateway.saxobank.com/sim/openapi" || LiveBaseURL != "https://gateway.saxobank.com/openapi" ||
		SimAuthURL != "https://sim.logonvalidation.net" || LiveAuthURL != "https://live.logonvalidation.net" ||
		AuthorizePath != "/authorize" || TokenPath != "/token" {
		t.Fatal("Saxo hosts changed")
	}
}

// TestNoPathOutsideAllowlist parses every non-test Go file of this package and fails on any
// path or URL literal that is not an allowlisted one, on any /trade/ literal, on any mutating
// HTTP method but the one POST in tokenRequest (the sign-in host's /token), and on a
// literal HTTP method name.
func TestNoPathOutsideAllowlist(t *testing.T) {
	ok := map[string]bool{
		SimBaseURL: true, LiveBaseURL: true, SimAuthURL: true, LiveAuthURL: true, AuthorizePath: true, TokenPath: true,
	}
	for _, e := range Allowlist {
		ok[e.Path] = true
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen, posts := 0, 0
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
		for _, decl := range af.Decls {
			fn := ""
			if fd, isFn := decl.(*ast.FuncDecl); isFn {
				fn = fd.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					switch x.Sel.Name {
					case "MethodPost":
						posts++
						if fn != "tokenRequest" {
							t.Errorf("%s: POST outside tokenRequest — the Saxo client only reads", fset.Position(x.Pos()))
						}
					case "MethodPut", "MethodPatch", "MethodDelete", "Post", "PostForm":
						t.Errorf("%s: %s — the Saxo client only reads", fset.Position(x.Pos()), x.Sel.Name)
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
						t.Errorf("%s: HTTP method %q as a literal", fset.Position(x.Pos()), s)
					}
					low := strings.ToLower(s)
					if strings.Contains(low, "/trade/") || strings.Contains(low, "/trade") && strings.HasPrefix(low, "/") {
						t.Errorf("%s: trading path %q", fset.Position(x.Pos()), s)
					}
					if (strings.HasPrefix(s, "/") && len(s) > 1) || strings.Contains(s, "://") || strings.Contains(low, "/v1/") ||
						strings.Contains(low, "openapi") || strings.Contains(low, "logonvalidation") {
						seen++
						if !ok[s] {
							t.Errorf("%s: Saxo path %q is not in the allowlist", fset.Position(x.Pos()), s)
						}
					}
				}
				return true
			})
		}
	}
	if posts != 1 {
		t.Fatalf("found %d POSTs, want exactly the one token request", posts)
	}
	if seen < len(ok) {
		t.Fatalf("scanned only %d path literals; the allowlist itself has %d", seen, len(ok))
	}
}
