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

// ClampVerdict enforces that the reviewer can only approve, shrink or reject: an unknown
// kind is a rejection, a multiplier is clamped to (0, 1], and a "reduce" to nothing is a
// rejection.
func ClampVerdict(v Verdict) Verdict {
	switch v.Kind {
	case VerdictApprove:
		v.SizeMultiplier = 1
	case VerdictReduce:
		if !(v.SizeMultiplier > 0) { // also catches NaN
			return Verdict{Kind: VerdictReject, Reason: "reduce to nothing: " + v.Reason}
		}
		v.SizeMultiplier = min(v.SizeMultiplier, 1)
	default:
		return Verdict{Kind: VerdictReject, Reason: v.Reason}
	}
	return v
}
