// Package storetest is the repository test suite every ports.Store implementation must pass.
// It runs against SQLite in `make test` and against MariaDB in `make test-int`, so both
// databases behave the same (CLAUDE.md §3).
package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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

	admin := domain.User{ID: "u-admin", Username: "admin", Role: domain.RoleAdmin, PasswordHash: "h1", MustChangePassword: true, CreatedAt: t0, UpdatedAt: t0}
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
		op.Role, op.Disabled, op.MustChangePassword, op.UpdatedAt = domain.RoleAdmin, true, true, t0.Add(time.Minute)
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

	t.Run("accounts", func(t *testing.T) {
		ar := s.Accounts()
		bob := domain.User{ID: "u-bob", Username: "bob", Role: domain.RoleOperator, PasswordHash: "h", CreatedAt: t0, UpdatedAt: t0}
		if err := s.Users().Create(ctx, bob); err != nil {
			t.Fatal(err)
		}
		a1 := domain.Account{ID: "acc-1", Name: "Main demo", OwnerID: admin.ID, Mode: domain.ModeDemo, Platform: domain.PlatformEtoro, CreatedAt: t0, UpdatedAt: t0}
		a2 := domain.Account{ID: "acc-2", Name: "Bob ISA", OwnerID: bob.ID, Mode: domain.ModeReal, EnvKeys: false, CreatedAt: t0, UpdatedAt: t0}
		env := domain.Account{ID: domain.EnvAccountID, Name: "Demo (environment)", OwnerID: admin.ID, Mode: domain.ModeDemo, EnvKeys: true, CreatedAt: t0, UpdatedAt: t0}
		for _, a := range []domain.Account{a1, a2, env} {
			if err := ar.Create(ctx, a); err != nil {
				t.Fatal(err)
			}
		}
		if err := ar.Create(ctx, a1); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate: %v", err)
		}
		got, err := ar.Get(ctx, "acc-1")
		a1.OwnerName = "admin"
		if err != nil || got != a1 {
			t.Fatalf("Get = %+v %v", got, err)
		}
		if _, err := ar.Get(ctx, "nope"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("Get missing")
		}
		if all, _ := ar.List(ctx); len(all) != 3 || all[0].Name != "Bob ISA" || !all[1].EnvKeys {
			t.Fatalf("List = %+v", all)
		}
		if err := ar.SetShare(ctx, domain.AccountShare{AccountID: "acc-2", UserID: admin.ID, Access: domain.AccessView, CreatedAt: t0}); err != nil {
			t.Fatal(err)
		}
		if err := ar.SetShare(ctx, domain.AccountShare{AccountID: "acc-2", UserID: admin.ID, Access: domain.AccessTrade, CreatedAt: t0}); err != nil {
			t.Fatal(err) // change, not a duplicate
		}
		views, err := ar.ForUser(ctx, admin.ID)
		if err != nil || len(views) != 3 || views[0].ID != "acc-2" || views[0].Access != domain.AccessTrade || views[1].Access != domain.AccessOwner {
			t.Fatalf("ForUser(admin) = %+v %v", views, err)
		}
		if v, _ := ar.ForUser(ctx, bob.ID); len(v) != 1 || v[0].Access != domain.AccessOwner || v[0].OwnerName != "bob" {
			t.Fatalf("ForUser(bob) = %+v", v)
		}
		sh, err := ar.Shares(ctx, "acc-2")
		if err != nil || len(sh) != 1 || sh[0].Username != "admin" || sh[0].Access != domain.AccessTrade {
			t.Fatalf("Shares = %+v %v", sh, err)
		}
		a2.Name, a2.Mode, a2.UpdatedAt = "Bob real", domain.ModeDemo, t0.Add(time.Minute)
		if err := ar.Update(ctx, a2); err != nil {
			t.Fatal(err)
		}
		if g, _ := ar.Get(ctx, "acc-2"); g.Name != "Bob real" || g.Mode != domain.ModeDemo || !g.UpdatedAt.Equal(a2.UpdatedAt) {
			t.Fatalf("after Update = %+v", g)
		}
		if err := ar.Update(ctx, domain.Account{ID: "nope", Name: "x", Mode: domain.ModeDemo}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("Update missing")
		}
		if n, _ := ar.CountOwnedBy(ctx, admin.ID); n != 2 {
			t.Fatalf("CountOwnedBy = %d", n)
		}
		// a user who owns an account cannot be deleted
		if err := s.Users().Delete(ctx, bob.ID); err == nil {
			t.Fatal("deleted the owner of an account")
		}
		if err := ar.RemoveShare(ctx, "acc-2", admin.ID); err != nil {
			t.Fatal(err)
		}
		if err := ar.RemoveShare(ctx, "acc-2", admin.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("RemoveShare missing")
		}
		_ = ar.SetShare(ctx, domain.AccountShare{AccountID: "acc-2", UserID: admin.ID, Access: domain.AccessView, CreatedAt: t0})
		if err := ar.Delete(ctx, "acc-2"); err != nil {
			t.Fatal(err)
		}
		if sh, _ := ar.Shares(ctx, "acc-2"); len(sh) != 0 {
			t.Fatal("shares survived their account")
		}
		if err := ar.Delete(ctx, "acc-2"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("Delete missing")
		}
		if err := s.Users().Delete(ctx, bob.ID); err != nil {
			t.Fatalf("delete a user without accounts: %v", err)
		}
	})

	t.Run("instruments_watchlist", func(t *testing.T) {
		ir, wl := s.Instruments(), s.Watchlist()
		aapl := domain.Instrument{ID: 1001, Symbol: "AAPL", Name: "Apple", Type: "Stocks", ExchangeID: 4, Market: domain.MarketNYSE, Currency: "USD"}
		aplEl := domain.Eligibility{Instrument: 1001, Symbol: "AAPL", RealLong: true, MinPositionAmount: 1000, CheckedAt: t0}
		barc := domain.Instrument{ID: 2002, Symbol: "BARC.L", Name: "Barclays", Type: "Stocks", Market: domain.MarketLSE}
		barcEl := domain.Eligibility{Instrument: 2002, Symbol: "BARC.L", Reason: "not offered as a real share"}
		if _, _, err := ir.Get(ctx, 1001); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("Get missing instrument")
		}
		if err := wl.Add(ctx, "acc-1", 1001, "admin", t0); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Add of an uncached instrument: %v", err)
		}
		for range 2 { // insert, then update with identical values
			if err := ir.Upsert(ctx, aapl, aplEl, t0); err != nil {
				t.Fatal(err)
			}
		}
		if err := ir.Upsert(ctx, barc, barcEl, t0); err != nil {
			t.Fatal(err)
		}
		if err := ir.Upsert(ctx, aapl, barcEl, t0); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("mismatched eligibility accepted")
		}
		in, el, err := ir.Get(ctx, 1001)
		if err != nil || in != aapl || el != aplEl {
			t.Fatalf("Get = %+v %+v %v", in, el, err)
		}
		if _, el, _ := ir.Get(ctx, 2002); !el.CheckedAt.IsZero() || el.RealLong || el.Reason == "" {
			t.Fatalf("never-checked eligibility = %+v", el)
		}
		aplEl.RealLong, aplEl.Reason = false, "suspended"
		if err := ir.Upsert(ctx, aapl, aplEl, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, el, _ := ir.Get(ctx, 1001); el.RealLong || el.Reason != "suspended" {
			t.Fatalf("update = %+v", el)
		}
		if err := wl.Add(ctx, "acc-1", 2002, "admin", t0); err != nil {
			t.Fatal(err)
		}
		if err := wl.Add(ctx, "no-such-account", 2002, "admin", t0); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown account: %v", err)
		}
		if err := wl.Add(ctx, domain.EnvAccountID, 2002, "admin", t0); err != nil {
			t.Fatal(err)
		}
		if err := wl.Add(ctx, "acc-1", 1001, "olivia", t0.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := wl.Add(ctx, "acc-1", 1001, "olivia", t0); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate Add: %v", err)
		}
		if ids, err := wl.Watched(ctx); err != nil || len(ids) != 2 || ids[0] != 1001 || ids[1] != 2002 {
			t.Fatalf("Watched = %v %v", ids, err)
		}
		if other, _ := wl.List(ctx, domain.EnvAccountID); len(other) != 1 {
			t.Fatalf("the other account's list = %+v", other)
		}
		items, err := wl.List(ctx, "acc-1")
		if err != nil || len(items) != 2 || items[0].Instrument.Symbol != "AAPL" || items[1].Instrument != barc ||
			items[0].AddedBy != "olivia" || !items[0].AddedAt.Equal(t0.Add(time.Second)) || items[0].Eligibility.Reason != "suspended" {
			t.Fatalf("List = %+v %v", items, err)
		}
		if err := wl.Remove(ctx, "acc-1", 2002); err != nil {
			t.Fatal(err)
		}
		if err := wl.Remove(ctx, "acc-1", 2002); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("Remove missing")
		}
		if items, _ := wl.List(ctx, "acc-1"); len(items) != 1 {
			t.Fatalf("after Remove = %+v", items)
		}
	})

	t.Run("events", func(t *testing.T) {
		ev := s.Events()
		if n, err := ev.CountSince(ctx, nil, time.Time{}); err != nil || n != 0 {
			t.Fatalf("CountSince on empty = %d %v", n, err)
		}
		long := strings.Repeat("é", 2000) // 4000 bytes: clipped on a rune boundary
		for i, e := range []domain.Event{
			{ID: "e1", At: t0, Kind: "watchlist.added", Severity: domain.SeverityInfo, Title: "AAPL added", Actor: "admin"},
			{ID: "e2", At: t0.Add(time.Minute), Kind: "order.filled", Severity: domain.SeveritySuccess, Title: "Bought", Detail: long},
			{ID: "e3", At: t0.Add(2 * time.Minute), Kind: "killswitch", Severity: domain.SeverityDanger, Title: "Kill", AccountID: "acc-1"},
			{ID: "e4", At: t0.Add(3 * time.Minute), Kind: "order.filled", Severity: domain.SeverityInfo, Title: "Other account", AccountID: "acc-9"},
		} {
			if err := ev.Append(ctx, e); err != nil {
				t.Fatalf("Append %d: %v", i, err)
			}
		}
		if err := ev.Append(ctx, domain.Event{ID: "e1", At: t0, Kind: "x", Severity: domain.SeverityInfo, Title: "dup"}); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate id: %v", err)
		}
		list, err := ev.List(ctx, []string{"acc-1"}, 2)
		if err != nil || len(list) != 2 || list[0].ID != "e3" || list[1].ID != "e2" || list[0].Severity != domain.SeverityDanger || list[0].AccountID != "acc-1" {
			t.Fatalf("List = %+v %v", list, err)
		}
		if !utf8.ValidString(list[1].Detail) || len(list[1].Detail) > 2000 || len(list[1].Detail) < 1990 {
			t.Fatalf("detail not clipped on a rune boundary: %d bytes", len(list[1].Detail))
		}
		if n, _ := ev.CountSince(ctx, []string{"acc-1"}, t0); n != 2 {
			t.Fatalf("CountSince = %d", n)
		}
		if n, _ := ev.CountSince(ctx, nil, t0); n != 1 {
			t.Fatalf("CountSince(system only) = %d", n)
		}
		if n, _ := ev.CountSince(ctx, []string{"acc-1", "acc-9"}, t0); n != 3 {
			t.Fatalf("CountSince = %d", n)
		}
		if n, err := ev.DeleteBefore(ctx, t0.Add(90*time.Second)); err != nil || n != 2 {
			t.Fatalf("DeleteBefore = %d %v", n, err)
		}
		if list, _ := ev.List(ctx, []string{"acc-1", "acc-9"}, 10); len(list) != 2 {
			t.Fatalf("after delete = %+v", list)
		}
	})

	t.Run("themes_reviews", func(t *testing.T) {
		th, rv := s.Themes(), s.Reviews()
		space := domain.Theme{
			ID: "th-1", AccountID: "acc-1", Name: "Space", Keywords: []string{"rocket", "satellite"}, Enabled: true,
			Instruments: []domain.InstrumentID{1001}, LLMProfileID: "llm-1", CreatedAt: t0, UpdatedAt: t0,
		}
		if err := th.Create(ctx, space); err != nil {
			t.Fatal(err)
		}
		if err := th.Create(ctx, space); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate: %v", err)
		}
		bad := space
		bad.ID, bad.Instruments = "th-bad", []domain.InstrumentID{999999}
		if err := th.Create(ctx, bad); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("uncached instrument: %v", err)
		}
		if _, err := th.Get(ctx, "th-bad"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("a failed create left a theme behind")
		}
		semis := domain.Theme{ID: "th-2", AccountID: "acc-1", Name: "Semiconductors", IndustryID: 7, CreatedAt: t0, UpdatedAt: t0}
		if err := th.Create(ctx, semis); err != nil {
			t.Fatal(err)
		}
		got, err := th.Get(ctx, "th-1")
		if err != nil || got.Name != "Space" || got.LLMProfileID != "llm-1" || len(got.Keywords) != 2 || got.Keywords[1] != "satellite" || len(got.Instruments) != 1 || !got.Enabled {
			t.Fatalf("Get = %+v %v", got, err)
		}
		list, err := th.List(ctx, "acc-1")
		if err != nil || len(list) != 2 || list[0].Name != "Semiconductors" || list[0].Keywords != nil || list[1].Instruments[0] != 1001 {
			t.Fatalf("List = %+v %v", list, err)
		}
		space.Enabled, space.Keywords, space.Instruments, space.UpdatedAt, space.LLMProfileID = false, nil, nil, t0.Add(time.Minute), ""
		if err := th.Update(ctx, space); err != nil {
			t.Fatal(err)
		}
		if g, _ := th.Get(ctx, "th-1"); g.Enabled || g.LLMProfileID != "" || len(g.Keywords) != 0 || len(g.Instruments) != 0 {
			t.Fatalf("after Update = %+v", g)
		}
		if err := th.Update(ctx, domain.Theme{ID: "nope", Name: "x"}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("Update missing")
		}
		if err := th.Delete(ctx, "th-2"); err != nil {
			t.Fatal(err)
		}
		if err := th.Delete(ctx, "th-2"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("Delete missing")
		}

		r1 := domain.Review{
			AccountID: "acc-1", Instrument: 1001, Symbol: "AAPL", Theme: "Space", At: t0, Outcome: domain.ReviewNoSignal,
			Probs: map[domain.Regime]float64{domain.RegimeTrending: 0.7}, Checks: []domain.ReviewCheck{{Rule: "enough price history", Pass: true, Detail: "300 bars"}, {Rule: "playbook x", Detail: "no breakout"}},
			Summary: "no signal",
		}
		if err := rv.Upsert(ctx, r1); err != nil {
			t.Fatal(err)
		}
		r1.At, r1.Outcome, r1.Direction = t0.Add(time.Minute), domain.ReviewAdded, domain.Short
		if err := rv.Upsert(ctx, r1); err != nil {
			t.Fatal(err)
		}
		r2 := domain.Review{AccountID: "acc-1", Instrument: 2002, Symbol: "BARC.L", At: t0, Outcome: domain.ReviewError, Summary: "no candles"}
		_ = rv.Upsert(ctx, r2)
		rs, err := rv.List(ctx, "acc-1", 10)
		if err != nil || len(rs) != 2 || rs[0].Instrument != 1001 || rs[0].Outcome != domain.ReviewAdded || rs[0].Direction != domain.Short ||
			rs[0].Probs[domain.RegimeTrending] != 0.7 || len(rs[0].Checks) != 2 || !rs[0].Checks[0].Pass || rs[0].Checks[1].Detail != "no breakout" {
			t.Fatalf("List = %+v %v", rs, err)
		}
		if rs, _ := rv.List(ctx, "acc-1", 1); len(rs) != 1 {
			t.Fatal("limit")
		}
		if n, err := rv.DeleteBefore(ctx, t0.Add(30*time.Second)); err != nil || n != 1 {
			t.Fatalf("DeleteBefore = %d %v", n, err)
		}
		// deleting the account removes its themes and reviews
		if err := s.Accounts().Delete(ctx, "acc-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := th.Get(ctx, "th-1"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("theme survived its account")
		}
		if rs, _ := rv.List(ctx, "acc-1", 10); len(rs) != 0 {
			t.Fatal("reviews survived their account")
		}
	})

	t.Run("user_email", func(t *testing.T) {
		u, err := s.Users().GetByUsername(ctx, admin.Username)
		if err != nil || u.Email != "" {
			t.Fatalf("no e-mail yet = %+v %v", u, err)
		}
		u.Email = "admin@example.com"
		if err := s.Users().Update(ctx, u); err != nil {
			t.Fatal(err)
		}
		if g, _ := s.Users().Get(ctx, u.ID); g.Email != "admin@example.com" {
			t.Fatalf("e-mail = %q", g.Email)
		}
	})

	t.Run("outbox", func(t *testing.T) {
		o := s.Outbox()
		m := func(id string, at time.Time) domain.OutboxMail {
			return domain.OutboxMail{Mail: domain.Mail{ID: id, To: "a@example.com", Subject: "s " + id, Body: "body\nline"}, CreatedAt: at, NextAt: at}
		}
		for i, id := range []string{"m1", "m2", "m3"} {
			if err := o.Enqueue(ctx, m(id, t0.Add(time.Duration(i)*time.Second))); err != nil {
				t.Fatal(err)
			}
		}
		if err := o.Enqueue(ctx, m("m1", t0)); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate: %v", err)
		}
		due, err := o.Due(ctx, t0.Add(time.Second), 10)
		if err != nil || len(due) != 2 || due[0].ID != "m1" || due[0].Body != "body\nline" {
			t.Fatalf("Due = %+v %v", due, err)
		}
		if err := o.MarkSent(ctx, "m1", t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		long := strings.Repeat("e", 700)
		if err := o.MarkFailed(ctx, "m2", 1, t0.Add(time.Hour), long); err != nil {
			t.Fatal(err)
		}
		if err := o.MarkFailed(ctx, "m1", 1, t0, "late failure"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("failure recorded on sent mail: %v", err)
		}
		if err := o.MarkSent(ctx, "nope", t0); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("MarkSent unknown: %v", err)
		}
		due, _ = o.Due(ctx, t0.Add(time.Minute), 10)
		if len(due) != 1 || due[0].ID != "m3" {
			t.Fatalf("Due after send/fail = %+v", due)
		}
		_ = o.MarkFailed(ctx, "m3", domain.MaxMailAttempts, t0, "gave up")
		if due, _ := o.Due(ctx, t0.Add(48*time.Hour), 10); len(due) != 1 || due[0].ID != "m2" || due[0].Attempts != 1 || len(due[0].LastError) != 500 {
			t.Fatalf("Due later = %+v", due)
		}
		all, err := o.List(ctx, 10)
		if err != nil || len(all) != 3 || all[0].ID != "m3" || all[2].SentAt.IsZero() || all[2].Attempts != 1 {
			t.Fatalf("List = %+v %v", all, err)
		}
		if n, err := o.DeleteBefore(ctx, t0.Add(1500*time.Millisecond)); err != nil || n != 2 {
			t.Fatalf("DeleteBefore = %d %v", n, err)
		}
	})

	t.Run("password_resets", func(t *testing.T) {
		pr := s.PasswordResets()
		r := domain.PasswordReset{TokenHash: strings.Repeat("a", 64), UserID: admin.ID, CreatedAt: t0, ExpiresAt: t0.Add(30 * time.Minute)}
		if err := pr.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := pr.Create(ctx, r); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate: %v", err)
		}
		if err := pr.Create(ctx, domain.PasswordReset{TokenHash: strings.Repeat("b", 64), UserID: admin.ID, CreatedAt: t0.Add(time.Minute), ExpiresAt: t0.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		g, err := pr.Get(ctx, r.TokenHash)
		if err != nil || g.UserID != admin.ID || !g.ExpiresAt.Equal(r.ExpiresAt) || !g.UsedAt.IsZero() {
			t.Fatalf("Get = %+v %v", g, err)
		}
		if _, err := pr.Get(ctx, "nope"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("Get unknown")
		}
		if n, err := pr.CountSince(ctx, admin.ID, t0.Add(30*time.Second)); err != nil || n != 1 {
			t.Fatalf("CountSince = %d %v", n, err)
		}
		if err := pr.Use(ctx, r.TokenHash, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := pr.Use(ctx, r.TokenHash, t0.Add(time.Minute)); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("used twice: %v", err)
		}
		if g, _ := pr.Get(ctx, r.TokenHash); g.UsedAt.IsZero() {
			t.Fatal("UsedAt not stored")
		}
		if n, err := pr.DeleteBefore(ctx, t0.Add(45*time.Minute)); err != nil || n != 1 {
			t.Fatalf("DeleteBefore = %d %v", n, err)
		}
		if err := pr.DeleteForUser(ctx, admin.ID); err != nil {
			t.Fatal(err)
		}
		if n, _ := pr.CountSince(ctx, admin.ID, t0); n != 0 {
			t.Fatal("DeleteForUser left links")
		}
	})

	t.Run("platform_equity_snapshots", func(t *testing.T) {
		ar, es := s.Accounts(), s.EquitySnapshots()
		t212 := domain.Account{ID: "acc-t212", Name: "ISA (Trading 212)", OwnerID: admin.ID, Mode: domain.ModeReal, Platform: domain.PlatformTrading212, CreatedAt: t0, UpdatedAt: t0}
		plain := domain.Account{ID: "acc-plain", Name: "No platform given", OwnerID: admin.ID, Mode: domain.ModeDemo, CreatedAt: t0, UpdatedAt: t0}
		for _, a := range []domain.Account{t212, plain} {
			if err := ar.Create(ctx, a); err != nil {
				t.Fatal(err)
			}
		}
		if g, err := ar.Get(ctx, t212.ID); err != nil || g.Platform != domain.PlatformTrading212 {
			t.Fatalf("Get t212 = %+v %v", g, err)
		}
		if g, _ := ar.Get(ctx, plain.ID); g.Platform != domain.PlatformEtoro {
			t.Fatalf("default platform = %q", g.Platform)
		}
		vs, _ := ar.ForUser(ctx, admin.ID)
		found := false
		for _, v := range vs {
			found = found || (v.ID == t212.ID && v.Platform == domain.PlatformTrading212)
		}
		if !found {
			t.Fatalf("ForUser lost the platform: %+v", vs)
		}
		// Update never changes the platform.
		t212.Name, t212.Platform, t212.UpdatedAt = "Renamed", domain.PlatformEtoro, t0.Add(time.Minute)
		if err := ar.Update(ctx, t212); err != nil {
			t.Fatal(err)
		}
		if g, _ := ar.Get(ctx, t212.ID); g.Name != "Renamed" || g.Platform != domain.PlatformTrading212 {
			t.Fatalf("after Update = %+v", g)
		}

		snap := func(acct string, at time.Time, cash, eq domain.Cents, cur string) domain.EquitySnapshot {
			return domain.EquitySnapshot{AccountID: acct, At: at, Currency: cur, Cash: cash, Equity: eq}
		}
		for _, e := range []domain.EquitySnapshot{
			snap(t212.ID, t0, 100_00, 1_000_00, "GBP"),
			snap(t212.ID, t0.Add(15*time.Minute), 100_00, 1_010_50, "GBP"),
			snap(plain.ID, t0.Add(15*time.Minute), -5, 9_000_000_000_00, "USD"),
			snap(plain.ID, t0.Add(-time.Hour), 1, 2, "USD"),
		} {
			if err := es.Add(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		if err := es.Add(ctx, snap(t212.ID, t0, 1, 1, "GBP")); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate: %v", err)
		}
		if err := es.Add(ctx, snap("nope", t0, 1, 1, "GBP")); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown account: %v", err)
		}
		got, err := es.List(ctx, []string{t212.ID, plain.ID}, t0)
		if err != nil || len(got) != 3 || got[0].AccountID != plain.ID || got[0].Equity != 9_000_000_000_00 || got[0].Cash != -5 ||
			got[1].AccountID != t212.ID || !got[1].At.Equal(t0) || got[1].Currency != "GBP" || got[2].Equity != 1_010_50 {
			t.Fatalf("List = %+v %v", got, err)
		}
		if got, _ := es.List(ctx, []string{t212.ID}, t0.Add(time.Minute)); len(got) != 1 {
			t.Fatalf("List since = %+v", got)
		}
		if got, err := es.List(ctx, nil, t0); err != nil || len(got) != 0 {
			t.Fatalf("List no ids = %+v %v", got, err)
		}
		if n, err := es.DeleteBefore(ctx, t0); err != nil || n != 1 {
			t.Fatalf("DeleteBefore = %d %v", n, err)
		}
		if err := ar.Delete(ctx, t212.ID); err != nil {
			t.Fatal(err)
		}
		if got, _ := es.List(ctx, []string{t212.ID}, time.Time{}); len(got) != 0 {
			t.Fatal("snapshots survived their account")
		}
	})
}
