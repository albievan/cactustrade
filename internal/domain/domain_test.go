package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRoles(t *testing.T) {
	tests := []struct {
		role Role
		perm Permission
		want bool
	}{
		{RoleViewer, PermView, true},
		{RoleViewer, PermAgentControl, false},
		{RoleViewer, PermKillSwitch, false},
		{RoleViewer, PermManageSecrets, false},
		{RoleOperator, PermAgentControl, true},
		{RoleOperator, PermKillSwitch, true},
		{RoleOperator, PermManageSettings, false},
		{RoleOperator, PermManageUsers, false},
		{RoleAdmin, PermManageUsers, true},
		{RoleAdmin, PermManageSecrets, true},
		{RoleAdmin, PermSwitchMode, true},
		{Role("root"), PermView, false},
	}
	for _, tt := range tests {
		if got := tt.role.Allows(tt.perm); got != tt.want {
			t.Errorf("%s.Allows(%s) = %v, want %v", tt.role, tt.perm, got, tt.want)
		}
	}
	// admin ⊃ operator ⊃ viewer
	for _, p := range RoleViewer.Permissions() {
		if !RoleOperator.Allows(p) {
			t.Errorf("operator lacks viewer permission %s", p)
		}
	}
	for _, p := range RoleOperator.Permissions() {
		if !RoleAdmin.Allows(p) {
			t.Errorf("admin lacks operator permission %s", p)
		}
	}
}

func TestRoleOrderAndParse(t *testing.T) {
	if !RoleViewer.AtMost(RoleAdmin) || !RoleAdmin.AtMost(RoleAdmin) || RoleAdmin.AtMost(RoleOperator) {
		t.Fatal("AtMost ordering")
	}
	if Role("x").AtMost(RoleAdmin) || RoleViewer.AtMost(Role("x")) {
		t.Fatal("unknown roles must never compare")
	}
	for _, s := range []string{"admin", "operator", "viewer"} {
		if r, err := ParseRole(s); err != nil || string(r) != s || !r.Valid() {
			t.Errorf("ParseRole(%q) = %v %v", s, r, err)
		}
	}
	if _, err := ParseRole("Admin"); !errors.Is(err, ErrInvalid) {
		t.Errorf("ParseRole(Admin) err = %v", err)
	}
}

func TestUsernameAndPassword(t *testing.T) {
	if NormalizeUsername("  Alice ") != "alice" {
		t.Fatal("normalize")
	}
	for _, u := range []string{"al", "alice", "a.b-c_d", "0x"} {
		if err := ValidateUsername(u); err != nil {
			t.Errorf("ValidateUsername(%q) = %v", u, err)
		}
	}
	for _, u := range []string{"", "a", "-alice", "Alice", "al ice", strings.Repeat("a", 33)} {
		if err := ValidateUsername(u); err == nil {
			t.Errorf("ValidateUsername(%q) accepted", u)
		}
	}
	pw := []struct {
		p, user string
		ok      bool
	}{
		{"correct horse battery", "alice", true},
		{"short", "alice", false},
		{strings.Repeat("x", 129), "alice", false},
		{"longusername1", "longusername1", false},
		{"ééééééééééé", "bob", false}, // 11 characters, 22 bytes
		{"éééééééééééé", "bob", true},
	}
	for _, tt := range pw {
		err := ValidatePassword(tt.p, tt.user)
		if (err == nil) != tt.ok {
			t.Errorf("ValidatePassword(%q) = %v, want ok=%v", tt.p, err, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Errorf("error does not wrap ErrInvalid: %v", err)
		}
	}
}

func TestSessionAndKey(t *testing.T) {
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	s := Session{LastSeen: now, ExpiresAt: now.Add(12 * time.Hour)}
	p := DefaultSessionPolicy
	if !s.Active(now.Add(59*time.Minute), p) {
		t.Error("active within idle")
	}
	if s.Active(now.Add(61*time.Minute), p) {
		t.Error("idle expiry")
	}
	s.LastSeen = now.Add(12 * time.Hour)
	if s.Active(now.Add(12*time.Hour), p) {
		t.Error("absolute expiry")
	}
	k := APIKey{}
	if !k.Usable(now) {
		t.Error("no expiry = usable")
	}
	exp := now.Add(time.Hour)
	k.ExpiresAt = &exp
	if !k.Usable(now) || k.Usable(exp) {
		t.Error("expiry")
	}
	if ValidateKeyName("") == nil || ValidateKeyName(strings.Repeat("k", 65)) == nil || ValidateKeyName("ci") != nil {
		t.Error("key name")
	}
}

func TestCredentials(t *testing.T) {
	for _, c := range Credentials {
		if n, err := ParseCredentialName(string(c.Name)); err != nil || n != c.Name {
			t.Errorf("ParseCredentialName(%s)", c.Name)
		}
	}
	if _, err := ParseCredentialName("aws_key"); err == nil {
		t.Error("unknown credential accepted")
	}
	a, u := EtoroKeys(ModeDemo)
	if a != CredEtoroDemoAPIKey || u != CredEtoroDemoUserKey {
		t.Error("demo keys")
	}
	a, u = EtoroKeys(ModeReal)
	if a != CredEtoroRealAPIKey || u != CredEtoroRealUserKey {
		t.Error("real keys")
	}
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"  abc\n", "abc", true},
		{"", "", false},
		{"   ", "", false},
		{"a\x00b", "", false},
		{strings.Repeat("k", MaxCredentialLen+1), "", false},
	}
	for _, tt := range tests {
		got, err := NormalizeCredential(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("NormalizeCredential(%q) = %q %v", tt.in, got, err)
		}
	}
}

func TestErrors(t *testing.T) {
	err := Invalid("f", "bad")
	if !errors.Is(err, ErrInvalid) || err.Error() != "f: bad" {
		t.Fatal(err)
	}
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "f" {
		t.Fatal("As")
	}
}

func TestMoney(t *testing.T) {
	tests := map[Cents]string{0: "0.00", 5: "0.05", 1205: "12.05", -1205: "-12.05", -5: "-0.05", 100: "1.00"}
	for c, want := range tests {
		if got := c.String(); got != want {
			t.Errorf("%d.String() = %s, want %s", int64(c), got, want)
		}
	}
	p := Portfolio{AvailableCash: 1000, PendingOpen: 200, Positions: []Position{
		{Amount: 500, UnrealisedPnL: -50}, {Amount: 300, UnrealisedPnL: 25},
	}}
	if got := p.Equity(); got != 1975 {
		t.Errorf("Equity = %d", got)
	}
}
