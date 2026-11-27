package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlySetRiskLimitsWritesTheLimits (PLAN R38): the trading budget, the money the agent may
// use, is stored only by SetRiskLimits, the owner's change in a web session, audited.
// Approving a minimums proposal goes through it too; nothing else writes the limits, so no
// code path can raise the budget without the owner.
func TestOnlySetRiskLimitsWritesTheLimits(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	writers := map[string]int{} // "file:func" → writes of the limits
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Set" {
					return true
				}
				for _, a := range call.Args {
					if c, ok := a.(*ast.CallExpr); ok {
						if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "riskLimitsKey" {
							writers[name+":"+fn.Name.Name]++
						}
					}
				}
				return true
			})
		}
	}
	if len(writers) != 1 || writers["risk.go:SetRiskLimits"] != 1 {
		t.Fatalf("the risk limits are written by %v; only SetRiskLimits (once) may", writers)
	}
}
