package domain

import (
	"strings"
	"testing"
)

func TestPKCEChallenge(t *testing.T) {
	// RFC 7636 Appendix B.
	if got := PKCEChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("challenge = %q", got)
	}
}

func TestValidPKCEVerifier(t *testing.T) {
	tests := []struct {
		v    string
		want bool
	}{
		{"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk", true},
		{strings.Repeat("a", 43), true},
		{strings.Repeat("~", 128), true},
		{strings.Repeat("a", 42), false},
		{strings.Repeat("a", 129), false},
		{strings.Repeat("a", 42) + "+", false},
		{strings.Repeat("a", 42) + "/", false},
		{strings.Repeat("a", 42) + "=", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ValidPKCEVerifier(tt.v); got != tt.want {
			t.Errorf("ValidPKCEVerifier(%q) = %v", tt.v, got)
		}
	}
}
