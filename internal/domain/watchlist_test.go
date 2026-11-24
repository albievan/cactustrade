package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestScoreTheme(t *testing.T) {
	space := Theme{ID: "t1", Name: "Space", Keywords: []string{"rocket", "space", "RKLB"}, IndustryID: 7}
	tests := []struct {
		name    string
		share   ShareFacts
		theme   Theme
		score   int
		reasons []string
	}{
		{"nothing", ShareFacts{Symbol: "AAPL", Name: "Apple"}, space, 0, nil},
		{"symbol", ShareFacts{Symbol: "rklb", Name: "Something"}, space, ScoreSymbol, []string{"search word “RKLB” is the symbol"}},
		{"symbol with a market suffix", ShareFacts{Symbol: "RKLB.L"}, space, ScoreSymbol, []string{"search word “RKLB” is the symbol"}},
		{
			"name word",
			ShareFacts{Symbol: "RKLB", Name: "Rocket Lab USA"},
			space,
			ScoreNameWord + ScoreSymbol,
			[]string{"name contains “rocket”", "search word “RKLB” is the symbol"},
		},
		{"word start only", ShareFacts{Symbol: "X", Name: "Aerospace Corp"}, space, 0, nil},
		{
			"industry",
			ShareFacts{Symbol: "X", IndustryID: 7, IndustryName: "Space Technology"},
			space,
			ScoreIndustry + ScoreIndustryKeyword,
			[]string{"in the theme's eToro industry (Space Technology)", "industry Space Technology contains “space”"},
		},
		{"industry without a name", ShareFacts{Symbol: "X", IndustryID: 7}, space, ScoreIndustry, []string{"in the theme's eToro industry (#7)"}},
		{"other industry", ShareFacts{Symbol: "X", IndustryID: 8}, space, 0, nil},
		{"blank words ignored", ShareFacts{Symbol: "X", Name: "X"}, Theme{Keywords: []string{" ", ""}}, 0, nil},
		{"after punctuation", ShareFacts{Symbol: "Y", Name: "Virgin-Space Ltd"}, space, ScoreNameWord, []string{"name contains “space”"}},
		{"later occurrence", ShareFacts{Symbol: "Y", Name: "Pricespace and Space"}, space, ScoreNameWord, []string{"name contains “space”"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for range 3 { // deterministic
				m := ScoreTheme(tt.share, tt.theme)
				if m.Score != tt.score || !reflect.DeepEqual(m.Reasons, tt.reasons) {
					t.Fatalf("ScoreTheme = %d %q, want %d %q", m.Score, m.Reasons, tt.score, tt.reasons)
				}
			}
		})
	}
}

func TestBestTheme(t *testing.T) {
	themes := []Theme{
		{ID: "b", Name: "banks", Keywords: []string{"bank"}},
		{ID: "a", Name: "Banking", Keywords: []string{"bank"}},
		{ID: "c", Name: "Chips", Keywords: []string{"semi"}, IndustryID: 3},
		{ID: "d", Name: "chips", Keywords: []string{"semi"}, IndustryID: 3},
	}
	tests := []struct {
		name  string
		share ShareFacts
		want  string
		ok    bool
	}{
		{"none", ShareFacts{Symbol: "AAPL", Name: "Apple"}, "", false},
		{"tie by name", ShareFacts{Symbol: "BARC.L", Name: "Barclays Bank"}, "a", true},
		{"higher score wins", ShareFacts{Symbol: "NVDA", Name: "Nvidia Bank", IndustryID: 3, IndustryName: "Semiconductors"}, "c", true},
		{"tie by id", ShareFacts{Symbol: "TSM", Name: "Taiwan Semiconductor"}, "c", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, m, ok := BestTheme(tt.share, themes)
			if ok != tt.ok || got.ID != tt.want || (ok && m.Score == 0) {
				t.Fatalf("BestTheme = %q %+v %v, want %q %v", got.ID, m, ok, tt.want, tt.ok)
			}
			// the order of the themes does not matter
			rev := []Theme{themes[3], themes[2], themes[1], themes[0]}
			if got2, _, _ := BestTheme(tt.share, rev); got2.ID != got.ID {
				t.Fatalf("order changed the answer: %q vs %q", got2.ID, got.ID)
			}
		})
	}
}

func TestValidateWatchlistFile(t *testing.T) {
	good := func() WatchlistFile {
		return WatchlistFile{
			Version: 1, Watchlist: []FileShare{{Instrument: 1, Symbol: "AAPL", Market: "NYSE"}},
			Themes: []FileTheme{{Name: " Space ", Keywords: []string{" rocket ", ""}, Instruments: []InstrumentID{1}, LLMProfileName: " Desk "}},
		}
	}
	many := make([]FileShare, MaxFileShares+1)
	for i := range many {
		many[i] = FileShare{Instrument: InstrumentID(i + 1)}
	}
	tests := []struct {
		name  string
		edit  func(f *WatchlistFile)
		field string // "" = valid
	}{
		{"valid", func(*WatchlistFile) {}, ""},
		{"version 0", func(f *WatchlistFile) { f.Version = 0 }, "version"},
		{"version 2", func(f *WatchlistFile) { f.Version = 2 }, "version"},
		{"too many shares", func(f *WatchlistFile) { f.Watchlist = many }, "watchlist"},
		{"too many themes", func(f *WatchlistFile) { f.Themes = make([]FileTheme, MaxFileThemes+1) }, "themes"},
		{"negative id", func(f *WatchlistFile) { f.Watchlist[0].Instrument = -1 }, "watchlist"},
		{"duplicate share", func(f *WatchlistFile) { f.Watchlist = append(f.Watchlist, f.Watchlist[0]) }, "watchlist"},
		{"control character in a symbol", func(f *WatchlistFile) { f.Watchlist[0].Symbol = "A\x00B" }, "watchlist"},
		{"long symbol", func(f *WatchlistFile) { f.Watchlist[0].Symbol = strings.Repeat("A", 33) }, "watchlist"},
		{"long market", func(f *WatchlistFile) { f.Watchlist[0].Market = strings.Repeat("M", 17) }, "watchlist"},
		{"empty theme name", func(f *WatchlistFile) { f.Themes[0].Name = " " }, "name"},
		{"script in a name is just text", func(f *WatchlistFile) { f.Themes[0].Name = "<script>x</script>" }, ""},
		{"empty theme", func(f *WatchlistFile) { f.Themes[0].Keywords, f.Themes[0].Instruments = nil, nil }, "keywords"},
		{"too many keywords", func(f *WatchlistFile) { f.Themes[0].Keywords = strings.Split(strings.Repeat("k,", 11), ",") }, "keywords"},
		{"51 shares", func(f *WatchlistFile) { f.Themes[0].Instruments = make([]InstrumentID, 51) }, "instruments"},
		{"negative industry", func(f *WatchlistFile) { f.Themes[0].IndustryID = -3 }, "industryId"},
		{"zero share id", func(f *WatchlistFile) { f.Themes[0].Instruments = []InstrumentID{0} }, "themes"},
		{"duplicate theme", func(f *WatchlistFile) { f.Themes = append(f.Themes, FileTheme{Name: "SPACE", Keywords: []string{"x"}}) }, "themes"},
		{"share in two themes", func(f *WatchlistFile) {
			f.Themes = append(f.Themes, FileTheme{Name: "Other", Instruments: []InstrumentID{1}})
		}, "themes"},
		{"long profile name", func(f *WatchlistFile) { f.Themes[0].LLMProfileName = strings.Repeat("p", 61) }, "themes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := good()
			tt.edit(&f)
			got, err := ValidateWatchlistFile(f)
			if tt.field == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != tt.field {
				t.Fatalf("err = %v, want field %q", err, tt.field)
			}
			if got.Version != 0 {
				t.Fatal("a refused file came back")
			}
		})
	}
	got, err := ValidateWatchlistFile(good())
	if err != nil || got.Themes[0].Name != "Space" || !reflect.DeepEqual(got.Themes[0].Keywords, []string{"rocket"}) || got.Themes[0].LLMProfileName != "Desk" {
		t.Fatalf("normalised = %+v %v", got.Themes, err)
	}
}

func TestHoldingsByInstrument(t *testing.T) {
	pf := Portfolio{Positions: []Position{
		{ID: 1, Instrument: 9, Units: 2_000_000, Amount: 100_00, UnrealisedPnL: 5_00, Long: true},
		{ID: 2, Instrument: 9, Units: 1_000_000, Amount: 50_00, UnrealisedPnL: -1_00, Long: true},
		{ID: 3, Instrument: 4, Units: 500_000, Amount: 20_00, UnrealisedPnL: 2_00, Long: false, CFD: true},
	}}
	got := HoldingsByInstrument(pf, map[int64]bool{2: true, 3: true})
	want := []InstrumentHolding{
		{Instrument: 4, Positions: 1, Units: -500_000, Value: 22_00, Cost: 20_00, PnL: 2_00, Ours: 1, Shorts: 1},
		{Instrument: 9, Positions: 2, Units: 3_000_000, Value: 154_00, Cost: 150_00, PnL: 4_00, Ours: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("holdings = %+v", got)
	}
	if len(HoldingsByInstrument(Portfolio{}, nil)) != 0 {
		t.Fatal("empty portfolio")
	}
	if !SupportedMarket("ASX") || SupportedMarket("XETRA") || SupportedMarket("") {
		t.Fatal("SupportedMarket")
	}
}
