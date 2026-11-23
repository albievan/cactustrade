package domain

import (
	"strings"
	"unicode"
)

// TradingMode selects the eToro environment. Demo is the default; real needs the gate.
type TradingMode string

// Trading modes.
const (
	ModeDemo TradingMode = "demo"
	ModeReal TradingMode = "real"
)

// CredentialName names a write-only secret the owner enters in the UI. eToro binds each key
// pair to one environment, so demo and real pairs are separate.
type CredentialName string

// Credential names.
const (
	CredEtoroDemoAPIKey  CredentialName = "etoro_demo_api_key"
	CredEtoroDemoUserKey CredentialName = "etoro_demo_user_key"
	CredEtoroRealAPIKey  CredentialName = "etoro_real_api_key"
	CredEtoroRealUserKey CredentialName = "etoro_real_user_key"
	CredLLMAPIKey        CredentialName = "llm_api_key" // before LLM profiles; migrated by app.MigrateLLMSetup
)

// CredentialInfo describes a credential for the UI (never its value).
type CredentialInfo struct {
	Name  CredentialName
	Label string
}

// Credentials lists every credential the UI may set, in display order.
var Credentials = []CredentialInfo{
	{CredEtoroDemoAPIKey, "eToro demo — API key (x-api-key)"},
	{CredEtoroDemoUserKey, "eToro demo — user key (x-user-key)"},
	{CredEtoroRealAPIKey, "eToro real — API key (x-api-key)"},
	{CredEtoroRealUserKey, "eToro real — user key (x-user-key)"},
}

// CredentialEnv maps credentials that may come from the environment to their variable.
// eToro's "Public API Key" is the x-api-key and the "User Key" the x-user-key. A variable
// NAME_FILE naming a file (container secrets) works too. Set this way, the credential is
// read-only in the UI.
var CredentialEnv = map[CredentialName]string{
	CredEtoroDemoAPIKey:  "ETORO_DEMO_PUB",
	CredEtoroDemoUserKey: "ETORO_DEMO_PK",
}

// MaxCredentialLen bounds a stored secret.
const MaxCredentialLen = 4096

// ParseCredentialName validates a credential name.
func ParseCredentialName(s string) (CredentialName, error) {
	for _, c := range Credentials {
		if string(c.Name) == s {
			return c.Name, nil
		}
	}
	return "", Invalid("name", "unknown credential")
}

// EtoroKeys returns the API-key and user-key credential names for a mode.
func EtoroKeys(m TradingMode) (apiKey, userKey CredentialName) {
	if m == ModeReal {
		return CredEtoroRealAPIKey, CredEtoroRealUserKey
	}
	return CredEtoroDemoAPIKey, CredEtoroDemoUserKey
}

// NormalizeCredential trims surrounding whitespace (keys are often pasted with a newline)
// and rejects empty, over-long or control-character values.
func NormalizeCredential(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", Invalid("value", "must not be empty")
	}
	if len(v) > MaxCredentialLen {
		return "", Invalid("value", "too long")
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return "", Invalid("value", "must not contain control characters")
		}
	}
	return v, nil
}
