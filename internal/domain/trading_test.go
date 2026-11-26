package domain

import "testing"

func TestMarketForSymbol(t *testing.T) {
	for sym, want := range map[string]Market{
		"AAPL": MarketNYSE, "brk.b": MarketNYSE, "BARC.L": MarketLSE, "VOD.L": MarketLSE, "CBA.ASX": MarketASX, "bhp.asx": MarketASX, ".ASX": "", "CBA.AX": "",
		"SAP.DE": "", "MC.PA": "", "": "", ".L": "", "X.LL": "", "A.1": "",
	} {
		got, ok := MarketForSymbol(sym)
		if got != want || ok != (want != "") {
			t.Errorf("MarketForSymbol(%q) = %q %v, want %q", sym, got, ok, want)
		}
	}
	if m, ok := MarketForSymbol(" barc.l\t"); m != MarketLSE || !ok {
		t.Error("surrounding space and case must not matter")
	}
}

func TestIsShare(t *testing.T) {
	for typ, want := range map[string]bool{"Stocks": true, "stock": true, "ETF": false, "Crypto": false, "CFD": false, "": false} {
		if IsShare(typ) != want {
			t.Errorf("IsShare(%q) != %v", typ, want)
		}
	}
}

func TestPositionConforms(t *testing.T) {
	for _, ok := range []Position{{RealAsset: true, Long: true, Leverage: 1}, {CFD: true, Long: false, Leverage: 1}} {
		if !ok.Conforms() {
			t.Fatalf("%+v must conform", ok)
		}
	}
	if (Position{Long: true}).Direction() != Long || (Position{}).Direction() != Short {
		t.Fatal("Direction")
	}
	for _, p := range []Position{
		{RealAsset: false, Long: true, Leverage: 1},
		{CFD: true, Long: true, Leverage: 1},
		{RealAsset: true, Long: false, Leverage: 1},
		{CFD: true, Long: false, Leverage: 2},
		{RealAsset: true, Long: true, Leverage: 2},
		{},
	} {
		if p.Conforms() {
			t.Errorf("%+v must not conform", p)
		}
	}
}

func TestMicrosString(t *testing.T) {
	for m, want := range map[Micros]string{0: "0", 1: "0.000001", 189123456: "189.123456", 189100000: "189.1", -1500000: "-1.5", 2000000: "2"} {
		if got := m.String(); got != want {
			t.Errorf("Micros(%d) = %q, want %q", int64(m), got, want)
		}
	}
}

func TestEligibilityByDirection(t *testing.T) {
	e := Eligibility{RealLong: true, Reason: "", MinPositionAmount: 10, ShortCFD: false, ShortReason: "not shortable", MinShortAmount: 50}
	if ok, _ := e.Allows(Long); !ok {
		t.Fatal("long")
	}
	if ok, why := e.Allows(Short); ok || why != "not shortable" {
		t.Fatal("short")
	}
	if ok, _ := e.Allows("sideways"); ok {
		t.Fatal("unknown direction")
	}
	if e.MinAmount(Long) != 10 || e.MinAmount(Short) != 50 || !Long.Valid() || Direction("x").Valid() {
		t.Fatal("helpers")
	}
}

func TestParseMicros(t *testing.T) {
	ok := map[string]Micros{
		"0": 0, "3": 3_000_000, "3.5": 3_500_000, "0.000001": 1, "-0.000001": -1, "15.00": 15_000_000,
		"189.1": 189_100_000, "9223372036854": 9_223_372_036_854_000_000, "9223372036854.775807": 9_223_372_036_854_775_807,
	}
	for s, want := range ok {
		got, err := ParseMicros(s)
		if err != nil || got != want {
			t.Errorf("ParseMicros(%q) = %d %v, want %d", s, got, err, want)
		}
		if err == nil && got.String() != s && s != "15.00" {
			if back, _ := ParseMicros(got.String()); back != got {
				t.Errorf("round trip of %q", s)
			}
		}
	}
	for _, s := range []string{"", "-", ".", "1.", ".5", "1.0000001", "1e3", " 1", "1 ", "1,000", "+1", "0x10", "9223372036855", "9223372036854.775808", "--1", "1.-5"} {
		if _, err := ParseMicros(s); err == nil {
			t.Errorf("ParseMicros(%q) accepted", s)
		}
	}
	for _, m := range []Micros{0, 1, -1, 1_234_567, 9_223_372_036_854_775_807, -9_223_372_036_854_775_807} {
		if back, err := ParseMicros(m.String()); err != nil || back != m {
			t.Errorf("round trip %d: %d %v", m, back, err)
		}
	}
}

func TestMarketLabel(t *testing.T) {
	for m, want := range map[Market]string{MarketLSE: "London (LSE)", MarketNYSE: "New York (NYSE/NASDAQ)", MarketASX: "Sydney (ASX)", "": "", "XETRA": ""} {
		if got := m.Label(); got != want {
			t.Errorf("Market(%q).Label() = %q, want %q", m, got, want)
		}
	}
}
