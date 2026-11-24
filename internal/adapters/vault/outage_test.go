package vault

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

// forgetValues drops the values the store last read (as after a restart).
func forgetValues(s *Store) {
	s.mu.Lock()
	clear(s.last)
	s.mu.Unlock()
}

// TestOutageServesLastValue: while Vault is down or sealed, a value this process read or
// wrote before is still served (exits keep their eToro keys, CLAUDE.md §2.6), with one
// warning naming the credential only; a value never read fails; a deleted one is forgotten;
// a value Vault says is gone is forgotten too.
func TestOutageServesLastValue(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, nil)
	_ = e.s.Put(ctx, "written", value)
	_ = e.s.Put(ctx, "read", "v-read")
	_ = e.s.Put(ctx, "deleted", "v-deleted")
	_ = e.s.Put(ctx, "gone", "v-gone")
	forgetValues(e.s)
	for _, n := range []domain.CredentialName{"read", "deleted", "gone"} {
		if _, err := e.s.Get(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	_ = e.s.Put(ctx, "written", value) // a write is remembered too
	if err := e.s.Delete(ctx, "deleted"); err != nil {
		t.Fatal(err)
	}
	e.fv.mu.Lock()
	delete(e.fv.kv, "gone") // deleted by someone else
	e.fv.mu.Unlock()
	if _, err := e.s.Get(ctx, "gone"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("gone = %v", err)
	}
	e.fv.override = func(r *http.Request) (int, string, bool) { return 503, `{"errors":["Vault is sealed"]}`, true }
	for range 3 {
		if v, err := e.s.Get(ctx, "written"); err != nil || v != value {
			t.Fatalf("written during the outage = %q %v", v, err)
		}
		if v, err := e.s.Get(ctx, "read"); err != nil || v != "v-read" {
			t.Fatalf("read during the outage = %q %v", v, err)
		}
	}
	for _, n := range []domain.CredentialName{"deleted", "gone", "never-read"} {
		if _, err := e.s.Get(ctx, n); !errors.Is(err, domain.ErrUnavailable) {
			t.Fatalf("%s during the outage = %v", n, err)
		}
	}
	if err := e.s.Put(ctx, "written", "new"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("a write during the outage = %v", err)
	}
	if v, _ := e.s.Get(ctx, "written"); v != value {
		t.Fatal("a failed write replaced the remembered value")
	}
	if n := strings.Count(e.logs.String(), "using the credentials last read"); n != 1 {
		t.Fatalf("%d outage warnings, want 1", n)
	}
	// Vault back: fresh reads, and the next outage warns again
	e.fv.override = nil
	if v, err := e.s.Get(ctx, "read"); err != nil || v != "v-read" {
		t.Fatal(err)
	}
	e.fv.override = func(r *http.Request) (int, string, bool) { return 503, ``, true }
	_, _ = e.s.Get(ctx, "read")
	if n := strings.Count(e.logs.String(), "using the credentials last read"); n != 2 {
		t.Fatalf("%d outage warnings, want 2", n)
	}
	noSecrets(t, "the log", e.logs.String())
	if strings.Contains(e.logs.String(), "v-read") {
		t.Fatal("a value in the log")
	}
}
