package httpapi

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

// TestPreferencesEndpoints: R50 — the caller's own view preferences, CSRF on writes, the
// caps, API keys refused, another user's preferences unreachable.
func TestPreferencesEndpoints(t *testing.T) {
	x := newTC(t, nil)
	x.setupAndLogin()
	csrf := []string{"X-CSRF-Token", x.csrf}

	if code, p, _ := x.do("GET", "/api/v1/me/preferences", ""); code != 200 || len(p) != 0 {
		t.Fatalf("empty %d %v", code, p)
	}
	// CSRF: missing, wrong, cross-origin
	body := `{"dashboard.liveInvest.range":"max","orders.feed.side":"long"}`
	for _, hdr := range [][]string{nil, {"X-CSRF-Token", "nope"}, {"X-CSRF-Token", x.csrf, "Origin", "https://evil.example"}} {
		if code, _, _ := x.do("PUT", "/api/v1/me/preferences", body, hdr...); code != 403 {
			t.Fatalf("csrf %v: %d", hdr, code)
		}
	}
	if code, p, _ := x.do("PUT", "/api/v1/me/preferences", body, csrf...); code != 200 || p["dashboard.liveInvest.range"] != "max" || p["orders.feed.side"] != "long" {
		t.Fatalf("set %d %v", code, p)
	}
	// merge: null deletes, others kept, objects of scalars allowed
	if code, p, _ := x.do("PUT", "/api/v1/me/preferences", `{"orders.feed.side":null,"orders.table.sort":{"key":"time","desc":true}}`, csrf...); code != 200 ||
		p["orders.feed.side"] != nil || p["dashboard.liveInvest.range"] != "max" || p["orders.table.sort"].(map[string]any)["desc"] != true {
		t.Fatalf("patch %d %v", code, p)
	}
	if code, p, _ := x.do("GET", "/api/v1/me/preferences", ""); code != 200 || len(p) != 2 || p["dashboard.liveInvest.range"] != "max" {
		t.Fatalf("read back %d %v", code, p)
	}
	// refusals: bad key, nested value, not an object, too big (body cap 413, stored cap 400)
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"../other":"x"}`, 400},
		{`{"a.b":{"c":{"d":1}}}`, 400},
		{`["a.b"]`, 400},
		{`null`, 400},
		{`{"a.b":"` + strings.Repeat("x", 70<<10) + `"}`, 413},
	} {
		if code, p, _ := x.do("PUT", "/api/v1/me/preferences", tc.body, csrf...); code != tc.code {
			t.Fatalf("%.40s: %d %v", tc.body, code, p)
		}
	}
	// the stored cap: 1 KiB values until the 60 KiB total is reached
	val := `"` + strings.Repeat("y", 1000) + `"`
	refused := false
	for i := range 10 { // ten 1 KiB values per patch
		var kv []string
		for j := range 10 {
			kv = append(kv, `"fill.k`+strings.Repeat("x", i)+`.n`+strings.Repeat("y", j)+`":`+val)
		}
		code, p, _ := x.do("PUT", "/api/v1/me/preferences", "{"+strings.Join(kv, ",")+"}", csrf...)
		if code == 400 && strings.Contains(p["detail"].(string), "60 KiB") {
			refused = true
			break
		}
		if code != 200 {
			t.Fatalf("fill %d: %d %v", i, code, p)
		}
	}
	if !refused {
		t.Fatal("no size cap")
	}
	if code, p, _ := x.do("GET", "/api/v1/me/preferences", ""); code != 200 || p["dashboard.liveInvest.range"] != "max" {
		t.Fatalf("a refused patch changed something: %d", code)
	}

	// another user: starts empty, cannot read or change the admin's, and the admin's stay
	if code, _, _ := x.do("POST", "/api/v1/users", `{"username":"olga","password":"olga password!!","role":"viewer"}`, csrf...); code != 201 {
		t.Fatalf("create user %d", code)
	}
	admin := x.c.Jar
	x.c.Jar, _ = cookiejar.New(nil)
	x.login("olga", "olga password!!")
	if code, p, _ := x.do("GET", "/api/v1/me/preferences", ""); code != 200 || len(p) != 0 {
		t.Fatalf("olga sees %d %v", code, p)
	}
	if code, _, _ := x.do("PUT", "/api/v1/me/preferences", `{"dashboard.liveInvest.range":"day"}`, "X-CSRF-Token", x.csrf); code != 200 {
		t.Fatalf("olga set %d", code)
	}
	// there is no path to someone else's preferences
	for _, path := range []string{"/api/v1/me/preferences?userId=admin", "/api/v1/users/admin/preferences"} {
		code, p, _ := x.do("GET", path, "")
		if code == 200 && p["dashboard.liveInvest.range"] == "max" {
			t.Fatalf("%s reached the admin's preferences", path)
		}
	}
	x.c.Jar = admin
	if code, p, _ := x.do("GET", "/api/v1/me/preferences", ""); code != 200 || p["dashboard.liveInvest.range"] != "max" {
		t.Fatalf("admin's changed by olga: %d %v", code, p)
	}

	// an API key is refused (web app only)
	code, m, _ := x.do("POST", "/api/v1/api-keys", `{"name":"ci","role":"viewer","expiresInDays":30}`, csrf...)
	if code != 201 {
		t.Fatalf("create key %d %v", code, m)
	}
	secret := m["secret"].(string)
	anon := &http.Client{Transport: x.c.Transport}
	for _, method := range []string{"GET", "PUT"} {
		var rd *strings.Reader
		if method == "PUT" {
			rd = strings.NewReader(`{"a.b":1}`)
		} else {
			rd = strings.NewReader("")
		}
		req, _ := http.NewRequestWithContext(context.Background(), method, x.srv.URL+"/api/v1/me/preferences", rd)
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/json")
		resp, err := anon.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Fatalf("API key %s: %d", method, resp.StatusCode)
		}
	}
	// signed out: 401
	if code := x.anon("GET", "/api/v1/me/preferences", ""); code != 401 {
		t.Fatalf("anonymous %d", code)
	}
}
