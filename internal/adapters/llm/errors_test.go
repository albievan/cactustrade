package llm

import (
	"net/http"
	"strings"
	"testing"
)

func TestProviderMessage(t *testing.T) {
	gemini := `[{"error":{"code":429,"message":"You exceeded your current quota,\n please check your plan","status":"RESOURCE_EXHAUSTED"}}]`
	cases := []struct {
		status     int
		body, want string
	}{
		{http.StatusTooManyRequests, gemini, "quota or rate limit for this key is used up"},
		{http.StatusTooManyRequests, gemini, "You exceeded your current quota, please check your plan"},
		{http.StatusUnauthorized, `{"error":{"message":"Incorrect API key"}}`, "refused the API key"},
		{http.StatusForbidden, `{"error":{"message":"no"}}`, "refused the API key"},
		{http.StatusPaymentRequired, `{"error":{"message":"Insufficient credits"}}`, "wants payment"},
		{http.StatusNotFound, `{"error":{"message":"model not found"}}`, "Load models"},
		{http.StatusInternalServerError, "plain text failure", "plain text failure"},
		{http.StatusBadRequest, strings.Repeat("x", 400), "…"},
	}
	for _, c := range cases {
		if got := providerMessage(c.status, []byte(c.body)); !strings.Contains(got, c.want) {
			t.Errorf("%d %q → %q, want %q", c.status, c.body[:min(30, len(c.body))], got, c.want)
		}
	}
}
