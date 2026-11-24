package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Web research (PLAN R29): an LLM with its provider's built-in web search proposes shares
// for the watchlist. Its answer is untrusted data: it is parsed strictly here, every share
// still goes through eToro's eligibility check, and the watchlist only decides what the
// deterministic model looks at. Nothing is ever traded because of research.

// ResearchMode says what web research may do for an account.
type ResearchMode string

// Research modes.
const (
	// ResearchOff: no research runs.
	ResearchOff ResearchMode = "off"
	// ResearchPropose: results wait on the Watchlist page until the owner accepts them.
	ResearchPropose ResearchMode = "propose"
	// ResearchAutoAdd: eligible results join the watchlist at once (the default).
	ResearchAutoAdd ResearchMode = "auto_add"
)

// Valid reports whether m is a known mode.
func (m ResearchMode) Valid() bool {
	return m == ResearchOff || m == ResearchPropose || m == ResearchAutoAdd
}

// Research limits.
const (
	// MaxResearchShares bounds the shares one run may propose; DefaultResearchShares is the
	// default.
	MaxResearchShares     = 20
	DefaultResearchShares = 10
	// MaxResearchRunsPerDay bounds the runs per account in 24 hours (cost);
	// DefaultResearchRunsPerDay is the default. 0 switches research off.
	MaxResearchRunsPerDay     = 24
	DefaultResearchRunsPerDay = 3
	// MaxResearchReason is the longest reason kept (runes); a longer one is cut.
	MaxResearchReason = 300
	// MaxResearchSources is how many source links a share may carry.
	MaxResearchSources = 3
	// MaxResearchURL is the longest source link kept (bytes); a longer one is dropped.
	MaxResearchURL = 300
	// MaxResearchAnswer bounds the answer text that is parsed at all (bytes).
	MaxResearchAnswer = 64 << 10
	// MaxResearchProposals bounds the proposals waiting per account.
	MaxResearchProposals = 20
)

// ResearchExchanges are the exchanges research may name, in the order the prompt lists them.
var ResearchExchanges = []string{"LSE", "NYSE", "NASDAQ", "ASX"}

// ExchangeMarket is the calendar market of a research exchange (NASDAQ trades on the NYSE
// calendar); ok is false for any other exchange.
func ExchangeMarket(exchange string) (Market, bool) {
	switch strings.ToUpper(strings.TrimSpace(exchange)) {
	case "LSE":
		return MarketLSE, true
	case "NYSE", "NASDAQ":
		return MarketNYSE, true
	case "ASX":
		return MarketASX, true
	}
	return "", false
}

// ResearchPick is one share web research proposes. Every field is untrusted text from the
// web, validated by ValidateResearchPick; Reason and Sources are shown as plain text only.
type ResearchPick struct {
	Symbol   string // the ticker without a market suffix, upper case ("NVDA", "BARC", "CBA")
	Exchange string // one of ResearchExchanges
	Name     string
	Theme    string // the theme the model thinks it fits ("" = none)
	Reason   string // at most MaxResearchReason runes
	Sources  []string
}

// EToroSymbol is the symbol eToro lists the share under: "BARC.L" in London, "CBA.ASX" in
// Sydney, the plain ticker (or a US share class such as "BRK.B") in New York.
func (p ResearchPick) EToroSymbol() string {
	switch strings.ToUpper(p.Exchange) {
	case "LSE":
		return p.Symbol + ".L"
	case "ASX":
		return p.Symbol + ".ASX"
	}
	return p.Symbol
}

// ResearchRefusal is a share of an answer that was not used, and why.
type ResearchRefusal struct {
	Symbol string // as far as it could be read ("" when not at all)
	Reason string
}

// ResearchProposal is a pick waiting for the owner on the Watchlist page (mode propose).
type ResearchProposal struct {
	ID         string
	Instrument InstrumentID // eToro's instrument with the pick's symbol on its exchange
	ResearchPick
	At time.Time
}

// ResearchConfig is how web research runs (one setting for the whole installation).
type ResearchConfig struct {
	ProfileID  string // the LLM profile ("" = the default reviewer profile)
	MaxShares  int    // per run, 1..MaxResearchShares
	RunsPerDay int    // per account in 24 hours, 0 (off)..MaxResearchRunsPerDay
}

// DefaultResearchConfig is the configuration before anything is stored.
var DefaultResearchConfig = ResearchConfig{MaxShares: DefaultResearchShares, RunsPerDay: DefaultResearchRunsPerDay}

// Validate checks the configuration.
func (c ResearchConfig) Validate() error {
	if len(c.ProfileID) > 64 || strings.ContainsFunc(c.ProfileID, unicode.IsControl) {
		return Invalid("profileId", "no such profile")
	}
	if c.MaxShares < 1 || c.MaxShares > MaxResearchShares {
		return Invalid("maxShares", "1 to "+strconv.Itoa(MaxResearchShares))
	}
	if c.RunsPerDay < 0 || c.RunsPerDay > MaxResearchRunsPerDay {
		return Invalid("runsPerDay", "0 (off) to "+strconv.Itoa(MaxResearchRunsPerDay))
	}
	return nil
}

// researchPickJSON is the exact shape of one share in the answer; any other field makes
// the whole answer unusable.
type researchPickJSON struct {
	Symbol   string   `json:"symbol"`
	Exchange string   `json:"exchange"`
	Name     string   `json:"name"`
	Theme    string   `json:"theme"`
	Reason   string   `json:"reason"`
	Sources  []string `json:"sources"`
}

// ErrResearchAnswer is an answer that is not the expected JSON (nothing of it is used).
var ErrResearchAnswer = errors.New("the research answer is not the expected JSON")

// ParseResearchAnswer reads an LLM's research answer strictly. The answer is one JSON array
// of shares, either the whole text or the only ```json block in it. Anything else (no array,
// two blocks, unknown fields, wrong types, trailing data, more than max shares) is
// ErrResearchAnswer and nothing is used. Within a good array, each share is checked by
// ValidateResearchPick; a bad or repeated share is refused with its reason and the others
// are kept.
func ParseResearchAnswer(text string, max int) ([]ResearchPick, []ResearchRefusal, error) {
	if len(text) > MaxResearchAnswer {
		return nil, nil, fmt.Errorf("%w: longer than %d bytes", ErrResearchAnswer, MaxResearchAnswer)
	}
	body, err := researchJSON(text)
	if err != nil {
		return nil, nil, err
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(body)))
	dec.DisallowUnknownFields()
	var raw []researchPickJSON
	if err := dec.Decode(&raw); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrResearchAnswer, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("%w: data after the array", ErrResearchAnswer)
	}
	if raw == nil {
		return nil, nil, fmt.Errorf("%w: not an array", ErrResearchAnswer)
	}
	if len(raw) > max {
		return nil, nil, fmt.Errorf("%w: %d shares, more than the %d asked for", ErrResearchAnswer, len(raw), max)
	}
	var picks []ResearchPick
	var refused []ResearchRefusal
	seen := map[string]bool{}
	for _, r := range raw {
		p, err := ValidateResearchPick(ResearchPick(r))
		if err != nil {
			refused = append(refused, ResearchRefusal{Symbol: PrintableSymbol(r.Symbol), Reason: err.Error()})
			continue
		}
		key := p.EToroSymbol()
		if seen[key] {
			refused = append(refused, ResearchRefusal{Symbol: p.Symbol, Reason: "listed twice"})
			continue
		}
		seen[key] = true
		picks = append(picks, p)
	}
	return picks, refused, nil
}

// researchJSON finds the JSON array in an answer: the whole trimmed text, or the content of
// its only ```json block.
func researchJSON(text string) (string, error) {
	const fence = "```json"
	switch n := strings.Count(strings.ToLower(text), fence); {
	case n > 1:
		return "", fmt.Errorf("%w: more than one JSON block", ErrResearchAnswer)
	case n == 1:
		i := strings.Index(strings.ToLower(text), fence)
		rest := text[i+len(fence):]
		j := strings.Index(rest, "```")
		if j < 0 {
			return "", fmt.Errorf("%w: the JSON block is not closed", ErrResearchAnswer)
		}
		return strings.TrimSpace(rest[:j]), nil
	}
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "[") || !strings.HasSuffix(t, "]") {
		return "", fmt.Errorf("%w: no JSON array", ErrResearchAnswer)
	}
	return t, nil
}

// PrintableSymbol is a refused symbol as it may be shown: at most 12 letters, digits and
// dots (the rest of it is untrusted and dropped).
func PrintableSymbol(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		if b.Len() >= 12 {
			break
		}
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ValidateResearchPick checks and normalises one proposed share: the symbol is 1-10 letters
// or digits with an optional share class (".B"), and without a market suffix; the exchange
// is one of ResearchExchanges; the name is 1-100 characters; the theme at most 60; the
// reason 1-MaxResearchReason characters (longer ones are cut, whitespace runs become one
// space); and it has 1-MaxResearchSources http(s) links (bad links are dropped). Control
// characters are refused everywhere.
func ValidateResearchPick(p ResearchPick) (ResearchPick, error) {
	p.Symbol = strings.ToUpper(strings.TrimSpace(p.Symbol))
	if !validTicker(p.Symbol) {
		return ResearchPick{}, Invalid("symbol", "1-10 letters or digits, an optional class such as .B, no market suffix")
	}
	p.Exchange = strings.ToUpper(strings.TrimSpace(p.Exchange))
	if _, ok := ExchangeMarket(p.Exchange); !ok {
		return ResearchPick{}, Invalid("exchange", "must be LSE, NYSE, NASDAQ or ASX")
	}
	if strings.Contains(p.Symbol, ".") && p.Exchange != "NYSE" && p.Exchange != "NASDAQ" {
		return ResearchPick{}, Invalid("symbol", "a share class suffix only on NYSE or NASDAQ")
	}
	p.Name = strings.TrimSpace(p.Name)
	if n := utf8.RuneCountInString(p.Name); n < 1 || n > 100 || strings.ContainsFunc(p.Name, unicode.IsControl) {
		return ResearchPick{}, Invalid("name", "1-100 characters, no control characters")
	}
	p.Theme = strings.TrimSpace(p.Theme)
	if utf8.RuneCountInString(p.Theme) > 60 || strings.ContainsFunc(p.Theme, unicode.IsControl) {
		return ResearchPick{}, Invalid("theme", "at most 60 characters, no control characters")
	}
	reason, ok := cleanText(p.Reason)
	if !ok || reason == "" {
		return ResearchPick{}, Invalid("reason", "1-300 characters of text")
	}
	if utf8.RuneCountInString(reason) > MaxResearchReason {
		reason = string([]rune(reason)[:MaxResearchReason-1]) + "…"
	}
	p.Reason = reason
	var srcs []string
	for _, s := range p.Sources {
		if u, ok := SourceURL(s); ok && len(srcs) < MaxResearchSources {
			srcs = append(srcs, u)
		}
	}
	if len(srcs) == 0 {
		return ResearchPick{}, Invalid("sources", "at least one http(s) link")
	}
	p.Sources = srcs
	return p, nil
}

// validTicker: 1-10 upper-case letters or digits, optionally "." and a 1-letter class.
func validTicker(s string) bool {
	base, class, dotted := strings.Cut(s, ".")
	if len(base) < 1 || len(base) > 10 || (dotted && (len(class) != 1 || class[0] < 'A' || class[0] > 'Z')) {
		return false
	}
	for _, r := range base {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// cleanText turns whitespace runs (newlines included) into one space and refuses any other
// control character (ok=false).
func cleanText(s string) (string, bool) {
	if strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) && !unicode.IsSpace(r) }) {
		return "", false
	}
	return strings.Join(strings.Fields(s), " "), true
}

// SourceURL checks a source link for showing as text: http or https, a host, no user info,
// no spaces or control characters, at most MaxResearchURL bytes. The server never fetches it.
func SourceURL(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > MaxResearchURL || strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", false
	}
	return s, true
}

// WatchOrigin says who put a share on the watchlist, from its AddedBy: the scanner, web
// research, or a person ("user").
func WatchOrigin(addedBy string) string {
	switch addedBy {
	case WatchAddedByScanner:
		return "scanner"
	case WatchAddedByResearch:
		return "research"
	}
	return "user"
}

// Watchlist AddedBy values of the automatic sources (a person's entry holds the username).
const (
	WatchAddedByScanner  = "scanner"
	WatchAddedByResearch = "research"
)
