package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestPlatformsAreViewOnlyButEtoro(t *testing.T) {
	tests := []struct {
		in       string
		want     Platform
		label    string
		tradable bool
	}{
		{"", PlatformEtoro, "eToro", true},
		{"etoro", PlatformEtoro, "eToro", true},
		{"trading212", PlatformTrading212, "Trading 212", false},
		{"saxo", PlatformSaxo, "Saxo", false},
		{"tiger", PlatformTiger, "Tiger Brokers", false},
	}
	for _, tt := range tests {
		got, err := ParsePlatform(tt.in)
		if err != nil || got != tt.want || got.Label() != tt.label {
			t.Errorf("ParsePlatform(%q) = %q %q %v", tt.in, got, got.Label(), err)
		}
		if (Account{Platform: got}).Tradable() != tt.tradable {
			t.Errorf("%s: Tradable() != %v", tt.in, tt.tradable)
		}
	}
	for _, bad := range []string{"ibkr", "moomoo", "SAXO", "Tiger"} {
		if _, err := ParsePlatform(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParsePlatform(%q) accepted", bad)
		}
	}
}

func TestAccountCredentialNames(t *testing.T) {
	tests := []struct {
		name string
		a    Account
		key  string
		want CredentialName
		ok   bool
	}{
		{"eToro api key", Account{ID: "e"}, "api_key", "etoro.e.api_key", true},
		{"eToro user key", Account{ID: "e", Platform: PlatformEtoro}, "user_key", "etoro.e.user_key", true},
		{"eToro env api key", Account{ID: "env", EnvKeys: true}, "api_key", CredEtoroDemoAPIKey, true},
		{"eToro env user key", Account{ID: "env", EnvKeys: true}, "user_key", CredEtoroDemoUserKey, true},
		{"t212 secret", Account{ID: "t", Platform: PlatformTrading212}, "api_secret", "t212.t.api_secret", true},
		{"t212 ignores EnvKeys", Account{ID: "t", Platform: PlatformTrading212, EnvKeys: true}, "api_key", "t212.t.api_key", true},
		{"saxo app key", Account{ID: "s", Platform: PlatformSaxo}, SaxoAppKey, "saxo.s.app_key", true},
		{"saxo refresh token", Account{ID: "s", Platform: PlatformSaxo}, SaxoRefreshToken, "saxo.s.refresh_token", true},
		{"saxo dev token", Account{ID: "s", Platform: PlatformSaxo}, SaxoDevToken, "saxo.s.dev_token", true},
		{"tiger key", Account{ID: "g", Platform: PlatformTiger}, TigerPrivateKey, "tiger.g.private_key", true},
		{"tiger licence", Account{ID: "g", Platform: PlatformTiger}, TigerLicense, "tiger.g.license", true},
		{"unknown key", Account{ID: "g", Platform: PlatformTiger}, "api_key", "", false},
		{"eToro has no secret", Account{ID: "e"}, "api_secret", "", false},
	}
	for _, tt := range tests {
		got, ok := AccountCredentialName(tt.a, tt.key)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: %q %v", tt.name, got, ok)
		}
	}
	// the first two are what AccountKeyNames returns, for every platform
	for _, p := range Platforms {
		a := Account{ID: "x", Platform: p}
		k, u := AccountKeyNames(a)
		specs := PlatformCredentials(p)
		if len(specs) < 2 || !strings.HasSuffix(string(k), "."+specs[0].Key) || !strings.HasSuffix(string(u), "."+specs[1].Key) {
			t.Errorf("%s: %s %s", p, k, u)
		}
		seen := map[string]bool{}
		for _, c := range specs {
			if c.Key == "" || c.Label == "" || c.Help == "" || seen[c.Key] {
				t.Errorf("%s: bad spec %+v", p, c)
			}
			seen[c.Key] = true
		}
	}
}

func TestCredentialsComplete(t *testing.T) {
	has := func(keys ...string) func(string) bool {
		return func(k string) bool {
			for _, x := range keys {
				if x == k {
					return true
				}
			}
			return false
		}
	}
	tests := []struct {
		name string
		p    Platform
		set  func(string) bool
		want bool
	}{
		{"eToro both", PlatformEtoro, has("api_key", "user_key"), true},
		{"eToro one", PlatformEtoro, has("api_key"), false},
		{"t212 both", PlatformTrading212, has("api_key", "api_secret"), true},
		{"saxo connected", PlatformSaxo, has(SaxoAppKey, SaxoAppSecret, SaxoRefreshToken), true},
		{"saxo not connected yet", PlatformSaxo, has(SaxoAppKey, SaxoAppSecret), false},
		{"saxo refresh token without app", PlatformSaxo, has(SaxoRefreshToken), false},
		{"saxo developer token", PlatformSaxo, has(SaxoDevToken), true},
		{"tiger without licence", PlatformTiger, has(TigerID, TigerAccount, TigerPrivateKey), true},
		{"tiger without key", PlatformTiger, has(TigerID, TigerAccount, TigerLicense), false},
		{"nothing", PlatformTiger, has(), false},
	}
	for _, tt := range tests {
		if got := CredentialsComplete(tt.p, tt.set); got != tt.want {
			t.Errorf("%s: %v", tt.name, got)
		}
	}
}

func TestNormalizeCredentialFor(t *testing.T) {
	pem := CredentialSpec{Key: TigerPrivateKey, Multiline: true}
	one := CredentialSpec{Key: "api_key"}
	tests := []struct {
		name string
		spec CredentialSpec
		in   string
		want string
		ok   bool
	}{
		{"pem keeps lines", pem, "\r\n-----BEGIN PRIVATE KEY-----\r\nAAAA\r\n-----END PRIVATE KEY-----\r\n", "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----", true},
		{"pem tab ok", pem, "a\tb", "a\tb", true},
		{"pem empty", pem, " \n ", "", false},
		{"pem NUL", pem, "a\x00b", "", false},
		{"pem too long", pem, strings.Repeat("a", MaxCredentialLen+1), "", false},
		{"single line refuses newline", one, "a\nb", "", false},
		{"single line trims", one, " k \n", "k", true},
	}
	for _, tt := range tests {
		got, err := NormalizeCredentialFor(tt.spec, tt.in)
		if got != tt.want || (err == nil) != tt.ok {
			t.Errorf("%s: %q %v", tt.name, got, err)
		}
	}
	if _, ok := CredentialSpecFor(PlatformSaxo, "nope"); ok {
		t.Fatal("unknown key found")
	}
}

func TestCurrencyDecimals(t *testing.T) {
	tests := []struct {
		code string
		dec  int
		per  int64
	}{
		{"JPY", 0, 1}, {"jpy", 0, 1}, {" KRW ", 0, 1}, {"USD", 2, 100}, {"AUD", 2, 100}, {"GBX", 2, 100}, {"", 2, 100},
	}
	for _, tt := range tests {
		if d, p := CurrencyDecimals(tt.code), MinorUnitsPerUnit(tt.code); d != tt.dec || p != tt.per {
			t.Errorf("%q: %d %d", tt.code, d, p)
		}
	}
	if !errors.Is(fmtWrap(ErrReconnect), ErrReconnect) {
		t.Fatal("ErrReconnect")
	}
}

func fmtWrap(err error) error { return errors.Join(errors.New("saxo"), err) }
