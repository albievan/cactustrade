// Package storetest is the repository test suite every ports.Store implementation must pass.
// It runs against SQLite in `make test` and against MariaDB in `make test-int`, so both
// databases behave the same (CLAUDE.md §3).
package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Run exercises every repository. The store must be freshly migrated and empty.
func Run(t *testing.T, s ports.Store) {
	t.Helper()
	ctx := context.Background()
	t0 := time.Date(2026, 11, 23, 9, 30, 0, 123456000, time.UTC)

	t.Run("ping", func(t *testing.T) {
		if err := s.Ping(ctx); err != nil {
			t.Fatal(err)
		}
	})

	admin := domain.User{ID: "u-admin", Username: "admin", Role: domain.RoleAdmin, PasswordHash: "h1", CreatedAt: t0, UpdatedAt: t0}
	t.Run("users", func(t *testing.T) {
		u := s.Users()
		if n, err := u.Count(ctx); err != nil || n != 0 {
			t.Fatalf("Count = %d %v", n, err)
		}
		// concurrent first-run setup: exactly one wins
		var wg sync.WaitGroup
		errs := make([]error, 8)
		for i := range errs {
			wg.Go(func() {
				x := admin
				if i > 0 {
					x.ID, x.Username = fmt.Sprintf("u-race-%d", i), fmt.Sprintf("race%d", i)
				}
				errs[i] = u.CreateFirst(ctx, x)
			})
		}
		wg.Wait()
		wins := 0
		for _, err := range errs {
			switch {
			case err == nil:
				wins++
			case !errors.Is(err, domain.ErrSetupDone):
				t.Fatalf("CreateFirst: %v", err)
			}
		}
		if n, _ := u.Count(ctx); wins != 1 || n != 1 {
			t.Fatalf("CreateFirst winners = %d, users = %d", wins, n)
		}
		// make sure the winner is "admin" for the rest of the suite
		all, _ := u.List(ctx)
		if all[0].ID != admin.ID {
			if err := u.Delete(ctx, all[0].ID); err != nil {
				t.Fatal(err)
			}
			if err := u.Create(ctx, admin); err != nil {
				t.Fatal(err)
			}
		}
		got, err := u.Get(ctx, admin.ID)
		if err != nil || got != admin {
			t.Fatalf("Get = %+v %v (want %+v)", got, err, admin)
		}
		if _, err := u.Get(ctx, "nope"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Get missing: %v", err)
		}
		op := domain.User{ID: "u-op", Username: "olivia", Role: domain.RoleOperator, PasswordHash: "h2", CreatedAt: t0, UpdatedAt: t0}
		if err := u.Create(ctx, op); err != nil {
			t.Fatal(err)
		}
		dup := op
		dup.ID = "u-op2"
		if err := u.Create(ctx, dup); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate username: %v", err)
		}
		if got, err := u.GetByUsername(ctx, "olivia"); err != nil || got.ID != "u-op" {
			t.Fatalf("GetByUsername = %+v %v", got, err)
		}
		if _, err := u.GetByUsername(ctx, "ghost"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("GetByUsername missing")
		}
		op.Role, op.Disabled, op.UpdatedAt = domain.RoleAdmin, true, t0.Add(time.Minute)
		if err := u.Update(ctx, op); err != nil {
			t.Fatal(err)
		}
		if got, _ := u.Get(ctx, "u-op"); got != op {
			t.Fatalf("after Update = %+v", got)
		}
		if err := u.Update(ctx, op); err != nil { // same values again: still found
			t.Fatalf("idempotent Update: %v", err)
		}
		if err := u.Update(ctx, domain.User{ID: "nope", Username: "n", Role: domain.RoleViewer, PasswordHash: "x"}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("Update missing")
		}
		clash := op
		clash.Username = "admin"
		if err := u.Update(ctx, clash); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("Update to taken username: %v", err)
		}
		if n, _ := u.CountActiveAdmins(ctx); n != 1 {
			t.Fatalf("CountActiveAdmins = %d (disabled admin must not count)", n)
		}
		list, err := u.List(ctx)
		if err != nil || len(list) != 2 || list[0].Username != "admin" || list[1].Username != "olivia" {
			t.Fatalf("List = %+v %v", list, err)
		}
		if err := u.CreateFirst(ctx, domain.User{ID: "late", Username: "late", Role: domain.RoleAdmin, PasswordHash: "x"}); !errors.Is(err, domain.ErrSetupDone) {
			t.Fatalf("CreateFirst after setup: %v", err)
		}
	})

	t.Run("sessions", func(t *testing.T) {
		r := s.Sessions()
		a := domain.Session{TokenHash: "h-a", UserID: admin.ID, CSRFToken: "c-a", CreatedAt: t0, LastSeen: t0, ExpiresAt: t0.Add(time.Hour)}
		b := domain.Session{TokenHash: "h-b", UserID: admin.ID, CSRFToken: "c-b", CreatedAt: t0, LastSeen: t0, ExpiresAt: t0.Add(3 * time.Hour)}
		for _, x := range []domain.Session{a, b} {
			if err := r.Create(ctx, x); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := r.Get(ctx, "h-a"); err != nil || got != a {
			t.Fatalf("Get = %+v %v", got, err)
		}
		if err := r.Touch(ctx, "h-a", t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if got, _ := r.Get(ctx, "h-a"); !got.LastSeen.Equal(t0.Add(time.Minute)) {
			t.Fatal("Touch")
		}
		if err := r.Touch(ctx, "nope", t0); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("Touch missing")
		}
		if n, err := r.DeleteExpired(ctx, t0.Add(2*time.Hour)); err != nil || n != 1 {
			t.Fatalf("DeleteExpired = %d %v", n, err)
		}
		if _, err := r.Get(ctx, "h-a"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("expired session kept")
		}
		if err := r.Delete(ctx, "h-b"); err != nil {
			t.Fatal(err)
		}
		if err := r.Delete(ctx, "h-b"); err != nil {
			t.Fatal("Delete must be idempotent")
		}
		_ = r.Create(ctx, a)
		if err := r.DeleteForUser(ctx, admin.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Get(ctx, "h-a"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("DeleteForUser")
		}
	})

	t.Run("api_keys", func(t *testing.T) {
		r := s.APIKeys()
		exp := t0.Add(24 * time.Hour)
		k1 := domain.APIKey{ID: "k1", OwnerID: admin.ID, Name: "ci", Role: domain.RoleViewer, SecretHash: "s1", CreatedAt: t0, ExpiresAt: &exp}
		k2 := domain.APIKey{ID: "k2", OwnerID: "u-op", Name: "bot", Role: domain.RoleOperator, SecretHash: "s2", CreatedAt: t0.Add(time.Second)}
		for _, k := range []domain.APIKey{k1, k2} {
			if err := r.Create(ctx, k); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.Create(ctx, k1); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate key: %v", err)
		}
		got, err := r.Get(ctx, "k1")
		if err != nil || got.ID != "k1" || got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) || got.LastUsedAt != nil {
			t.Fatalf("Get = %+v %v", got, err)
		}
		if err := r.Touch(ctx, "k1", t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if got, _ := r.Get(ctx, "k1"); got.LastUsedAt == nil || !got.LastUsedAt.Equal(t0.Add(time.Minute)) {
			t.Fatal("Touch")
		}
		if all, _ := r.List(ctx, ""); len(all) != 2 || all[0].ID != "k1" {
			t.Fatalf("List all = %+v", all)
		}
		if mine, _ := r.List(ctx, "u-op"); len(mine) != 1 || mine[0].ID != "k2" {
			t.Fatalf("List mine = %+v", mine)
		}
		if err := r.Delete(ctx, "k1"); err != nil {
			t.Fatal(err)
		}
		if err := r.Delete(ctx, "k1"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("Delete missing")
		}
		if err := r.DeleteForOwner(ctx, "u-op"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Get(ctx, "k2"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("DeleteForOwner")
		}
		if err := r.Touch(ctx, "k2", t0); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("Touch missing")
		}
	})

	t.Run("secrets", func(t *testing.T) {
		r := s.Secrets()
		blob := []byte{0, 1, 2, 255, 254, 'x'}
		if err := r.Put(ctx, ports.SecretRecord{Name: "etoro_demo_api_key", Ciphertext: blob, UpdatedAt: t0}); err != nil {
			t.Fatal(err)
		}
		got, err := r.Get(ctx, "etoro_demo_api_key")
		if err != nil || string(got.Ciphertext) != string(blob) || !got.UpdatedAt.Equal(t0) {
			t.Fatalf("Get = %+v %v", got, err)
		}
		blob2 := []byte("second")
		if err := r.Put(ctx, ports.SecretRecord{Name: "etoro_demo_api_key", Ciphertext: blob2, UpdatedAt: t0.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if got, _ := r.Get(ctx, "etoro_demo_api_key"); string(got.Ciphertext) != "second" {
			t.Fatal("Put must replace")
		}
		_ = r.Put(ctx, ports.SecretRecord{Name: "llm_api_key", Ciphertext: blob, UpdatedAt: t0})
		list, err := r.List(ctx)
		if err != nil || len(list) != 2 || list[0].Ciphertext != nil {
			t.Fatalf("List = %+v %v (ciphertext must be omitted)", list, err)
		}
		if err := r.Delete(ctx, "llm_api_key"); err != nil {
			t.Fatal(err)
		}
		if err := r.Delete(ctx, "llm_api_key"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("Delete missing")
		}
		if _, err := r.Get(ctx, "llm_api_key"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("Get missing")
		}
	})

	t.Run("audit", func(t *testing.T) {
		r := s.Audit()
		for i := range 3 {
			e := domain.AuditEntry{At: t0.Add(time.Duration(i) * time.Second), ActorID: admin.ID, Actor: "admin", Action: fmt.Sprintf("a%d", i), Target: "t", Details: "d", RemoteIP: "10.0.0.1"}
			if err := r.Append(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		es, err := r.List(ctx, 2)
		if err != nil || len(es) != 2 || es[0].Action != "a2" || es[1].Action != "a1" || es[0].ID <= es[1].ID || es[0].RemoteIP != "10.0.0.1" {
			t.Fatalf("List = %+v %v", es, err)
		}
	})

	t.Run("settings", func(t *testing.T) {
		r := s.Settings()
		if _, err := r.Get(ctx, "trading.mode"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("Get missing")
		}
		for _, v := range []string{"demo", "demo", "real"} { // same value twice must not fail
			if err := r.Set(ctx, "trading.mode", v, t0); err != nil {
				t.Fatal(err)
			}
		}
		if v, _ := r.Get(ctx, "trading.mode"); v != "real" {
			t.Fatalf("Get = %q", v)
		}
	})

	t.Run("cascade", func(t *testing.T) {
		_ = s.Sessions().Create(ctx, domain.Session{TokenHash: "h-c", UserID: "u-op", CSRFToken: "c", CreatedAt: t0, LastSeen: t0, ExpiresAt: t0.Add(time.Hour)})
		_ = s.APIKeys().Create(ctx, domain.APIKey{ID: "k3", OwnerID: "u-op", Name: "x", Role: domain.RoleViewer, SecretHash: "s", CreatedAt: t0})
		if err := s.Users().Delete(ctx, "u-op"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Sessions().Get(ctx, "h-c"); !errors.Is(err, domain.ErrNotFound) {
			t.Error("session survived its user")
		}
		if _, err := s.APIKeys().Get(ctx, "k3"); !errors.Is(err, domain.ErrNotFound) {
			t.Error("API key survived its user")
		}
		if err := s.Users().Delete(ctx, "u-op"); !errors.Is(err, domain.ErrNotFound) {
			t.Error("Delete missing")
		}
	})
}
