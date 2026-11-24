package domain

import (
	"strings"
	"unicode"
)

// CredentialSpec describes one credential of a platform account for the API and the UI —
// never its value. Every credential is write-only and stored encrypted under
// <prefix>.<account id>.<Key> (AccountCredentialName).
type CredentialSpec struct {
	Key       string // the suffix of the stored name, e.g. "api_key"
	Label     string
	Help      string
	Multiline bool // a PEM key: line breaks are kept
	Optional  bool // the account works without it
	SetByFlow bool // set by the platform's sign-in (Saxo's "Connect"), never typed in
}

// Credential keys of Saxo and Tiger accounts (the eToro and Trading 212 keys predate these
// and keep their names so stored secrets keep working).
const (
	SaxoAppKey       = "app_key"
	SaxoAppSecret    = "app_secret"
	SaxoRefreshToken = "refresh_token"
	SaxoDevToken     = "dev_token"

	TigerID         = "tiger_id"
	TigerAccount    = "account"
	TigerPrivateKey = "private_key"
	TigerLicense    = "license"
)

// PlatformCredentials lists the credentials an account on p has, in display order. The
// first two are the ones the older apiKey/userKey API fields set.
func PlatformCredentials(p Platform) []CredentialSpec {
	switch p {
	case PlatformTrading212:
		return []CredentialSpec{
			{Key: "api_key", Label: "API key", Help: "Trading 212 app → Settings → API (Beta) → Generate API key, with read permissions only (account, portfolio)."},
			{Key: "api_secret", Label: "API secret", Help: "Shown once next to the API key."},
		}
	case PlatformSaxo:
		return []CredentialSpec{
			{Key: SaxoAppKey, Label: "App key", Help: "developer.saxo → Application management → your Code Flow app (a SIM app for a demo account, a Live app for a real one; Live apps need Saxo's approval)."},
			{Key: SaxoAppSecret, Label: "App secret", Help: "The app's secret, next to its app key."},
			{Key: SaxoRefreshToken, Label: "Refresh token", Help: "Set by “Connect to Saxo”; renewed automatically.", SetByFlow: true, Optional: true},
			{Key: SaxoDevToken, Label: "24-hour token (SIM only)", Help: "Instead of connecting: developer.saxo → Get a 24-hour token. Demo (SIM) accounts only; it stops working after 24 hours.", Optional: true},
		}
	case PlatformTiger:
		return []CredentialSpec{
			{Key: TigerID, Label: "Tiger ID", Help: "Tiger developer console (Open API): your tiger_id."},
			{Key: TigerAccount, Label: "Account number", Help: "The account to show (a paper account number for a demo account). Tiger paper accounts have no Australian market."},
			{Key: TigerPrivateKey, Label: "RSA private key (PEM)", Help: "Generate an RSA key pair, upload the public key in the Tiger developer console and paste the private key here (PKCS#8 or PKCS#1 PEM).", Multiline: true},
			{Key: TigerLicense, Label: "Licence", Help: "Optional (e.g. TBHK, TBSG). Stored for reference only: cactusTrade always uses Tiger's fixed gateway.", Optional: true},
		}
	}
	return []CredentialSpec{
		{Key: "api_key", Label: "Public API key", Help: "eToro: Settings → Trading → API Key Management → Create New Key — the Public API Key (x-api-key)."},
		{Key: "user_key", Label: "User key", Help: "The User Key (x-user-key) of the same key."},
	}
}

// CredentialSpecFor finds the credential key of platform p.
func CredentialSpecFor(p Platform, key string) (CredentialSpec, bool) {
	for _, c := range PlatformCredentials(p) {
		if c.Key == key {
			return c, true
		}
	}
	return CredentialSpec{}, false
}

func credentialPrefix(p Platform) string {
	switch p {
	case PlatformTrading212:
		return "t212"
	case PlatformSaxo:
		return "saxo"
	case PlatformTiger:
		return "tiger"
	}
	return "etoro"
}

// AccountCredentialName is the stored name of credential key of account a; ok is false for
// a key the platform does not have. The deployment's environment account (EnvKeys) uses
// ETORO_DEMO_PUB / ETORO_DEMO_PK.
func AccountCredentialName(a Account, key string) (CredentialName, bool) {
	p := a.OnPlatform()
	if _, ok := CredentialSpecFor(p, key); !ok {
		return "", false
	}
	if p == PlatformEtoro && a.EnvKeys {
		if key == "api_key" {
			return CredEtoroDemoAPIKey, true
		}
		return CredEtoroDemoUserKey, true
	}
	return CredentialName(credentialPrefix(p) + "." + a.ID + "." + key), true
}

// CredentialsComplete reports whether the credentials set (set(key) is true when the key
// is stored) are enough to read an account on p: a Saxo account needs a refresh token from
// "Connect" (with its app key and secret) or a 24-hour developer token; every other
// platform needs all its non-optional credentials.
func CredentialsComplete(p Platform, set func(key string) bool) bool {
	if p == PlatformSaxo {
		return set(SaxoDevToken) || (set(SaxoAppKey) && set(SaxoAppSecret) && set(SaxoRefreshToken))
	}
	for _, c := range PlatformCredentials(p) {
		if !c.Optional && !set(c.Key) {
			return false
		}
	}
	return true
}

// NormalizeCredentialFor normalizes a value for spec: a multi-line credential (a PEM key)
// keeps its line breaks (CRLF becomes LF) and may contain tabs; any other credential goes
// through NormalizeCredential.
func NormalizeCredentialFor(spec CredentialSpec, v string) (string, error) {
	if !spec.Multiline {
		return NormalizeCredential(v)
	}
	v = strings.TrimSpace(strings.ReplaceAll(v, "\r\n", "\n"))
	if v == "" {
		return "", Invalid("value", "must not be empty")
	}
	if len(v) > MaxCredentialLen {
		return "", Invalid("value", "too long")
	}
	for _, r := range v {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", Invalid("value", "must not contain control characters")
		}
	}
	return v, nil
}
