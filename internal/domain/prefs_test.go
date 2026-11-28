package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestValidatePrefKey(t *testing.T) {
	for _, tc := range []struct {
		key string
		ok  bool
	}{
		{"dashboard.liveInvest.range", true},
		{"infoCard.risk-states.open", true},
		{"orders.table.sort", true},
		{"a.b", true},
		{"a.b.c.d.e.f.g.h", true},    // seven parts after the first
		{"a.b.c.d.e.f.g.h.i", false}, // eight
		{"dashboard", false},         // one part only
		{"Dashboard.range", false},   // starts upper case
		{"1dash.range", false},       // starts with a digit
		{"dash..range", false},       // empty part
		{"dash.range.", false},       // trailing dot
		{"dash.ran ge", false},       // space
		{"dash.range/x", false},      // slash
		{"dash.ränge", false},        // non-ASCII
		{"", false},                  // empty
		{"a." + strings.Repeat("x", MaxPrefKeyLen), false},
	} {
		err := ValidatePrefKey(tc.key)
		if (err == nil) != tc.ok {
			t.Errorf("ValidatePrefKey(%q) = %v, want ok %v", tc.key, err, tc.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidatePrefKey(%q) = %v, not ErrInvalid", tc.key, err)
		}
	}
}

func TestValidatePrefValue(t *testing.T) {
	long := `"` + strings.Repeat("x", MaxPrefStringLen+1) + `"`
	many := "[" + strings.Repeat("1,", MaxPrefItems) + "1]"
	var members []string
	for i := range MaxPrefItems + 1 {
		members = append(members, `"m`+strings.Repeat("x", i)+`":1`)
	}
	manyObj := "{" + strings.Join(members, ",") + "}"
	for _, tc := range []struct {
		name, raw string
		ok        bool
	}{
		{"string", `"max"`, true},
		{"number", `30`, true},
		{"float", `1.5`, true},
		{"true", `true`, true},
		{"false", `false`, true},
		{"array of scalars", `["a",1,true]`, true},
		{"empty array", `[]`, true},
		{"flat object", `{"key":"time","desc":true}`, true},
		{"empty object", `{}`, true},
		{"null", `null`, false},
		{"nested array", `[[1]]`, false},
		{"object in array", `[{"a":1}]`, false},
		{"nested object", `{"a":{"b":1}}`, false},
		{"null in array", `[null]`, false},
		{"long string", long, false},
		{"long string in array", "[" + long + "]", false},
		{"too many items", many, false},
		{"too many members", manyObj, false},
		{"not JSON", `max`, false},
		{"two values", `1 2`, false},
		{"bad UTF-8", "\"\xff\"", false},
	} {
		err := ValidatePrefValue("x.y", json.RawMessage(tc.raw))
		if (err == nil) != tc.ok {
			t.Errorf("%s: ValidatePrefValue(%.60s) = %v, want ok %v", tc.name, tc.raw, err, tc.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v is not ErrInvalid", tc.name, err)
		}
	}
}

func TestMergePreferences(t *testing.T) {
	cur := Preferences{"dashboard.liveInvest.range": json.RawMessage(`"day"`), "orders.feed.side": json.RawMessage(`"long"`)}
	for _, tc := range []struct {
		name  string
		patch map[string]json.RawMessage
		want  string // encoded result; "" = refused
	}{
		{
			"set and replace (last write wins)",
			map[string]json.RawMessage{"dashboard.liveInvest.range": json.RawMessage(` "max" `)},
			`{"dashboard.liveInvest.range":"max","orders.feed.side":"long"}`,
		},
		{
			"null deletes",
			map[string]json.RawMessage{"orders.feed.side": json.RawMessage(`null`)},
			`{"dashboard.liveInvest.range":"day"}`,
		},
		{
			"deleting a missing key is fine",
			map[string]json.RawMessage{"nope.key": json.RawMessage(`null`)},
			`{"dashboard.liveInvest.range":"day","orders.feed.side":"long"}`,
		},
		{
			"compacted",
			map[string]json.RawMessage{"orders.table.sort": json.RawMessage("{ \"key\" : \"time\",\n \"desc\": true }")},
			`{"dashboard.liveInvest.range":"day","orders.feed.side":"long","orders.table.sort":{"key":"time","desc":true}}`,
		},
		{"empty patch", map[string]json.RawMessage{}, `{"dashboard.liveInvest.range":"day","orders.feed.side":"long"}`},
		{"bad key refuses the whole patch", map[string]json.RawMessage{"ok.key": json.RawMessage(`1`), "Bad": json.RawMessage(`1`)}, ""},
		{"bad value refuses the whole patch", map[string]json.RawMessage{"ok.key": json.RawMessage(`1`), "bad.value": json.RawMessage(`[[1]]`)}, ""},
	} {
		got, err := MergePreferences(cur, tc.patch)
		if tc.want == "" {
			if err == nil || !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", tc.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if enc, _ := got.Encode(); enc != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, enc, tc.want)
		}
	}
	if len(cur) != 2 || string(cur["dashboard.liveInvest.range"]) != `"day"` {
		t.Fatalf("cur changed: %v", cur)
	}
}

func TestMergePreferencesCaps(t *testing.T) {
	// MaxPrefsKeys keys fit, one more does not.
	p := Preferences{}
	patch := map[string]json.RawMessage{}
	for i := range MaxPrefsKeys {
		patch["k.n"+strings.Repeat("x", i%50)+string(rune('a'+i/50))] = json.RawMessage(`1`)
	}
	p, err := MergePreferences(p, patch)
	if err != nil || len(p) != MaxPrefsKeys {
		t.Fatalf("%d keys: %v", len(p), err)
	}
	if _, err := MergePreferences(p, map[string]json.RawMessage{"k.one.more": json.RawMessage(`1`)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("key cap: %v", err)
	}
	// replacing and deleting at the cap still work
	if _, err := MergePreferences(p, map[string]json.RawMessage{"k.one.more": json.RawMessage(`1`), "k.na": json.RawMessage(`null`)}); err != nil {
		t.Fatalf("delete one, add one at the cap: %v", err)
	}

	// the byte cap: 60 strings of 1 KiB fit, more do not
	big := json.RawMessage(`"` + strings.Repeat("é", MaxPrefStringLen/2) + `"`) // 1024 bytes of 2-byte runes
	q := Preferences{}
	var refused bool
	for i := range 80 {
		next, err := MergePreferences(q, map[string]json.RawMessage{"big.n" + strings.Repeat("x", i): big})
		if err != nil {
			if !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
			refused = true
			break
		}
		q = next
	}
	enc, _ := q.Encode()
	if !refused || len(enc) > MaxPrefsBytes || len(enc) < MaxPrefsBytes-2*MaxPrefStringLen {
		t.Fatalf("byte cap: refused %v, %d bytes", refused, len(enc))
	}
}

func TestParsePreferences(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		fail bool
	}{
		{"", 0, false},
		{"null", 0, false},
		{"{}", 0, false},
		{`{"a.b":"x","c.d":[1,2]}`, 2, false},
		{"[1]", 0, true},
		{"{", 0, true},
	} {
		p, err := ParsePreferences(tc.in)
		if (err != nil) != tc.fail || len(p) != tc.n || p == nil {
			t.Errorf("ParsePreferences(%q) = %v %v", tc.in, p, err)
		}
	}
	if s, err := Preferences(nil).Encode(); err != nil || s != "{}" {
		t.Fatalf("nil encodes as %q %v", s, err)
	}
}
