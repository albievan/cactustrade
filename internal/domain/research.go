package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Theme is an account's own "industry" (Space, Semiconductors…): the scanner reviews the
// shares that its search words, its eToro industry and its picked shares lead to.
type Theme struct {
	ID          string
	AccountID   string
	Name        string
	Keywords    []string // eToro searches by name or symbol
	IndustryID  int64    // 0 = none; else one of eToro's stock industries
	Enabled     bool
	Instruments []InstrumentID // picked by hand
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Theme limits.
const (
	MaxThemeKeywords    = 10
	MaxThemeInstruments = 50
)

// NormalizeTheme trims and validates a theme.
func NormalizeTheme(t Theme) (Theme, error) {
	t.Name = strings.TrimSpace(t.Name)
	if n := utf8.RuneCountInString(t.Name); n < 1 || n > 60 || strings.ContainsFunc(t.Name, unicode.IsControl) {
		return Theme{}, Invalid("name", "1-60 characters, no control characters")
	}
	var kws []string
	for _, k := range t.Keywords {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if utf8.RuneCountInString(k) > 100 || strings.ContainsFunc(k, unicode.IsControl) {
			return Theme{}, Invalid("keywords", "each at most 100 characters, no control characters")
		}
		kws = append(kws, k)
	}
	if len(kws) > MaxThemeKeywords {
		return Theme{}, Invalid("keywords", "at most 10")
	}
	t.Keywords = kws
	if t.IndustryID < 0 {
		return Theme{}, Invalid("industryId", "must not be negative")
	}
	if len(t.Instruments) > MaxThemeInstruments {
		return Theme{}, Invalid("instruments", "at most 50")
	}
	if len(t.Keywords) == 0 && t.IndustryID == 0 && len(t.Instruments) == 0 {
		return Theme{}, Invalid("keywords", "give search words, an industry or shares")
	}
	return t, nil
}

// ReviewOutcome is what came of a scanner review.
type ReviewOutcome string

// Review outcomes.
const (
	ReviewNoSignal  ReviewOutcome = "no_signal" // a rule did not pass
	ReviewCandidate ReviewOutcome = "candidate" // every rule passed (already on the watchlist)
	ReviewAdded     ReviewOutcome = "added"     // every rule passed; added to the watchlist
	ReviewSkipped   ReviewOutcome = "skipped"   // passed, but not added (not eligible, list full)
	ReviewError     ReviewOutcome = "error"     // data or broker failure (nothing concluded)
)

// ReviewCheck is one rule applied to a stock.
type ReviewCheck struct {
	Rule   string
	Pass   bool
	Detail string
}

// Review is the scanner's latest look at one stock for one account.
type Review struct {
	AccountID  string
	Instrument InstrumentID
	Symbol     string
	Theme      string
	At         time.Time
	Outcome    ReviewOutcome
	Direction  Direction // of the candidate, when there is one
	Probs      map[Regime]float64
	Checks     []ReviewCheck
	Summary    string
}
