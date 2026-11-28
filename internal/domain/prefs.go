package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"unicode/utf8"
)

// Preferences are one user's own view choices in the web app (R50): which chart range,
// tab, filter, sort order or folded card the user last picked. They follow the user to any
// browser. They are never an input to trading, risk or authorization: nothing but the web
// app reads them.
//
// Each value is compact JSON of an allowed shape (see ValidatePrefValue).
type Preferences map[string]json.RawMessage

// Bounds of a user's preferences. The stored JSON must fit MariaDB's TEXT column (65 535
// bytes) of the settings table, so the cap is 60 KiB rather than 64.
const (
	// MaxPrefsBytes caps the stored JSON object of one user's preferences.
	MaxPrefsBytes = 60 << 10
	// MaxPrefsKeys caps how many preferences one user keeps.
	MaxPrefsKeys = 200
	// MaxPrefKeyLen caps a key's length in bytes.
	MaxPrefKeyLen = 128
	// MaxPrefStringLen caps a string value (or a string inside an array or object) in bytes.
	MaxPrefStringLen = 1024
	// MaxPrefItems caps the elements of an array value or the members of an object value.
	MaxPrefItems = 100
)

// prefKeyPattern: a page or component name, then one to seven dot-separated parts, e.g.
// "dashboard.liveInvest.range" or "infoCard.risk-states.open".
var prefKeyPattern = regexp.MustCompile(`^[a-z][A-Za-z0-9]*(\.[A-Za-z0-9_-]+){1,7}$`)

// ValidatePrefKey checks a preference key against the strict pattern.
func ValidatePrefKey(k string) error {
	if len(k) > MaxPrefKeyLen || !prefKeyPattern.MatchString(k) {
		return Invalid("preferences", fmt.Sprintf("key %.40q is not a preference name (letters, digits, dots, - and _; e.g. dashboard.liveInvest.range)", k))
	}
	return nil
}

// ValidatePrefValue checks one value: a string, a number, true/false, an array of at most
// MaxPrefItems such scalars, or an object of at most MaxPrefItems such scalars (one level,
// e.g. a table's filters or sort). null is not a value (it deletes the key in a patch).
func ValidatePrefValue(k string, raw json.RawMessage) error {
	if !utf8.Valid(raw) {
		return Invalid("preferences", fmt.Sprintf("%s: not UTF-8", k))
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return Invalid("preferences", fmt.Sprintf("%s: not JSON", k))
	}
	if dec.More() {
		return Invalid("preferences", fmt.Sprintf("%s: one JSON value expected", k))
	}
	bad := func(why string) error { return Invalid("preferences", fmt.Sprintf("%s: %s", k, why)) }
	switch x := v.(type) {
	case nil:
		return bad("null is not a value")
	case []any:
		if len(x) > MaxPrefItems {
			return bad(fmt.Sprintf("at most %d items", MaxPrefItems))
		}
		for _, e := range x {
			if err := prefScalar(e); err != nil {
				return bad(err.Error())
			}
		}
	case map[string]any:
		if len(x) > MaxPrefItems {
			return bad(fmt.Sprintf("at most %d members", MaxPrefItems))
		}
		for mk, e := range x {
			if len(mk) > MaxPrefStringLen {
				return bad("a member name is too long")
			}
			if err := prefScalar(e); err != nil {
				return bad(err.Error())
			}
		}
	default:
		if err := prefScalar(x); err != nil {
			return bad(err.Error())
		}
	}
	return nil
}

// prefScalar accepts a string (bounded), a number or a bool.
func prefScalar(v any) error {
	switch x := v.(type) {
	case string:
		if len(x) > MaxPrefStringLen {
			return fmt.Errorf("a text is longer than %d bytes", MaxPrefStringLen)
		}
		return nil
	case json.Number, bool:
		return nil
	default:
		return fmt.Errorf("only text, numbers, true/false, and small lists or objects of them")
	}
}

// ParsePreferences reads stored preferences. Anything that does not parse as an object is
// reported (the caller starts from empty preferences: they are view choices only).
func ParsePreferences(stored string) (Preferences, error) {
	out := Preferences{}
	if stored == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(stored), &out); err != nil {
		return Preferences{}, fmt.Errorf("domain.ParsePreferences: %w", err)
	}
	if out == nil { // the stored text was "null"
		out = Preferences{}
	}
	return out, nil
}

// Encode is the stored form: one compact JSON object, keys sorted.
func (p Preferences) Encode() (string, error) {
	if p == nil {
		p = Preferences{}
	}
	b, err := json.Marshal(map[string]json.RawMessage(p))
	if err != nil {
		return "", fmt.Errorf("domain.Preferences.Encode: %w", err)
	}
	return string(b), nil
}

// MergePreferences applies a JSON merge patch of keys to cur (RFC 7396, one level): a value
// sets its key (last write wins), JSON null deletes it. Every key and value of the patch is
// validated, and the result must stay within MaxPrefsKeys and MaxPrefsBytes. cur is not
// changed; nothing is applied when anything is refused.
func MergePreferences(cur Preferences, patch map[string]json.RawMessage) (Preferences, error) {
	out := maps.Clone(cur)
	if out == nil {
		out = Preferences{}
	}
	for k, raw := range patch {
		if err := ValidatePrefKey(k); err != nil {
			return nil, err
		}
		t := bytes.TrimSpace(raw)
		if bytes.Equal(t, []byte("null")) || len(t) == 0 {
			delete(out, k)
			continue
		}
		if err := ValidatePrefValue(k, t); err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, t); err != nil {
			return nil, Invalid("preferences", fmt.Sprintf("%s: not JSON", k))
		}
		out[k] = json.RawMessage(buf.Bytes())
	}
	if len(out) > MaxPrefsKeys {
		return nil, Invalid("preferences", fmt.Sprintf("at most %d preferences", MaxPrefsKeys))
	}
	enc, err := out.Encode()
	if err != nil {
		return nil, Invalid("preferences", "cannot be stored")
	}
	if len(enc) > MaxPrefsBytes {
		return nil, Invalid("preferences", fmt.Sprintf("at most %d KiB together", MaxPrefsBytes>>10))
	}
	return out, nil
}
