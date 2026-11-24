package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWatchlistThemesExportImport(t *testing.T) {
	x, _ := newMarketTC(t)
	csrf := []string{"X-CSRF-Token", x.csrf}
	if code, m, _ := x.do("POST", "/api/v1/watchlist", `{"instrumentId":1}`, csrf...); code != 201 {
		t.Fatalf("add %d %v", code, m)
	}
	code, th, _ := x.do("POST", "/api/v1/themes", `{"name":"Tech","keywords":["apple"],"industryId":0,"enabled":true,"instruments":[]}`, csrf...)
	if code != 201 {
		t.Fatalf("theme %d %v", code, th)
	}
	themeID := th["id"].(string)

	// overview: the owner may edit; holdings are unknown without keys
	code, m, _ := x.do("GET", "/api/v1/watchlist/overview", "")
	hold, _ := m["holdings"].(map[string]any)
	if code != 200 || m["canEdit"] != true || hold["known"] != false || hold["error"] == nil || hold["notOnWatchlist"] == nil {
		t.Fatalf("overview %d %v", code, m)
	}

	// proposals, then accept
	code, m, _ = x.do("GET", "/api/v1/watchlist/theme-proposals", "")
	ps, _ := m["proposals"].([]any)
	if code != 200 || len(ps) != 1 || ps[0].(map[string]any)["themeName"] != "Tech" || ps[0].(map[string]any)["score"] != 3.0 {
		t.Fatalf("proposals %d %v", code, m)
	}
	if code, m, _ := x.do("POST", "/api/v1/watchlist/theme-assignments", `{"assignments":[{"instrumentId":1,"themeId":"`+themeID+`"}]}`, csrf...); code != 200 || m["changedThemes"] != 1.0 {
		t.Fatalf("assign %d %v", code, m)
	}
	var items []map[string]any
	if code := x.getJSON("/api/v1/watchlist", &items); code != 200 || len(items) != 1 || items[0]["themeId"] != themeID {
		t.Fatalf("list %d %v", code, items)
	}
	if code, _, _ := x.do("POST", "/api/v1/watchlist/theme-assignments", `{"assignments":[{"instrumentId":99,"themeId":""}]}`, csrf...); code != 404 {
		t.Fatalf("not on the watchlist %d", code)
	}

	// export: a file, no ids
	req, _ := http.NewRequestWithContext(context.Background(), "GET", x.srv.URL+"/api/v1/watchlist/export", nil)
	resp, err := x.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Disposition") != `attachment; filename="cactustrade-watchlist-2026-11-23.json"` ||
		resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("export %d %v", resp.StatusCode, resp.Header)
	}
	var file map[string]any
	if err := json.Unmarshal(body, &file); err != nil || file["version"] != 1.0 || file["account"].(map[string]any)["name"] != "Main demo" {
		t.Fatalf("file %s", body)
	}
	if strings.Contains(string(body), themeID) || strings.Contains(string(body), `"id"`) {
		t.Fatalf("the file carries ids: %s", body)
	}

	// import the file back: idempotent
	code, m, _ = x.do("POST", "/api/v1/watchlist/import", string(body), csrf...)
	if code != 200 || len(m["already"].([]any)) != 1 || len(m["themesUnchanged"].([]any)) != 1 || len(m["added"].([]any)) != 0 {
		t.Fatalf("import %d %v", code, m)
	}
	// a larger file than a plain JSON body may be (64 KiB) is fine up to 1 MiB
	padded := strings.Replace(string(body), "{", "{"+strings.Repeat(" ", 100<<10), 1)
	if code, m, _ := x.do("POST", "/api/v1/watchlist/import", padded, csrf...); code != 200 {
		t.Fatalf("100 KiB file %d %v", code, m)
	}
	tests := []struct {
		name string
		body string
		hdr  []string
		code int
	}{
		{"over 1 MiB", strings.Replace(string(body), "{", "{"+strings.Repeat(" ", 1<<20), 1), csrf, 413},
		{"bad version", `{"version":2,"exportedAt":"2026-11-23T09:00:00Z","account":{"name":"x"},"watchlist":[],"themes":[]}`, csrf, 400},
		{"unknown field", `{"version":1,"watchlist":[],"themes":[],"apiKey":"x"}`, csrf, 400},
		{"two values", `{"version":1,"watchlist":[],"themes":[]} {}`, csrf, 400},
		{"not JSON", `version=1`, csrf, 400},
		{"too many shares", `{"version":1,"watchlist":[` + strings.TrimSuffix(strings.Repeat(`{"instrumentId":1,"symbol":"A","market":"NYSE"},`, 201), ",") + `],"themes":[]}`, csrf, 400},
		{"no CSRF token", `{"version":1,"watchlist":[],"themes":[]}`, nil, 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code, m, _ := x.do("POST", "/api/v1/watchlist/import", tt.body, tt.hdr...); code != tt.code {
				t.Fatalf("%d %v, want %d", code, m, tt.code)
			}
		})
	}
	req, _ = http.NewRequestWithContext(context.Background(), "POST", x.srv.URL+"/api/v1/watchlist/import", strings.NewReader(`{"version":1}`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-CSRF-Token", x.csrf)
	if resp, err := x.c.Do(req); err != nil || resp.StatusCode != 415 {
		t.Fatalf("text/plain %v %v", resp, err)
	} else {
		resp.Body.Close()
	}

	// an API key may export but never import
	code, m, _ = x.do("POST", "/api/v1/api-keys", `{"name":"ci","role":"operator","expiresInDays":30}`, csrf...)
	if code != 201 {
		t.Fatalf("key %d %v", code, m)
	}
	secret := m["secret"].(string)
	anon := &http.Client{Transport: x.c.Transport}
	call := func(method, path, body string) int {
		req, _ := http.NewRequestWithContext(context.Background(), method, x.srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/json")
		resp, err := anon.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := call("GET", "/api/v1/watchlist/export", ""); c != 200 {
		t.Fatalf("export by key %d", c)
	}
	if c := call("POST", "/api/v1/watchlist/import", string(body)); c != 403 {
		t.Fatalf("import by key %d", c)
	}
}
