package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

type env struct {
	svc   *Service
	store *fakes.Store
	clock *fakes.Clock
	sec   *fakes.SecretStore
	admin Principal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := fakes.NewStore()
	clk := &fakes.Clock{T: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
	sec := &fakes.SecretStore{Now: clk.Now}
	svc, err := New(Deps{Store: st, Secrets: sec, Hasher: fakes.Hasher{}, Random: &fakes.Random{}, Clock: clk},
		Config{Version: "0.1.0", Build: "7"})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAgentLoop(true) // as after a clean start-up (6.13); TestStartWithoutLoop covers the rest
	e := &env{svc: svc, store: st, clock: clk, sec: sec}
	ctx := context.Background()
	if created, err := svc.EnsureDefaultAdmin(ctx); err != nil || !created {
		t.Fatalf("EnsureDefaultAdmin = %v %v", created, err)
	}
	u, err := st.Users().GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	first := Principal{UserID: u.ID, Username: u.Username, Role: u.Role, Via: ViaSession, RemoteIP: "10.0.0.1", MustChangePassword: true}
	if err := svc.ChangePassword(ctx, first, "admin", "admin password 1"); err != nil {
		t.Fatal(err)
	}
	e.admin = Principal{UserID: u.ID, Username: u.Username, Role: u.Role, Via: ViaSession}
	return e
}

func (e *env) user(t *testing.T, name string, role domain.Role) Principal {
	t.Helper()
	u, err := e.svc.CreateUser(context.Background(), e.admin, name, name+" password!", role)
	if err != nil {
		t.Fatal(err)
	}
	return Principal{UserID: u.ID, Username: u.Username, Role: role, Via: ViaSession}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Deps{}, Config{}); err == nil {
		t.Fatal("missing deps accepted")
	}
	st := fakes.NewStore()
	_, err := New(Deps{Store: st, Secrets: &fakes.SecretStore{}, Hasher: fakes.Hasher{Fail: true}, Random: &fakes.Random{}, Clock: &fakes.Clock{}}, Config{})
	if err == nil {
		t.Fatal("hasher failure ignored")
	}
}

func TestDefaultAdmin(t *testing.T) {
	ctx := context.Background()
	st := fakes.NewStore()
	s, err := New(Deps{Store: st, Secrets: &fakes.SecretStore{}, Hasher: fakes.Hasher{}, Random: &fakes.Random{}, Clock: &fakes.Clock{}}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if created, err := s.EnsureDefaultAdmin(ctx); err != nil || !created {
		t.Fatalf("first start: %v %v", created, err)
	}
	if created, err := s.EnsureDefaultAdmin(ctx); err != nil || created {
		t.Fatalf("second start: %v %v", created, err)
	}
	if n, _ := st.Users().Count(ctx); n != 1 {
		t.Fatalf("users = %d", n)
	}

	// admin/admin signs in, but can do nothing except Me, change password, sign out
	res, err := s.Login(ctx, "admin", "admin", "")
	if err != nil || !res.User.MustChangePassword || res.User.Role != domain.RoleAdmin {
		t.Fatalf("default login = %+v %v", res.User, err)
	}
	p, _, err := s.SessionPrincipal(ctx, res.Token, "")
	if err != nil || !p.MustChangePassword {
		t.Fatalf("principal = %+v %v", p, err)
	}
	// Status answers only the real banner's facts (review minor 1: the change-password page
	// shows the banner): no version, build, start time or account.
	if st, err := s.Status(ctx, p); err != nil || st.Version != "" || st.Build != "" || st.Account != nil || !st.StartedAt.IsZero() {
		t.Fatalf("status before change: %+v %v", st, err)
	}
	if _, err := s.ListUsers(ctx, p); !errors.Is(err, domain.ErrPasswordChangeRequired) {
		t.Fatalf("users before change: %v", err)
	}
	if _, err := s.Me(ctx, p); err != nil {
		t.Fatalf("me before change: %v", err)
	}
	for _, bad := range []string{"admin", "short", "Admin"} {
		if err := s.ChangePassword(ctx, p, "admin", bad); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("new password %q: %v", bad, err)
		}
	}
	if err := s.ChangePassword(ctx, p, "admin", "a much better password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SessionPrincipal(ctx, res.Token, ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("session survived the password change")
	}
	if _, err := s.Login(ctx, "admin", "admin", ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("default password still works")
	}
	res, err = s.Login(ctx, "admin", "a much better password", "")
	if err != nil || res.User.MustChangePassword {
		t.Fatalf("login after change = %+v %v", res.User, err)
	}
	p, _, _ = s.SessionPrincipal(ctx, res.Token, "")
	if _, err := s.Status(ctx, p); err != nil {
		t.Fatalf("status after change: %v", err)
	}

	broken, _ := New(Deps{Store: fakes.NewStore(), Secrets: &fakes.SecretStore{}, Hasher: fakes.Hasher{}, Random: &fakes.Random{}, Clock: &fakes.Clock{}}, Config{})
	broken.d.Hasher = fakes.Hasher{Fail: true}
	if _, err := broken.EnsureDefaultAdmin(ctx); err == nil {
		t.Fatal("hash failure ignored")
	}
}

func TestAPIKeyOfDefaultAdminIsLocked(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	k, err := e.svc.CreateAPIKey(ctx, e.admin, "ci", domain.RoleViewer, 0)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := e.store.Users().Get(ctx, e.admin.UserID)
	u.MustChangePassword = true
	if err := e.store.Users().Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.APIKeyPrincipal(ctx, k.Secret, "")
	if err != nil || !p.MustChangePassword {
		t.Fatalf("principal = %+v %v", p, err)
	}
	if st, err := e.svc.Status(ctx, p); err != nil || st.Version != "" || st.Account != nil {
		t.Fatalf("status (banner facts only): %+v %v", st, err)
	}
	if _, err := e.svc.ListUsers(ctx, p); !errors.Is(err, domain.ErrPasswordChangeRequired) {
		t.Fatalf("users: %v", err)
	}
}

func TestLoginLogoutSession(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	tests := []struct {
		name, user, pw string
		ok             bool
	}{
		{"ok", "admin", "admin password 1", true},
		{"case-insensitive user", " ADMIN ", "admin password 1", true},
		{"wrong password", "admin", "wrong password 1", false},
		{"unknown user", "ghost", "admin password 1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := e.svc.Login(ctx, tt.user, tt.pw, "10.0.0.2")
			if tt.ok != (err == nil) {
				t.Fatalf("err = %v", err)
			}
			if !tt.ok && !errors.Is(err, domain.ErrUnauthenticated) {
				t.Fatalf("err = %v", err)
			}
			if tt.ok && (r.Token == "" || r.CSRFToken == "" || r.Session.TokenHash == r.Token) {
				t.Fatal("token must be returned and only its hash stored")
			}
		})
	}
	r, _ := e.svc.Login(ctx, "admin", "admin password 1", "")
	p, csrf, err := e.svc.SessionPrincipal(ctx, r.Token, "1.2.3.4")
	if err != nil || p.Username != "admin" || p.Via != ViaSession || csrf != r.CSRFToken || p.RemoteIP != "1.2.3.4" {
		t.Fatalf("SessionPrincipal = %+v %q %v", p, csrf, err)
	}
	if _, _, err := e.svc.SessionPrincipal(ctx, "", ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("empty token")
	}
	if _, _, err := e.svc.SessionPrincipal(ctx, "bogus", ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("bogus token")
	}
	// idle expiry
	e.clock.Advance(2 * time.Hour)
	if _, _, err := e.svc.SessionPrincipal(ctx, r.Token, ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("idle session accepted")
	}
	r, _ = e.svc.Login(ctx, "admin", "admin password 1", "")
	if err := e.svc.Logout(ctx, r.Token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.SessionPrincipal(ctx, r.Token, ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("session survived logout")
	}
	// disabled user cannot log in and loses sessions
	op := e.user(t, "olivia", domain.RoleOperator)
	r, _ = e.svc.Login(ctx, "olivia", "olivia password!", "")
	dis := true
	if _, err := e.svc.UpdateUser(ctx, e.admin, op.UserID, UserUpdate{Disabled: &dis}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.SessionPrincipal(ctx, r.Token, ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("disabled user's session accepted")
	}
	if _, err := e.svc.Login(ctx, "olivia", "olivia password!", ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("disabled user logged in")
	}
	// expired sessions are purged
	e.clock.Advance(24 * time.Hour)
	if n, err := e.svc.PurgeExpiredSessions(ctx); err != nil || n == 0 {
		t.Fatalf("purge = %d %v", n, err)
	}
	// failed logins are audited without the password
	for _, a := range e.store.AuditEntries() {
		if strings.Contains(a.Details, "password 1") {
			t.Fatal("password in audit log")
		}
	}
}

func TestChangePasswordAndMe(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r, _ := e.svc.Login(ctx, "admin", "admin password 1", "")
	tests := []struct {
		name      string
		p         Principal
		cur, next string
		want      error
	}{
		{"api key cannot", Principal{UserID: e.admin.UserID, Role: domain.RoleAdmin, Via: ViaAPIKey}, "admin password 1", "new password 12", domain.ErrForbidden},
		{"wrong current", e.admin, "nope", "new password 12", domain.ErrInvalid},
		{"weak next", e.admin, "admin password 1", "short", domain.ErrInvalid},
		{"ok", e.admin, "admin password 1", "new password 12", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := e.svc.ChangePassword(ctx, tt.p, tt.cur, tt.next); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	if _, _, err := e.svc.SessionPrincipal(ctx, r.Token, ""); err == nil {
		t.Fatal("old sessions must end after a password change")
	}
	if _, err := e.svc.Login(ctx, "admin", "new password 12", ""); err != nil {
		t.Fatal(err)
	}
	if u, err := e.svc.Me(ctx, e.admin); err != nil || u.Username != "admin" {
		t.Fatal(err)
	}
	if _, err := e.svc.Me(ctx, Principal{}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("anonymous Me")
	}
}

func TestUsersRBACAndLastAdmin(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	op := e.user(t, "olivia", domain.RoleOperator)
	view := e.user(t, "victor", domain.RoleViewer)

	for _, p := range []Principal{op, view, {}} {
		if _, err := e.svc.ListUsers(ctx, p); err == nil {
			t.Errorf("%q listed users", p.Username)
		}
		if _, err := e.svc.CreateUser(ctx, p, "zed", "zed password 12", domain.RoleAdmin); err == nil {
			t.Errorf("%q created a user", p.Username)
		}
	}
	if _, err := e.svc.CreateUser(ctx, e.admin, "olivia", "another password", domain.RoleViewer); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := e.svc.CreateUser(ctx, e.admin, "zed", "zed password 12", domain.Role("god")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad role: %v", err)
	}
	us, err := e.svc.ListUsers(ctx, e.admin)
	if err != nil || len(us) != 3 {
		t.Fatalf("ListUsers = %d %v", len(us), err)
	}

	viewer, admin, bad := domain.RoleViewer, domain.RoleAdmin, domain.Role("x")
	dis := true
	weak := "short"
	good := "brand new password"
	tests := []struct {
		name string
		id   string
		up   UserUpdate
		want error
	}{
		{"demote last admin", e.admin.UserID, UserUpdate{Role: &viewer}, domain.ErrConflict},
		{"disable last admin", e.admin.UserID, UserUpdate{Disabled: &dis}, domain.ErrConflict},
		{"bad role", op.UserID, UserUpdate{Role: &bad}, domain.ErrInvalid},
		{"weak password", op.UserID, UserUpdate{Password: &weak}, domain.ErrInvalid},
		{"reset password", op.UserID, UserUpdate{Password: &good}, nil},
		{"promote", op.UserID, UserUpdate{Role: &admin}, nil},
		{"demote now allowed", e.admin.UserID, UserUpdate{Role: &viewer}, nil},
		{"unknown", "nope", UserUpdate{Role: &viewer}, domain.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := e.svc.UpdateUser(ctx, e.admin, tt.id, tt.up); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	opAdmin := Principal{UserID: op.UserID, Username: op.Username, Role: domain.RoleAdmin, Via: ViaSession}
	if err := e.svc.DeleteUser(ctx, opAdmin, op.UserID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("self delete: %v", err)
	}
	if err := e.svc.DeleteUser(ctx, view, op.UserID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer delete: %v", err)
	}
	if err := e.svc.DeleteUser(ctx, opAdmin, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown delete: %v", err)
	}
	if err := e.svc.DeleteUser(ctx, opAdmin, view.UserID); err != nil {
		t.Fatal(err)
	}
	// opAdmin is now the only admin: another admin cannot delete it… create one and try
	ad2, err := e.svc.CreateUser(ctx, opAdmin, "ada", "ada password 123", domain.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteUser(ctx, opAdmin, ad2.ID); err != nil {
		t.Fatalf("deleting a non-last admin: %v", err)
	}
	ad3, _ := e.svc.CreateUser(ctx, opAdmin, "ada3", "ada password 123", domain.RoleAdmin)
	p3 := Principal{UserID: ad3.ID, Username: ad3.Username, Role: domain.RoleAdmin, Via: ViaSession}
	if _, err := e.svc.UpdateUser(ctx, p3, op.UserID, UserUpdate{Disabled: &dis}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteUser(ctx, opAdmin, ad3.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("last enabled admin deleted: %v", err)
	}
}

func TestAPIKeys(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	op := e.user(t, "olivia", domain.RoleOperator)
	tests := []struct {
		name string
		p    Principal
		kn   string
		role domain.Role
		days int
		want error
	}{
		{"anonymous", Principal{}, "ci", domain.RoleViewer, 0, domain.ErrUnauthenticated},
		{"via api key", Principal{UserID: op.UserID, Role: domain.RoleOperator, Via: ViaAPIKey}, "ci", domain.RoleViewer, 0, domain.ErrForbidden},
		{"above own role", op, "ci", domain.RoleAdmin, 0, domain.ErrInvalid},
		{"bad role", op, "ci", domain.Role("x"), 0, domain.ErrInvalid},
		{"bad name", op, " ", domain.RoleViewer, 0, domain.ErrInvalid},
		{"bad expiry", op, "ci", domain.RoleViewer, 400, domain.ErrInvalid},
		{"ok", op, "ci", domain.RoleOperator, 30, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := e.svc.CreateAPIKey(ctx, tt.p, tt.kn, tt.role, tt.days); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	ck, err := e.svc.CreateAPIKey(ctx, op, "bot", domain.RoleOperator, 0)
	if err != nil || !strings.HasPrefix(ck.Secret, APIKeyPrefix+ck.Key.ID+"_") || ck.Key.SecretHash == "" {
		t.Fatalf("created %+v %v", ck, err)
	}
	p, err := e.svc.APIKeyPrincipal(ctx, ck.Secret, "9.9.9.9")
	if err != nil || p.Via != ViaAPIKey || p.Role != domain.RoleOperator || p.APIKeyID != ck.Key.ID {
		t.Fatalf("APIKeyPrincipal = %+v %v", p, err)
	}
	for _, bad := range []string{"", "Bearer x", APIKeyPrefix + "x", APIKeyPrefix + ck.Key.ID + "_wrong", APIKeyPrefix + "missing_secret"} {
		if _, err := e.svc.APIKeyPrincipal(ctx, bad, ""); !errors.Is(err, domain.ErrUnauthenticated) {
			t.Errorf("APIKeyPrincipal(%q) = %v", bad, err)
		}
	}
	// listing: own keys vs admin sees all
	if ks, _ := e.svc.ListAPIKeys(ctx, op); len(ks) != 2 {
		t.Fatalf("op keys = %d", len(ks))
	}
	adminKey, _ := e.svc.CreateAPIKey(ctx, e.admin, "adm", domain.RoleAdmin, 0)
	if ks, _ := e.svc.ListAPIKeys(ctx, e.admin); len(ks) != 3 {
		t.Fatalf("admin keys = %d", len(ks))
	}
	if _, err := e.svc.ListAPIKeys(ctx, Principal{}); err == nil {
		t.Fatal("anonymous list")
	}
	// revoke: not someone else's
	if err := e.svc.RevokeAPIKey(ctx, op, adminKey.Key.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revoke other's: %v", err)
	}
	if err := e.svc.RevokeAPIKey(ctx, op, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("revoke unknown")
	}
	// demotion revokes keys above the new role
	viewer := domain.RoleViewer
	if _, err := e.svc.UpdateUser(ctx, e.admin, op.UserID, UserUpdate{Role: &viewer}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.APIKeyPrincipal(ctx, ck.Secret, ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("operator key survived demotion to viewer")
	}
	if err := e.svc.RevokeAPIKey(ctx, e.admin, adminKey.Key.ID); err != nil {
		t.Fatal(err)
	}
	// expiry
	vk, _ := e.svc.CreateAPIKey(ctx, Principal{UserID: op.UserID, Username: "olivia", Role: domain.RoleViewer, Via: ViaSession}, "short", domain.RoleViewer, 1)
	if _, err := e.svc.APIKeyPrincipal(ctx, vk.Secret, ""); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(25 * time.Hour)
	if _, err := e.svc.APIKeyPrincipal(ctx, vk.Secret, ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("expired key accepted")
	}
}

// TestCredentialsStatusOnly: the deployment credential list says what is set and where from,
// never a value; there is no global write any more (PLAN 7.1.3, D27: eToro keys are set per
// account, see TestKeyChangeOnlyWhenQuiet).
func TestCredentialsStatusOnly(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	op := e.user(t, "olivia", domain.RoleOperator)
	if err := e.sec.Put(ctx, domain.CredEtoroDemoAPIKey, "super-secret-value"); err != nil {
		t.Fatal(err)
	}
	st, err := e.svc.Credentials(ctx, op)
	if err != nil || len(st) != len(domain.Credentials) {
		t.Fatal(err)
	}
	for _, c := range st {
		if (c.Name == domain.CredEtoroDemoAPIKey) != c.Set {
			t.Errorf("%s set=%v", c.Name, c.Set)
		}
		if strings.Contains(c.Label+c.Source+c.EnvVar, "super-secret") {
			t.Fatal("a value in the status")
		}
	}
	e.sec.Err = errors.New("store down")
	if _, err := e.svc.Credentials(ctx, op); err == nil {
		t.Fatal("store error swallowed")
	}
	if _, err := e.svc.Credentials(ctx, Principal{}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("anonymous")
	}
}

func TestAuditAndStatus(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	op := e.user(t, "olivia", domain.RoleOperator)
	if _, err := e.svc.AuditLog(ctx, op, 10); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("operator read audit")
	}
	es, err := e.svc.AuditLog(ctx, e.admin, 0)
	if err != nil || len(es) != 1 || es[0].Action != "user.create" {
		t.Fatalf("audit = %+v %v", es, err)
	}
	es, _ = e.svc.AuditLog(ctx, e.admin, 1000)
	if len(es) != 3 || es[1].Action != "auth.password_changed" || es[1].RemoteIP != "10.0.0.1" || es[2].Action != "setup.default_admin_created" {
		t.Fatalf("audit = %+v", es)
	}
	st, err := e.svc.Status(ctx, op)
	if err != nil || st.Mode != domain.ModeDemo || st.AgentState != domain.AgentStopped || st.Version != "0.1.0" || st.Database != "fake" {
		t.Fatalf("status = %+v %v", st, err)
	}
	if _, err := e.svc.Status(ctx, Principal{}); err == nil {
		t.Fatal("anonymous status")
	}
	// mode: unknown values fail closed to demo; only exact "real" is real
	for v, want := range map[domain.TradingMode]domain.TradingMode{"real": domain.ModeReal, "REAL": domain.ModeDemo, "": domain.ModeDemo, "demo": domain.ModeDemo} {
		if got := accountMode(domain.Account{Mode: v}); got != want {
			t.Errorf("accountMode(%q) = %s", v, got)
		}
	}
	if err := e.svc.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	e.store.PingErr = errors.New("down")
	if err := e.svc.Ready(ctx); err == nil {
		t.Fatal("ready while db down")
	}
}
