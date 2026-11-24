package domain

import (
	"crypto/sha256"
	"encoding/base64"
)

// PKCEChallenge is the S256 code challenge of an OAuth 2.0 PKCE code verifier (RFC 7636
// §4.2): BASE64URL(SHA256(ASCII(verifier))) without padding. The verifier stays with us until
// the code exchange; only the challenge goes into the browser's address (PLAN 7.1.13).
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ValidPKCEVerifier reports whether v is a code verifier RFC 7636 §4.1 accepts: 43–128
// characters from A–Z, a–z, 0–9 and "-._~".
func ValidPKCEVerifier(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for _, r := range v {
		ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' || r == '_' || r == '~'
		if !ok {
			return false
		}
	}
	return true
}
