package logctx

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

func capture() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))), &buf
}

func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func TestHandlerAddsAccountAndMode(t *testing.T) {
	demo := domain.Account{ID: "acc-demo", Name: "Demo", Mode: domain.ModeDemo}
	real := domain.Account{ID: "acc-real", Name: "Real", Mode: domain.ModeReal}
	noted := ports.WithAccountNote(context.Background())
	ports.NoteAccount(noted, real)
	ports.NoteAccount(noted, demo) // the first account noted wins
	tests := []struct {
		name        string
		ctx         context.Context
		args        []any
		wantAccount string
		wantMode    string
	}{
		{"demo account", ports.WithAccount(context.Background(), demo), nil, "acc-demo", "demo"},
		{"real account", ports.WithAccount(context.Background(), real), nil, "acc-real", "real"},
		{"noted by a request", noted, nil, "acc-real", "real"},
		{"WithAccount wins over the note", ports.WithAccount(noted, demo), nil, "acc-demo", "demo"},
		{"note without an account", ports.WithAccountNote(context.Background()), nil, "", ""},
		{"no account", context.Background(), nil, "", ""},
		{"an attribute set by the call is kept", ports.WithAccount(context.Background(), demo), []any{"mode", "x"}, "acc-demo", "x"},
		{"no mode (view-only platform)", ports.WithAccount(context.Background(), domain.Account{ID: "t212"}), nil, "t212", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			log, buf := capture()
			log.InfoContext(tc.ctx, "hello", tc.args...)
			ls := lines(t, buf)
			if len(ls) != 1 {
				t.Fatalf("lines = %v", ls)
			}
			if got, _ := ls[0]["account_id"].(string); got != tc.wantAccount {
				t.Errorf("account_id = %q, want %q", got, tc.wantAccount)
			}
			if got, _ := ls[0]["mode"].(string); got != tc.wantMode {
				t.Errorf("mode = %q, want %q", got, tc.wantMode)
			}
			if c := strings.Count(buf.String(), `"mode"`); tc.wantMode != "" && c != 1 {
				t.Errorf("mode appears %d times: %s", c, buf)
			}
		})
	}
}

func TestHandlerWithAttrsAndGroupKeepWrapping(t *testing.T) {
	log, buf := capture()
	ctx := ports.WithAccount(context.Background(), domain.Account{ID: "a1", Mode: domain.ModeDemo})
	log.With("svc", "x").InfoContext(ctx, "one")
	log.WithGroup("g").InfoContext(ctx, "two", "k", 1)
	if !log.Enabled(ctx, slog.LevelDebug) {
		t.Error("debug should be enabled")
	}
	ls := lines(t, buf)
	if len(ls) != 2 || ls[0]["account_id"] != "a1" || ls[0]["svc"] != "x" {
		t.Fatalf("lines = %v", ls)
	}
	if g, _ := ls[1]["g"].(map[string]any); g["account_id"] != "a1" || g["mode"] != "demo" {
		t.Fatalf("group line = %v", ls[1])
	}
}

// logMethods are the *slog.Logger (and package slog) calls that take no context.
var logMethods = []string{"Debug", "Info", "Warn", "Error"}

// contextFreeLogCalls lists the log calls in src that take no context: X.Info(msg, …) where X
// is package slog or an expression naming a logger (…Log, …log, …logger).
func contextFreeLogCalls(fset *token.FileSet, name string, src any) ([]string, error) {
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !slices.Contains(logMethods, sel.Sel.Name) {
			return true
		}
		var recv string
		switch x := sel.X.(type) {
		case *ast.Ident:
			recv = x.Name
		case *ast.SelectorExpr:
			recv = x.Sel.Name
		case *ast.CallExpr: // log.With(…).Info(…)
			if s, ok := x.Fun.(*ast.SelectorExpr); ok {
				recv = s.Sel.Name
			}
		}
		l := strings.ToLower(recv)
		if l == "slog" || strings.HasSuffix(l, "log") || strings.HasSuffix(l, "logger") || l == "with" {
			out = append(out, fset.Position(call.Pos()).String())
		}
		return true
	})
	return out, nil
}

// TestNoContextFreeLogCalls: the app and the eToro adapter log only with a context, so every
// line names its account and mode (PLAN 7.2).
func TestNoContextFreeLogCalls(t *testing.T) {
	fset := token.NewFileSet()
	var bad []string
	for _, dir := range []string{"../../app", "../etoro"} {
		ms, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(ms) == 0 {
			t.Fatalf("%s: %v (%d files)", dir, err, len(ms))
		}
		for _, m := range ms {
			if strings.HasSuffix(m, "_test.go") {
				continue
			}
			b, err := os.ReadFile(m)
			if err != nil {
				t.Fatal(err)
			}
			got, err := contextFreeLogCalls(fset, m, b)
			if err != nil {
				t.Fatal(err)
			}
			bad = append(bad, got...)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("log with a context (InfoContext, WarnContext…), not:\n%s", strings.Join(bad, "\n"))
	}
}

// TestContextFreeLogCallsFound: the source check catches planted calls and passes the
// context forms.
func TestContextFreeLogCallsFound(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"s.d.Log.Info", `s.d.Log.Info("x")`, 1},
		{"log.Warn", `log.Warn("x", "k", 1)`, 1},
		{"slog.Error", `slog.Error("x")`, 1},
		{"logger.Debug", `c.logger.Debug("x")`, 1},
		{"With chain", `s.d.Log.With("a", 1).Info("x")`, 1},
		{"context forms", `s.d.Log.InfoContext(ctx, "x"); s.d.Log.Log(ctx, 0, "x")`, 0},
		{"not a logger", `err.Error(); t.Error("x"); v.Info("x")`, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := "package p\nfunc f() {\n" + tc.body + "\n}\n"
			got, err := contextFreeLogCalls(token.NewFileSet(), "p.go", src)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.want {
				t.Fatalf("found %v, want %d", got, tc.want)
			}
		})
	}
}
