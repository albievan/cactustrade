package domain

import (
	"net/url"
	"strings"
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
	return p.LLMConfig.Validate()
}

// LLMSettings are the reviewer's switch and the chosen profile.
type LLMSettings struct {
	Enabled bool   // review every proposed trade
	Active  string // the profile in use ("" = none)
}
