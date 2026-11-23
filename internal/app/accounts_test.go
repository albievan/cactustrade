package app

import (
	"context"
	"errors"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

func TestAccountsLifecycle(t *testing.T) {
	ctx := context.Background()
	e, b := withBroker(t) // admin owns "Main demo"
	op := e.user(t, "olivia", domain.RoleOperator)
	viewer := e.user(t, "vera", domain.RoleViewer)

	if _, err := e.svc.CreateAccount(ctx, viewer, "x", domain.ModeDemo); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("a viewer created an account")
	}
	for _, bad := range []struct {
		name string
		mode domain.TradingMode
	}{{"", domain.ModeDemo}, {"ok", "paper"}} {
		if _, err := e.svc.CreateAccount(ctx, op, bad.name, bad.mode); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%+v accepted", bad)
		}
	}
	isa, err := e.svc.CreateAccount(ctx, op, "Olivia ISA", domain.ModeReal)
	if err != nil || isa.Access != domain.AccessOwner || isa.OwnerID != op.UserID {
		t.Fatalf("create = %+v %v", isa, err)
	}
	// the operator's current account is their own; the admin's stays theirs
	if cur, _ := e.svc.CurrentAccount(ctx, op); cur.ID != isa.ID {
		t.Fatalf("operator current = %+v", cur)
	}
	if st, _ := e.svc.Status(ctx, op); st.Mode != domain.ModeReal || st.Account == nil || st.Account.Name != "Olivia ISA" {
		t.Fatalf("status of a real account = %+v", st)
	}
	// the admin cannot see or touch the operator's account until it is shared
	if _, err := e.svc.SetCurrentAccount(ctx, e.admin, isa.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("admin saw an unshared account")
	}
	if err := e.svc.ShareAccount(ctx, op, isa.ID, "admin", domain.AccessView); err != nil {
		t.Fatal(err)
	}
	if v, err := e.svc.SetCurrentAccount(ctx, e.admin, isa.ID); err != nil || v.Access != domain.AccessView {
		t.Fatalf("switch = %+v %v", v, err)
	}
	if _, err := e.svc.AddToWatchlist(ctx, e.admin, 1); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("view access added to the watchlist")
	}
	if err := e.svc.ShareAccount(ctx, op, isa.ID, "admin", domain.AccessTrade); err != nil { // upgrade
		t.Fatal(err)
	}
	if _, err := e.svc.AddToWatchlist(ctx, e.admin, 1); err != nil {
		t.Fatalf("trade access add: %v", err)
	}
	if _, err := e.svc.RenameAccount(ctx, e.admin, isa.ID, "mine now"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("a sharee renamed the account")
	}
	// a viewer-role user given trade access can only view
	if err := e.svc.ShareAccount(ctx, op, isa.ID, "vera", domain.AccessTrade); err != nil {
		t.Fatal(err)
	}
	vs, _ := e.svc.Accounts(ctx, viewer)
	if len(vs) != 1 || vs[0].Access != domain.AccessView {
		t.Fatalf("viewer's access = %+v", vs)
	}
	// sharing rules
	for _, c := range []struct {
		user   string
		access domain.AccountAccess
	}{{"nobody", domain.AccessView}, {"olivia", domain.AccessView}, {"vera", domain.AccessOwner}} {
		if err := e.svc.ShareAccount(ctx, op, isa.ID, c.user, c.access); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("share %+v accepted", c)
		}
	}
	sh, err := e.svc.AccountShares(ctx, op, isa.ID)
	if err != nil || len(sh) != 2 {
		t.Fatalf("shares = %+v %v", sh, err)
	}
	if _, err := e.svc.AccountShares(ctx, e.admin, isa.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("a sharee listed the shares")
	}
	if err := e.svc.UnshareAccount(ctx, op, isa.ID, viewer.UserID); err != nil {
		t.Fatal(err)
	}
	if vs, _ := e.svc.Accounts(ctx, viewer); len(vs) != 0 {
		t.Fatal("unshare did not remove access")
	}
	if err := e.svc.UnshareAccount(ctx, op, isa.ID, viewer.UserID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unshare twice")
	}
	// keys: owner and web session only; write-only; status reports where from
	key := op
	key.Via = ViaAPIKey
	if err := e.svc.SetAccountKeys(ctx, key, isa.ID, "a", "u"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("API key set account keys")
	}
	if err := e.svc.SetAccountKeys(ctx, e.admin, isa.ID, "a", "u"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("a sharee set the keys")
	}
	if err := e.svc.SetAccountKeys(ctx, op, isa.ID, "bad\x01", ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("control character accepted")
	}
	if err := e.svc.SetAccountKeys(ctx, op, isa.ID, "api-1", ""); err != nil {
		t.Fatal(err)
	}
	if ks, _ := e.svc.AccountKeysStatus(ctx, op, isa.ID); !ks.APIKeySet || ks.UserKeySet || ks.FromEnvironment {
		t.Fatalf("keys = %+v", ks)
	}
	_ = e.svc.SetAccountKeys(ctx, op, isa.ID, "", "user-1")
	if ks, _ := e.svc.AccountKeysStatus(ctx, e.admin, isa.ID); !ks.APIKeySet || !ks.UserKeySet {
		t.Fatalf("keys after both = %+v", ks)
	}
	ak, _ := domain.AccountKeyNames(isa.Account)
	if v, _ := e.sec.Get(ctx, ak); v != "api-1" {
		t.Fatal("key not stored under the account's name")
	}
	// the broker sees the account through the context
	b.SeeAccount = true
	if _, err := e.svc.TestConnection(ctx, op, isa.ID); err != nil || b.LastAccount.ID != isa.ID {
		t.Fatalf("broker got account %+v (%v)", b.LastAccount, err)
	}
	// rename, delete
	if v, err := e.svc.RenameAccount(ctx, op, isa.ID, "  Olivia real  "); err != nil || v.Name != "Olivia real" {
		t.Fatalf("rename = %+v %v", v, err)
	}
	if _, err := e.svc.RenameAccount(ctx, op, isa.ID, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("empty name")
	}
	if err := e.svc.DeleteUser(ctx, e.admin, op.UserID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("deleted the owner of an account: %v", err)
	}
	_ = e.store.Settings().Set(ctx, agentStateKey(isa.ID), string(domain.AgentRunning), e.clock.T)
	if err := e.svc.DeleteAccount(ctx, op, isa.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("deleted an account whose agent runs")
	}
	_ = e.store.Settings().Set(ctx, agentStateKey(isa.ID), string(domain.AgentStopped), e.clock.T)
	if err := e.svc.DeleteAccount(ctx, key, isa.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("API key deleted an account")
	}
	if err := e.svc.DeleteAccount(ctx, op, isa.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sec.Get(ctx, ak); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("keys survived their account")
	}
	if err := e.svc.DeleteUser(ctx, e.admin, op.UserID); err != nil {
		t.Fatalf("delete user after the account: %v", err)
	}
	// admin falls back to an account they can still see
	if cur, _ := e.svc.CurrentAccount(ctx, e.admin); cur.Name != "Main demo" {
		t.Fatalf("fallback current = %+v", cur)
	}
}

func TestEnsureEnvAccount(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if ok, err := e.svc.EnsureEnvAccount(ctx); err != nil || ok {
		t.Fatalf("without keys: %v %v", ok, err)
	}
	_ = e.sec.Put(ctx, domain.CredEtoroDemoAPIKey, "a")
	if ok, _ := e.svc.EnsureEnvAccount(ctx); ok {
		t.Fatal("created with half a pair")
	}
	_ = e.sec.Put(ctx, domain.CredEtoroDemoUserKey, "u")
	if ok, err := e.svc.EnsureEnvAccount(ctx); err != nil || !ok {
		t.Fatalf("create: %v %v", ok, err)
	}
	if ok, _ := e.svc.EnsureEnvAccount(ctx); ok {
		t.Fatal("created twice")
	}
	a, err := e.store.Accounts().Get(ctx, domain.EnvAccountID)
	if err != nil || !a.EnvKeys || a.OwnerID != e.admin.UserID || a.Mode != domain.ModeDemo {
		t.Fatalf("env account = %+v %v", a, err)
	}
	if ks, _ := e.svc.AccountKeysStatus(ctx, e.admin, a.ID); !ks.APIKeySet || !ks.UserKeySet {
		t.Fatalf("env keys = %+v", ks)
	}
	// keys of the env account are the shared demo pair; deleting the account keeps them
	if err := e.svc.DeleteAccount(ctx, e.admin, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sec.Get(ctx, domain.CredEtoroDemoAPIKey); err != nil {
		t.Fatal("deleting the env account removed the deployment's keys")
	}
	// no admin → nothing to own it
	st := fakes.NewStore()
	sec := &fakes.SecretStore{}
	_ = sec.Put(ctx, domain.CredEtoroDemoAPIKey, "a")
	_ = sec.Put(ctx, domain.CredEtoroDemoUserKey, "u")
	svc, _ := New(Deps{Store: st, Secrets: sec, Hasher: fakes.Hasher{}, Random: &fakes.Random{}, Clock: &fakes.Clock{}}, Config{})
	if ok, err := svc.EnsureEnvAccount(ctx); err != nil || ok {
		t.Fatalf("without an admin: %v %v", ok, err)
	}
}

func TestAccountContext(t *testing.T) {
	if _, ok := ports.AccountFrom(context.Background()); ok {
		t.Fatal("account from an empty context")
	}
	ctx := ports.WithAccount(context.Background(), domain.Account{ID: "x"})
	if a, ok := ports.AccountFrom(ctx); !ok || a.ID != "x" {
		t.Fatal("round trip")
	}
}
