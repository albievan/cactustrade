package domain

import (
	"math"
	"math/bits"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// LLMProviderID names an LLM provider the reviewer can use.
type LLMProviderID string

// LLMProvider describes a provider for the setup page.
type LLMProvider struct {
	ID             LLMProviderID
	Label          string
	DefaultBaseURL string // "" = the provider's SDK default (Anthropic) or must be entered
	KeyRequired    bool
	Note           string
	// Anthropic uses its own Messages API; every other provider speaks the OpenAI-compatible
	// chat-completions API.
	Anthropic bool
}

// LLMProviders is the list on the LLM reviewer page.
var LLMProviders = []LLMProvider{
	{ID: "anthropic", Label: "Anthropic (Claude)", KeyRequired: true, Anthropic: true, Note: "Claude Opus 5.5 by default; structured output and a server-side fallback model on refusals."},
	{ID: "openai", Label: "OpenAI", DefaultBaseURL: "https://api.openai.com/v1", KeyRequired: true},
	{ID: "deepseek", Label: "DeepSeek", DefaultBaseURL: "https://api.deepseek.com/v1", KeyRequired: true},
	{ID: "openrouter", Label: "OpenRouter", DefaultBaseURL: "https://openrouter.ai/api/v1", KeyRequired: true, Note: "One key for many models; model ids look like vendor/model."},
	{ID: "kimi", Label: "Kimi (Moonshot AI)", DefaultBaseURL: "https://api.moonshot.ai/v1", KeyRequired: true},
	{ID: "gemini", Label: "Google Gemini", DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", KeyRequired: true, Note: "Gemini's OpenAI-compatible endpoint."},
	{ID: "grok", Label: "Grok (xAI)", DefaultBaseURL: "https://api.x.ai/v1", KeyRequired: true},
	{ID: "ollama", Label: "Ollama (remote server)", KeyRequired: false, Note: "Your Ollama server's OpenAI-compatible address, e.g. http://gpu-box:11434/v1; a key only if it sits behind one."},
	{ID: "ollama_local", Label: "Ollama (this machine)", DefaultBaseURL: "http://localhost:11434/v1", Note: "In a container use http://host.docker.internal:11434/v1."},
}

// LLMProviderByID finds a provider.
func LLMProviderByID(id LLMProviderID) (LLMProvider, bool) {
	for _, p := range LLMProviders {
		if p.ID == id {
			return p, true
		}
	}
	return LLMProvider{}, false
}

// DefaultAnthropicModel is used when no model is chosen for Anthropic.
const DefaultAnthropicModel = "claude-opus-5-5"

// LLMConfig is the reviewer's setup (the key is a separate write-only credential).
type LLMConfig struct {
	Enabled  bool
	Provider LLMProviderID
	Model    string
	BaseURL  string // "" = the provider's default
	// TimeoutSeconds bounds one review; a timeout is a rejection (default 60).
	TimeoutSeconds int
}

// Endpoint returns the base URL to use ("" for Anthropic's default).
func (c LLMConfig) Endpoint() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	p, _ := LLMProviderByID(c.Provider)
	return p.DefaultBaseURL
}

// Validate checks the configuration.
func (c LLMConfig) Validate() error {
	p, ok := LLMProviderByID(c.Provider)
	if !ok {
		return Invalid("provider", "unknown provider")
	}
	if len(c.Model) > 200 || strings.ContainsAny(c.Model, " \t\r\n") {
		return Invalid("model", "at most 200 characters, no spaces")
	}
	if c.Model == "" && !p.Anthropic {
		return Invalid("model", "choose a model")
	}
	if c.TimeoutSeconds < 0 || c.TimeoutSeconds > 600 {
		return Invalid("timeoutSeconds", "must be 0 (default) to 600")
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return Invalid("baseUrl", "must be an http(s) URL without credentials, query or fragment")
		}
	} else if p.DefaultBaseURL == "" && !p.Anthropic {
		return Invalid("baseUrl", "this provider needs the server's address")
	}
	return nil
}

// FullSize is a verdict's size share for "the whole proposed size": 10 000 bp = 100 %.
const FullSize BasisPoints = 10_000

// ClampVerdict enforces that the reviewer can only approve, shrink or reject: an unknown
// kind is a rejection, an approval is the full size, a reduction is capped at the full size,
// and a "reduce" to nothing (0 bp or less) is a rejection with size 0.
func ClampVerdict(v Verdict) Verdict {
	switch v.Kind {
	case VerdictApprove:
		v.SizeBp = FullSize
	case VerdictReduce:
		if v.SizeBp <= 0 {
			return Verdict{Kind: VerdictReject, Reason: "reduce to nothing: " + v.Reason}
		}
		v.SizeBp = min(v.SizeBp, FullSize)
	default:
		return Verdict{Kind: VerdictReject, Reason: v.Reason}
	}
	return v
}

// ApplyVerdict returns the size left after the reviewer's verdict: never more than size,
// 0 for a rejection, otherwise size × SizeBp / 10 000 rounded down. It clamps the verdict
// itself, so an unclamped or hostile verdict cannot enlarge a trade.
func ApplyVerdict(size Cents, v Verdict) Cents {
	v = ClampVerdict(v)
	if v.Kind == VerdictReject || size <= 0 {
		return 0
	}
	// size/10 000 and size%10 000 separately: no overflow for any int64 size.
	q, r := int64(size)/int64(FullSize), int64(size)%int64(FullSize)
	return Cents(q*int64(v.SizeBp) + r*int64(v.SizeBp)/int64(FullSize))
}

// LLMProfile is one named reviewer setup (provider, model, address, time limit) with its own
// write-only key (LLMKeyName). Several can be set up; the reviewer uses the active one.
type LLMProfile struct {
	ID   string
	Name string
	LLMConfig
	// InputPrice and OutputPrice are what the provider charges, in USD micros per million
	// input and output tokens, as the admin entered them (OD13: no built-in price table).
	// 0 = unknown, and then every call's cost is unknown.
	InputPrice  Micros
	OutputPrice Micros
}

// MaxLLMPrice bounds a price per million tokens: USD 1 000 000 (in micros).
const MaxLLMPrice Micros = 1_000_000 * 1_000_000

// MaxLLMTokens caps the token counts Cost uses; a count above it is a provider bug.
const MaxLLMTokens int64 = 1 << 40

// CostUnknown is LLMCall.CostMicros when the profile has no prices.
const CostUnknown Micros = -1

// tokensPerPrice is the unit of a price: one million tokens.
const tokensPerPrice = 1_000_000

// Cost returns what in input and out output tokens cost at the profile's prices, in USD
// micros, rounded up so a call is never shown cheaper than it was. It is CostUnknown when
// either price is unknown (0) or invalid (negative). Counts below 0 count as 0 and counts
// above MaxLLMTokens as MaxLLMTokens; the sum is computed in 128 bits and capped at
// math.MaxInt64, so the cost never overflows and never falls when a count grows.
func (p LLMProfile) Cost(in, out int64) Micros {
	if p.InputPrice <= 0 || p.OutputPrice <= 0 {
		return CostUnknown
	}
	clamp := func(n int64) uint64 { return uint64(min(max(n, 0), MaxLLMTokens)) }
	hi1, lo1 := bits.Mul64(clamp(in), uint64(p.InputPrice))
	hi2, lo2 := bits.Mul64(clamp(out), uint64(p.OutputPrice))
	lo, carry := bits.Add64(lo1, lo2, 0)
	hi, _ := bits.Add64(hi1, hi2, carry) // < 2^127: no overflow
	lo, carry = bits.Add64(lo, tokensPerPrice-1, 0)
	hi += carry
	if hi >= tokensPerPrice { // the quotient would not fit in 64 bits
		return Micros(math.MaxInt64)
	}
	q, _ := bits.Div64(hi, lo, tokensPerPrice)
	return Micros(min(q, math.MaxInt64))
}

// MaxLLMProfiles bounds how many reviewer setups can be stored.
const MaxLLMProfiles = 20

// LLMKeyName is the secret holding a profile's API key.
func LLMKeyName(profileID string) CredentialName {
	return CredentialName("llm." + profileID + ".api_key")
}

// Normalize trims the profile's text fields.
func (p LLMProfile) Normalize() LLMProfile {
	p.Name = strings.TrimSpace(p.Name)
	p.Model = strings.TrimSpace(p.Model)
	p.BaseURL = strings.TrimSpace(p.BaseURL)
	if p.Name == "" {
		if pr, ok := LLMProviderByID(p.Provider); ok {
			p.Name = pr.Label
			if p.Model != "" {
				p.Name += " · " + p.Model
			}
		}
	}
	return p
}

// Validate checks a normalised profile.
func (p LLMProfile) Validate() error {
	if p.Name == "" || len(p.Name) > 80 || strings.ContainsFunc(p.Name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return Invalid("name", "1-80 characters, no control characters")
	}
	if p.InputPrice < 0 || p.InputPrice > MaxLLMPrice {
		return Invalid("inputPrice", "USD 0 (unknown) to 1 000 000 per million tokens")
	}
	if p.OutputPrice < 0 || p.OutputPrice > MaxLLMPrice {
		return Invalid("outputPrice", "USD 0 (unknown) to 1 000 000 per million tokens")
	}
	return p.LLMConfig.Validate()
}

// LLMSettings are the reviewer's switch and the chosen profile.
type LLMSettings struct {
	Enabled bool   // review every proposed trade
	Active  string // the profile in use ("" = none)
}

// LLMPurpose says why an LLM call was made.
type LLMPurpose string

// LLM call purposes.
const (
	LLMPurposeAgent   LLMPurpose = "agent"   // the agent's review of a trade (4.10 step 5)
	LLMPurposeScanner LLMPurpose = "scanner" // the scanner's check before the watchlist
	LLMPurposeTest    LLMPurpose = "test"    // the admin's test on the LLM page
)

// Valid reports whether u is a known purpose.
func (u LLMPurpose) Valid() bool {
	return u == LLMPurposeAgent || u == LLMPurposeScanner || u == LLMPurposeTest
}

// MaxLLMText bounds a stored prompt or response (32 KiB); longer ones are cut by Truncate.
const MaxLLMText = 32 << 10

// LLMCall is one request to an LLM reviewer: what was sent, what came back, what it used and
// cost. The reviewer adapter fills the provider's side (Provider, Model, Prompt, Response,
// tokens, Latency, Error), even when the call fails; the caller fills the rest. It never
// holds the API key or any request header.
type LLMCall struct {
	ID        string
	AccountID string // "" = not for an account (the admin's test)
	Purpose   LLMPurpose
	ProfileID string
	Provider  LLMProviderID
	// Model is the model that answered (Anthropic's server-side fallback may serve another
	// one than asked for); the requested one when there was no answer.
	Model string
	// Prompt is the request body as sent; Response the raw response body (both cut at
	// MaxLLMText by the repository).
	Prompt       string
	Response     string
	InputTokens  int64
	OutputTokens int64
	Latency      time.Duration
	// CostMicros is the cost in USD micros (LLMProfile.Cost); CostUnknown = -1.
	CostMicros Micros
	Error      string // "" = the call returned a usable answer
	At         time.Time
}

// Truncate cuts s to at most n bytes of valid UTF-8: invalid bytes become U+FFFD first, and
// a cut never splits a character. A cut string ends with "…" within the n bytes.
func Truncate(s string, n int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= n {
		return s
	}
	const mark = "…"
	if n < len(mark) {
		return ""
	}
	cut := n - len(mark)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + mark
}

// LLMRetentionDays is how long decisions and LLM calls (prompt and response included) are
// kept (OD12), like equity snapshots.
const LLMRetentionDays = 400

// LLMUsage sums the LLM calls of one profile and purpose over a period.
type LLMUsage struct {
	ProfileID    string
	Purpose      LLMPurpose
	Calls        int64
	Failed       int64 // calls with an error
	InputTokens  int64
	OutputTokens int64
	// Cost sums the calls whose cost is known; CostUnknownCalls counts the others (a profile
	// without prices, OD13). The total is known only when CostUnknownCalls is 0.
	Cost             Micros
	CostUnknownCalls int64
}

// CostKnown reports whether every call's cost is known.
func (u LLMUsage) CostKnown() bool { return u.CostUnknownCalls == 0 }

// Add folds o into u (saturating, so a sum never wraps).
func (u LLMUsage) Add(o LLMUsage) LLMUsage {
	u.Calls = satAdd(u.Calls, o.Calls)
	u.Failed = satAdd(u.Failed, o.Failed)
	u.InputTokens = satAdd(u.InputTokens, o.InputTokens)
	u.OutputTokens = satAdd(u.OutputTokens, o.OutputTokens)
	u.Cost = Micros(satAdd(int64(u.Cost), int64(o.Cost)))
	u.CostUnknownCalls = satAdd(u.CostUnknownCalls, o.CostUnknownCalls)
	return u
}

// satAdd adds two non-negative counts, capped at math.MaxInt64.
func satAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}
