package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// personalKeys are field names (lower case) that identify the account or its holder: their
// values become 0 (numbers) or "redacted" (anything else). Any key ending in "cid" (eToro's
// customer ids: cid, gcid, realCid, demoCid, parentCid…) counts too.
var personalKeys = map[string]bool{
	"accountid": true, "gcid": true, "cid": true, "username": true, "userid": true, "customerid": true,
	"clientid": true, "firstname": true, "lastname": true, "fullname": true,
	"email": true, "emailaddress": true, "phone": true, "phonenumber": true, "address": true, "ip": true,
	"ipaddress": true, "dateofbirth": true, "birthdate": true, "nickname": true, "avatar": true,
}

// secretFragments mark a token-looking field name: its value is always redacted.
var secretFragments = []string{"token", "apikey", "userkey", "secret", "password", "authorization", "sessionid", "cookie", "signature", "credential"}

var (
	jwtLike   = regexp.MustCompile(`^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*$`)
	opaqueKey = regexp.MustCompile(`^[A-Za-z0-9+/_=-]{32,}$`)
	uuidLike  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// sensitiveKey reports whether a field's value must be replaced, and whether it is personal
// (a number becomes 0) rather than a secret (always "redacted").
func sensitiveKey(k string) (redact, personal bool) {
	l := strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(k))
	if personalKeys[l] || strings.HasSuffix(l, "cid") {
		return true, true
	}
	for _, f := range secretFragments {
		if strings.Contains(l, f) {
			return true, false
		}
	}
	return false, false
}

// tokenLike reports whether a string value looks like a token or key whatever its field is
// called: a JWT, or a long opaque run of base64/hex characters that is not a UUID (our
// request ids and eToro's order tokens are UUIDs and say nothing about the account).
func tokenLike(s string) bool {
	return jwtLike.MatchString(s) || (opaqueKey.MatchString(s) && !uuidLike.MatchString(s))
}

// sanitizeJSON returns the answer with every personal field (account id, gcid, cid, user
// name…) set to 0 or "redacted" and every token-looking field or value set to "redacted",
// indented. Numbers keep their exact text (json.Number), so prices stay as eToro sent them.
// An answer that is not JSON is an error: it is never written as is.
func sanitizeJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("sanitise: not JSON: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("sanitise: more than one JSON value")
	}
	out, err := json.MarshalIndent(scrub(v), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("sanitise: %w", err)
	}
	return append(out, '\n'), nil
}

// scrub walks a decoded JSON value and replaces what sanitizeJSON promises to replace.
func scrub(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			redact, personal := sensitiveKey(k)
			switch {
			case !redact:
				x[k] = scrub(val)
			case val == nil:
			case personal:
				if _, isNum := val.(json.Number); isNum {
					x[k] = json.Number("0")
				} else {
					x[k] = "redacted"
				}
			default:
				x[k] = "redacted"
			}
		}
		return x
	case []any:
		for i := range x {
			x[i] = scrub(x[i])
		}
		return x
	case string:
		if tokenLike(x) {
			return "redacted"
		}
	}
	return v
}
