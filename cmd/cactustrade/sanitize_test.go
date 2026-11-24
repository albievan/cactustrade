package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSanitizeJSON: keys named accountId, gcid, cid, username (any case, at any depth, in
// arrays too) lose their values, every token-looking field or value is redacted, and
// everything else, numbers included, is kept exactly.
func TestSanitizeJSON(t *testing.T) {
	in := `{
	  "accountId": 123456789, "gcid": 22334455, "CID": "998877", "realCid": 4455, "UserName": "jane.doe",
	  "user_name": "jane", "token": "ad74d0fd-ca92-4974-a932-9668d29ec366", "accessToken": "abc",
	  "nextPageToken": null, "x-api-key": "k", "apiKey": "k2", "password": "p",
	  "note": "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl",
	  "blob": "QWxhZGRpbjpvcGVuIHNlc2FtZUFsYWRkaW46b3BlbiBzZXNhbWU=",
	  "referenceId": "8a3e2f4c-1b6d-4c3e-9f2a-7d5e6b4c3a21",
	  "displayName": "Example plc", "bid": 245.30, "units": 0.362279, "isPotential": false,
	  "positions": [{"positionId": 3649965576, "accountId": 1, "nested": {"gcid": 7, "cid": [1, 2]}}]
	}`
	out, err := sanitizeJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	dec := json.NewDecoder(strings.NewReader(string(out)))
	dec.UseNumber()
	if err := dec.Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"accountId": "0", "gcid": "0", "CID": "redacted", "realCid": "0", "UserName": "redacted", "user_name": "redacted",
		"token": "redacted", "accessToken": "redacted", "x-api-key": "redacted", "apiKey": "redacted", "password": "redacted",
		"note": "redacted", "blob": "redacted",
		"referenceId": "8a3e2f4c-1b6d-4c3e-9f2a-7d5e6b4c3a21", "displayName": "Example plc", "bid": "245.30", "units": "0.362279",
	}
	for k, v := range want {
		if s := toString(got[k]); s != v {
			t.Errorf("%s = %q, want %q", k, s, v)
		}
	}
	if got["nextPageToken"] != nil || got["isPotential"] != false {
		t.Errorf("nextPageToken %v, isPotential %v", got["nextPageToken"], got["isPotential"])
	}
	pos := got["positions"].([]any)[0].(map[string]any)
	nested := pos["nested"].(map[string]any)
	if toString(pos["positionId"]) != "3649965576" || toString(pos["accountId"]) != "0" || toString(nested["gcid"]) != "0" || nested["cid"] != "redacted" {
		t.Errorf("nested: %v", pos)
	}
	for _, leak := range []string{"123456789", "22334455", "998877", "jane", "eyJ", "QWxhZGRp", `"k"`, `"p"`, `"abc"`} {
		if strings.Contains(string(out), leak) {
			t.Errorf("sanitised output still holds %s:\n%s", leak, out)
		}
	}
	for _, bad := range []string{``, `not json`, `{"a":1} {"b":2}`} {
		if _, err := sanitizeJSON([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}
