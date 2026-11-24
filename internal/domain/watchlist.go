package domain

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Watchlist themes, the watchlist file and holdings per share (PLAN R27).

// ShareFacts is what the theme scoring knows about one watchlist share. IndustryID and
// IndustryName are 0 / "" when eToro's industry of the share is not known.
type ShareFacts struct {
	Symbol       string
	Name         string
	IndustryID   int64
	IndustryName string
}

// ThemeMatch is how closely a share fits a theme: a score (0 = no match) and the reasons
// that make it up, in a fixed order, for people.
type ThemeMatch struct {
	Score   int
	Reasons []string
}

// Theme scoring weights (R27-c). Each search word counts at most once per field.
const (
	ScoreIndustry        = 4 // the share is in the theme's eToro industry
	ScoreSymbol          = 5 // a search word is the share's symbol (without its market suffix)
	ScoreNameWord        = 3 // a search word starts a word of the share's name
	ScoreIndustryKeyword = 2 // a search word starts a word of the share's industry name
)

// ScoreTheme scores how well a share fits a theme. It is deterministic: the same facts and
// theme always give the same score and reasons.
func ScoreTheme(s ShareFacts, t Theme) ThemeMatch {
	var m ThemeMatch
	if t.IndustryID > 0 && s.IndustryID == t.IndustryID {
		m.Score += ScoreIndustry
		ind := s.IndustryName
		if ind == "" {
			ind = "#" + strconv.FormatInt(t.IndustryID, 10)
		}
		m.Reasons = append(m.Reasons, "in the theme's eToro industry ("+ind+")")
	}
	base, _, _ := strings.Cut(strings.TrimSpace(s.Symbol), ".")
	for _, kw := range t.Keywords {
		k := strings.TrimSpace(kw)
		if k == "" {
			continue
		}
		if base != "" && strings.EqualFold(k, base) {
			m.Score += ScoreSymbol
			m.Reasons = append(m.Reasons, "search word “"+k+"” is the symbol")
		}
		if startsWord(s.Name, k) {
			m.Score += ScoreNameWord
			m.Reasons = append(m.Reasons, "name contains “"+k+"”")
		}
		if startsWord(s.IndustryName, k) {
			m.Score += ScoreIndustryKeyword
			m.Reasons = append(m.Reasons, "industry "+s.IndustryName+" contains “"+k+"”")
		}
	}
	return m
}

// startsWord reports whether word (case-insensitive) occurs in text at the start of a word:
// "semi" in "Taiwan Semiconductor", not "ice" in "Price".
func startsWord(text, word string) bool {
	t, w := strings.ToLower(text), strings.ToLower(strings.TrimSpace(word))
	if w == "" || t == "" {
		return false
	}
	for i := 0; i <= len(t)-len(w); {
		j := strings.Index(t[i:], w)
		if j < 0 {
			return false
		}
		at := i + j
		if at == 0 {
			return true
		}
		prev, _ := utf8.DecodeLastRuneInString(t[:at])
		if !unicode.IsLetter(prev) && !unicode.IsDigit(prev) {
			return true
		}
		_, size := utf8.DecodeRuneInString(t[at:])
		i = at + size
	}
	return false
}

// BestTheme is the theme a share fits best: the highest score above 0; a tie goes to the
// theme whose name sorts first (case-insensitive), then to the lower id. ok is false when no
// theme matches at all.
func BestTheme(s ShareFacts, themes []Theme) (best Theme, match ThemeMatch, ok bool) {
	for _, t := range themes {
		m := ScoreTheme(s, t)
		if m.Score == 0 {
			continue
		}
		if !ok || m.Score > match.Score || (m.Score == match.Score && themeBefore(t, best)) {
			best, match, ok = t, m, true
		}
	}
	return best, match, ok
}

func themeBefore(a, b Theme) bool {
	return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.ID, b.ID)) < 0
}

// WatchlistFileVersion is the only version of the watchlist file there is.
const WatchlistFileVersion = 1

// Watchlist file limits (R27-f).
const (
	MaxFileShares   = 200
	MaxFileThemes   = 50
	MaxSymbolLength = 32
)

// WatchlistFile is an account's watchlist and themes as exported to and imported from JSON.
// It holds no secret and no account id: only the account's name for people.
type WatchlistFile struct {
	Version     int
	ExportedAt  time.Time
	AccountName string
	Watchlist   []FileShare
	Themes      []FileTheme
}

// FileShare is one watchlist share in the file. Symbol and Market are for people and for a
// first check; eToro decides on import.
type FileShare struct {
	Instrument InstrumentID
	Symbol     string
	Market     string
}

// FileTheme is one theme in the file. LLMProfileName names the reviewer profile ("" = the
// default); profiles and their keys are never exported.
type FileTheme struct {
	Name           string
	Keywords       []string
	IndustryID     int64
	Instruments    []InstrumentID
	LLMProfileName string
}

// ValidateWatchlistFile checks a file before anything of it is used: the version, the
// counts, every theme (NormalizeTheme's rules), positive and unique ids, one theme per share
// and unique theme names. It returns the file with its themes normalised.
func ValidateWatchlistFile(f WatchlistFile) (WatchlistFile, error) {
	if f.Version != WatchlistFileVersion {
		return WatchlistFile{}, Invalid("version", "must be 1 (a cactusTrade watchlist file)")
	}
	if len(f.Watchlist) > MaxFileShares {
		return WatchlistFile{}, Invalid("watchlist", "at most "+strconv.Itoa(MaxFileShares)+" shares")
	}
	if len(f.Themes) > MaxFileThemes {
		return WatchlistFile{}, Invalid("themes", "at most "+strconv.Itoa(MaxFileThemes)+" themes")
	}
	seen := map[InstrumentID]bool{}
	for _, s := range f.Watchlist {
		switch {
		case s.Instrument <= 0:
			return WatchlistFile{}, Invalid("watchlist", "every instrumentId must be positive")
		case seen[s.Instrument]:
			return WatchlistFile{}, Invalid("watchlist", "instrument "+strconv.FormatInt(int64(s.Instrument), 10)+" appears twice")
		case !shortText(s.Symbol, MaxSymbolLength) || !shortText(s.Market, 16):
			return WatchlistFile{}, Invalid("watchlist", "symbol at most 32 and market at most 16 characters, no control characters")
		}
		seen[s.Instrument] = true
	}
	names := map[string]bool{}
	inTheme := map[InstrumentID]string{}
	out := f
	out.Themes = make([]FileTheme, 0, len(f.Themes))
	for _, ft := range f.Themes {
		t, err := NormalizeTheme(Theme{Name: ft.Name, Keywords: ft.Keywords, IndustryID: ft.IndustryID, Instruments: ft.Instruments})
		if err != nil {
			return WatchlistFile{}, err
		}
		key := strings.ToLower(t.Name)
		if names[key] {
			return WatchlistFile{}, Invalid("themes", "the theme "+t.Name+" appears twice")
		}
		names[key] = true
		if !shortText(ft.LLMProfileName, 60) {
			return WatchlistFile{}, Invalid("themes", "llmProfileName: at most 60 characters, no control characters")
		}
		for _, id := range t.Instruments {
			if id <= 0 {
				return WatchlistFile{}, Invalid("themes", "every instrument id must be positive")
			}
			if other, ok := inTheme[id]; ok {
				return WatchlistFile{}, Invalid("themes", "instrument "+strconv.FormatInt(int64(id), 10)+" is in both "+other+" and "+t.Name+" (one theme per share)")
			}
			inTheme[id] = t.Name
		}
		out.Themes = append(out.Themes, FileTheme{
			Name: t.Name, Keywords: t.Keywords, IndustryID: t.IndustryID, Instruments: t.Instruments,
			LLMProfileName: strings.TrimSpace(ft.LLMProfileName),
		})
	}
	return out, nil
}

func shortText(s string, n int) bool {
	return utf8.RuneCountInString(s) <= n && !strings.ContainsFunc(s, unicode.IsControl)
}

// SupportedMarket reports whether a market name is one cactusTrade trades.
func SupportedMarket(m string) bool { return slices.Contains(Markets, Market(m)) }

// InstrumentHolding is what an account holds of one instrument: its open positions added
// up. Units are net (a short's units count negative); Value, Cost and PnL are account
// currency minor units (USD cents on eToro). It is a view only: nothing sizes an order on it.
type InstrumentHolding struct {
	Instrument InstrumentID
	Positions  int
	Units      Micros
	Value      Cents
	Cost       Cents
	PnL        Cents
	Ours       int // positions cactusTrade opened
	Shorts     int
}

// HoldingsByInstrument adds up a portfolio's positions per instrument. ours holds the
// position ids cactusTrade opened. The result is sorted by instrument id.
func HoldingsByInstrument(pf Portfolio, ours map[int64]bool) []InstrumentHolding {
	by := map[InstrumentID]*InstrumentHolding{}
	for _, p := range pf.Positions {
		h, ok := by[p.Instrument]
		if !ok {
			h = &InstrumentHolding{Instrument: p.Instrument}
			by[p.Instrument] = h
		}
		h.Positions++
		if p.Long {
			h.Units += p.Units
		} else {
			h.Units -= p.Units
			h.Shorts++
		}
		h.Value += p.Amount + p.UnrealisedPnL
		h.Cost += p.Amount
		h.PnL += p.UnrealisedPnL
		if ours[p.ID] {
			h.Ours++
		}
	}
	out := make([]InstrumentHolding, 0, len(by))
	for _, h := range by {
		out = append(out, *h)
	}
	slices.SortFunc(out, func(a, b InstrumentHolding) int { return cmp.Compare(a.Instrument, b.Instrument) })
	return out
}
