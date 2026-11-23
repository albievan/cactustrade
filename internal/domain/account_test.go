package domain

import "testing"

func TestAccountAccess(t *testing.T) {
	if !AccessOwner.AtLeast(AccessTrade) || !AccessTrade.AtLeast(AccessView) || AccessView.AtLeast(AccessTrade) || AccessNone.AtLeast(AccessNone) || AccountAccess("x").AtLeast(AccessView) {
		t.Fatal("AtLeast")
	}
	if !AccessView.Shareable() || !AccessTrade.Shareable() || AccessOwner.Shareable() || AccessNone.Shareable() {
		t.Fatal("Shareable")
	}
}

func TestAccountKeyNamesAndName(t *testing.T) {
	if a, u := AccountKeyNames(Account{ID: "x", EnvKeys: true}); a != CredEtoroDemoAPIKey || u != CredEtoroDemoUserKey {
		t.Fatal("env keys")
	}
	if a, u := AccountKeyNames(Account{ID: "abc"}); a != "etoro.abc.api_key" || u != "etoro.abc.user_key" {
		t.Fatal("own keys")
	}
	if n, err := ValidateAccountName("  My ISA  "); err != nil || n != "My ISA" {
		t.Fatal("trim")
	}
	for _, bad := range []string{"", "   ", "a\tb", string(make([]rune, 61))} {
		if _, err := ValidateAccountName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
