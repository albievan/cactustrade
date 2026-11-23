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

func TestNormalizeTheme(t *testing.T) {
	th, err := NormalizeTheme(Theme{Name: " Space ", Keywords: []string{" rocket ", "", "satellite"}})
	if err != nil || th.Name != "Space" || len(th.Keywords) != 2 || th.Keywords[0] != "rocket" {
		t.Fatalf("theme = %+v %v", th, err)
	}
	if _, err := NormalizeTheme(Theme{Name: "Semis", IndustryID: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeTheme(Theme{Name: "Picks", Instruments: []InstrumentID{1}}); err != nil {
		t.Fatal(err)
	}
	long := make([]string, 11)
	for i := range long {
		long[i] = "k"
	}
	many := make([]InstrumentID, 51)
	for _, bad := range []Theme{
		{Name: "", Keywords: []string{"a"}},
		{Name: "Empty"},
		{Name: "x", Keywords: long},
		{Name: "x", Keywords: []string{string(make([]rune, 101))}},
		{Name: "x", IndustryID: -1},
		{Name: "x", Keywords: []string{"a\nb"}},
		{Name: "a\tb", Keywords: []string{"a"}},
		{Name: "x", Instruments: many},
	} {
		if _, err := NormalizeTheme(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
