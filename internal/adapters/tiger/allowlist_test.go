package tiger

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestAllowlistIsReadOnly: exactly the three read methods, the fixed gateway.
func TestAllowlistIsReadOnly(t *testing.T) {
	var got []string
	for m, every := range Allowlist {
		got = append(got, string(m))
		if every <= 0 {
			t.Errorf("%s: no rate limit", m)
		}
	}
	slices.Sort(got)
	if strings.Join(got, ",") != "assets,positions,prime_assets" {
		t.Fatalf("the allowlist is %v; adding a method needs a reviewed reason (docs/tiger-api.md)", got)
	}
	if GatewayURL != "https://openapi.tigerfintech.com/gateway" {
		t.Fatal("Tiger gateway changed")
	}
}

// Fragments no string literal of the client may contain: orders and money movement.
var forbidden = []string{"order", "transfer", "withdraw", "deposit", "fund", "cancel", "modify", "place", "play-analytics", "license", "token"}

// snake-case literals that may appear: the envelope's and the allowlisted methods' names, and
// the log's duration key.
var allowedSnake = map[string]bool{"tiger_id": true, "sign_type": true, "biz_content": true, "prime_assets": true, "duration_ms": true}

var snake = regexp.MustCompile(`^[a-z]+(_[a-z]+)+$`)

// TestNoMethodOutsideAllowlist parses every non-test Go file of this package and fails on a
// literal naming an order or money method, on any snake_case literal that is not an envelope
// field or an allowlisted method, on any URL but the gateway, on a Method constant outside
// the allowlist, on a call() whose method is not an allowlisted constant, and on any HTTP
// method but the one POST of attempt().
func TestNoMethodOutsideAllowlist(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	posts, urls := 0, 0
	constOK := map[string]bool{"MethodPrimeAssets": true, "MethodAssets": true, "MethodPositions": true}
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
			if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
				continue // import paths are not Tiger literals
			}
			fn := ""
			if fd, ok := decl.(*ast.FuncDecl); ok {
				fn = fd.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.ValueSpec:
					if id, ok := x.Type.(*ast.Ident); ok && id.Name == "Method" {
						for i, name := range x.Names {
							lit, _ := x.Values[i].(*ast.BasicLit)
							s := ""
							if lit != nil {
								s, _ = strconv.Unquote(lit.Value)
							}
							if _, ok := Allowlist[Method(s)]; !ok || !constOK[name.Name] {
								t.Errorf("%s: Method constant %s = %q is not allowlisted", fset.Position(name.Pos()), name.Name, s)
							}
						}
					}
				case *ast.CallExpr:
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "call" && len(x.Args) >= 2 {
						if id, ok := x.Args[1].(*ast.Ident); !ok || !constOK[id.Name] {
							t.Errorf("%s: call() with a method that is not an allowlisted constant", fset.Position(x.Pos()))
						}
					}
				case *ast.SelectorExpr:
					switch x.Sel.Name {
					case "MethodPost":
						posts++
						if fn != "attempt" {
							t.Errorf("%s: POST outside attempt()", fset.Position(x.Pos()))
						}
					case "MethodGet", "MethodPut", "MethodPatch", "MethodDelete", "Post", "PostForm", "Get":
						if id, ok := x.X.(*ast.Ident); ok && id.Name == "http" {
							t.Errorf("%s: http.%s — the Tiger client sends one signed POST only", fset.Position(x.Pos()), x.Sel.Name)
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
					low := strings.ToLower(s)
					for _, f := range forbidden {
						if strings.Contains(low, f) {
							t.Errorf("%s: literal %q contains %q", fset.Position(x.Pos()), s, f)
						}
					}
					switch strings.ToUpper(s) {
					case "GET", "POST", "PUT", "PATCH", "DELETE":
						t.Errorf("%s: HTTP method %q as a literal", fset.Position(x.Pos()), s)
					}
					if snake.MatchString(s) && !allowedSnake[s] {
						t.Errorf("%s: %q looks like a Tiger method or field that is not allowed", fset.Position(x.Pos()), s)
					}
					if strings.Contains(s, "://") || strings.Contains(low, "tigerfintech") || strings.Contains(low, "itiger") || strings.Contains(low, "/gateway") {
						urls++
						if s != GatewayURL {
							t.Errorf("%s: URL %q is not the gateway", fset.Position(x.Pos()), s)
						}
					}
				}
				return true
			})
		}
	}
	if posts != 1 || urls != 1 {
		t.Fatalf("found %d POSTs and %d URLs, want exactly one of each", posts, urls)
	}
}
