package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// TestEnsureMetricsToken (PLAN 7.1.14): without CACTUSTRADE_METRICS_TOKEN a token is
// generated once into the data folder (0600) and reused; an empty file is refused.
func TestEnsureMetricsToken(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, metricsTokenFile)
	tok, created, err := ensureMetricsToken(f)
	if err != nil || !created || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(tok) {
		t.Fatalf("first = %d chars, created %v, %v", len(tok), created, err)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(f); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v %v", st.Mode(), err)
		}
	}
	again, created, err := ensureMetricsToken(f)
	if err != nil || created || again != tok {
		t.Fatalf("second: created %v, same %v, %v", created, again == tok, err)
	}
	other, _, err := ensureMetricsToken(filepath.Join(t.TempDir(), metricsTokenFile))
	if err != nil || other == tok {
		t.Fatalf("a second data folder got the same token (%v)", err)
	}
	if err := os.WriteFile(f, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureMetricsToken(f); err == nil {
		t.Fatal("an empty token file was accepted")
	}
	if _, _, err := ensureMetricsToken(filepath.Join(dir, "missing", metricsTokenFile)); err == nil {
		t.Fatal("an unwritable path was accepted")
	}
}
