package domain

import "testing"

func TestMarketForSymbol(t *testing.T) {
	for sym, want := range map[string]Market{
		"AAPL": MarketNYSE, "brk.b": MarketNYSE, "BARC.L": MarketLSE, "VOD.L": MarketLSE,
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
	ok := Position{RealAsset: true, Long: true, Leverage: 1}
	if !ok.Conforms() {
		t.Fatal("real long x1 must conform")
	}
	for _, p := range []Position{
		{RealAsset: false, Long: true, Leverage: 1},
		{RealAsset: true, Long: false, Leverage: 1},
		{RealAsset: true, Long: true, Leverage: 2},
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
