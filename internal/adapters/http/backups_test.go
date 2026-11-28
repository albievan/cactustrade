package httpapi

import (
	"log/slog"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

const testRecipient = "age1q73he0q5yzfu3d64msd3p6rvksnrwjk3d2598mgtmlqt9wrdr37q2vrn72"

// TestBackupEndpoints covers R48's routes: CSRF on writes, OD29 (no recipient, no backup),
// bad input, the download's headers and bytes, a bad file name, API keys refused for the
// schedule, the recipients and downloads (OD40), non-admins refused, and the rate class of
// the heavy routes.
func TestBackupEndpoints(t *testing.T) {
	b := &fakes.Backups{Content: []byte("age-encrypted\x00bytes")}
	x := newAgentTCServer(t, func(d *app.Deps) { d.Backups = b }, Config{RateLimits: RateLimits{Export: 100}}, slog.New(slog.DiscardHandler))

	var st BackupStatus
	if code := x.getJSON("/api/v1/backups", &st); code != 200 || !st.Available || st.Folder != "/backups" || st.Driver != "sqlite" ||
		st.Schedule.Kind != BackupScheduleKindOff || len(st.Files) != 0 || st.RecipientSource != BackupStatusRecipientSourceEmpty || st.NextRun != nil {
		t.Fatalf("status %d %+v", code, st)
	}
	daily := `{"kind":"daily","at":"02:30","zone":"Europe/London","keep":7}`
	if code, _, _ := x.do("PUT", "/api/v1/backups/schedule", daily); code != 403 {
		t.Fatalf("schedule without CSRF %d", code)
	}
	if code, m, _ := x.do("PUT", "/api/v1/backups/schedule", daily, x.csrfHdr()...); code != 400 || m["field"] != "recipients" {
		t.Fatalf("schedule without a recipient %d %v", code, m)
	}
	if code, m, _ := x.do("POST", "/api/v1/backups/run", "", x.csrfHdr()...); code != 400 || len(b.Names()) != 0 {
		t.Fatalf("run without a recipient %d %v", code, m)
	}
	if code, _, _ := x.do("PUT", "/api/v1/backups/recipients", `{"recipients":["`+testRecipient+`"]}`); code != 403 {
		t.Fatalf("recipients without CSRF %d", code)
	}
	for body, want := range map[string]int{
		`{"recipients":["not a key"]}`:             400,
		`{"recipients":["` + testRecipient + `"]}`: 200,
		`{"recipients":"x"}`:                       400,
		`{"keys":[]}`:                              400,
	} {
		if code, m, _ := x.do("PUT", "/api/v1/backups/recipients", body, x.csrfHdr()...); code != want {
			t.Errorf("recipients %s: %d %v, want %d", body, code, m, want)
		}
	}
	for body, want := range map[string]int{
		`{"kind":"weekly","keep":0}`:                       400,
		`{"kind":"every","everyHours":0,"keep":0}`:         400,
		`{"kind":"daily","at":"24:00","keep":0}`:           400,
		`{"kind":"hourly","zone":"Mars/Olympus","keep":0}`: 400,
		`{"kind":"hourly","keep":1001}`:                    400,
		daily:                                              200,
	} {
		if code, m, _ := x.do("PUT", "/api/v1/backups/schedule", body, x.csrfHdr()...); code != want {
			t.Errorf("schedule %s: %d %v, want %d", body, code, m, want)
		}
	}
	if code := x.getJSON("/api/v1/backups", &st); code != 200 || st.Schedule.Kind != BackupScheduleKindDaily || st.NextRun == nil ||
		st.NextRunLocal == nil || !strings.Contains(*st.NextRunLocal, "02:30") || len(st.Recipients) != 1 || st.Recipients[0].Source != BackupRecipientSourceUi {
		t.Fatalf("status after setup %d %+v", code, st)
	}

	if code, _, _ := x.do("POST", "/api/v1/backups/run", ""); code != 403 {
		t.Fatalf("run without CSRF %d", code)
	}
	code, m, _ := x.do("POST", "/api/v1/backups/run", "", x.csrfHdr()...)
	if code != 200 || !strings.HasSuffix(m["name"].(string), ".db.age") || m["size"].(float64) != float64(len(b.Content)) {
		t.Fatalf("run %d %v", code, m)
	}
	name := m["name"].(string)
	code, h, body := x.raw("/api/v1/backups/files/"+name, "")
	if code != 200 || body != string(b.Content) || h.Get("Content-Type") != "application/octet-stream" || h.Get("Cache-Control") != "no-store" ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Disposition") != `attachment; filename="`+name+`"` {
		t.Fatalf("download %d %v %q", code, h, body)
	}
	for _, bad := range []string{"notes.txt", "..%2Fetc%2Fpasswd", "cactustrade-20990101T000000Z.db.age", ".partial-00"} {
		if code, h, _ := x.raw("/api/v1/backups/files/"+bad, ""); code != 404 || !strings.HasPrefix(h.Get("Content-Type"), "application/problem+json") {
			t.Errorf("download %s: %d", bad, code)
		}
	}
	if code := x.getJSON("/api/v1/backups", &st); code != 200 || len(st.Files) != 1 || st.Last == nil || !st.Last.Ok || *st.Last.File != name {
		t.Fatalf("status after the run %d %+v", code, st)
	}

	// An admin API key may read the status and back up now; never the schedule, the
	// recipients or a download (OD40).
	code, m, _ = x.do("POST", "/api/v1/api-keys", `{"name":"k","role":"admin","expiresInDays":30}`, x.csrfHdr()...)
	if code != 201 {
		t.Fatalf("key %d %v", code, m)
	}
	key := m["secret"].(string)
	if code, body := x.bearer("GET", "/api/v1/backups", "", key); code != 200 {
		t.Fatalf("API key status %d %s", code, body)
	}
	if code, body := x.bearer("POST", "/api/v1/backups/run", "", key); code != 200 {
		t.Fatalf("API key run %d %s", code, body)
	}
	if code, _ := x.bearer("PUT", "/api/v1/backups/schedule", daily, key); code != 403 {
		t.Fatalf("API key schedule %d", code)
	}
	if code, _ := x.bearer("PUT", "/api/v1/backups/recipients", `{"recipients":["`+testRecipient+`"]}`, key); code != 403 {
		t.Fatalf("API key recipients %d", code)
	}
	if code, _, _ := x.raw("/api/v1/backups/files/"+name, key); code != 403 {
		t.Fatalf("API key download %d", code)
	}

	// One at a time: a second run while one is writing is a conflict.
	entered, release := make(chan struct{}), make(chan struct{})
	b.OnWrite = func() { close(entered); <-release }
	done := make(chan int)
	go func() {
		code, _ := x.bearer("POST", "/api/v1/backups/run", "", key)
		done <- code
	}()
	<-entered
	b.OnWrite = nil
	if code, m, _ := x.do("POST", "/api/v1/backups/run", "", x.csrfHdr()...); code != 409 {
		t.Fatalf("a second run %d %v", code, m)
	}
	close(release)
	if code := <-done; code != 200 {
		t.Fatalf("the first run %d", code)
	}

	// A failing backup: 503, nothing written.
	b.WriteErr = errString("disk full")
	if code, m, _ := x.do("POST", "/api/v1/backups/run", "", x.csrfHdr()...); code != 503 {
		t.Fatalf("failing run %d %v", code, m)
	}
	b.WriteErr = nil

	// Non-admins see nothing.
	for _, role := range []string{"operator", "viewer"} {
		if code, m, _ := x.do("POST", "/api/v1/users", `{"username":"`+role+`1","password":"`+role+` long password","role":"`+role+`"}`, x.csrfHdr()...); code != 201 {
			t.Fatalf("create %s %d %v", role, code, m)
		}
	}
	for _, role := range []string{"operator", "viewer"} {
		x.c.Jar, _ = cookiejar.New(nil)
		x.login(role+"1", role+" long password")
		if code, _, _ := x.do("GET", "/api/v1/backups", ""); code != 403 {
			t.Fatalf("%s status %d", role, code)
		}
		if code, _, _ := x.do("POST", "/api/v1/backups/run", "", x.csrfHdr()...); code != 403 {
			t.Fatalf("%s run %d", role, code)
		}
		if code, _, _ := x.raw("/api/v1/backups/files/"+name, ""); code != 403 {
			t.Fatalf("%s download %d", role, code)
		}
	}

	for _, p := range []string{"POST /api/v1/backups/run", "GET /api/v1/backups/files/{name}"} {
		method, path, _ := strings.Cut(p, " ")
		if c := classify(method, path, p); c != classExport {
			t.Errorf("%s: class %q, want export", p, c)
		}
	}
}

// errString is an error with a fixed text.
type errString string

func (e errString) Error() string { return string(e) }

// TestBackupsUnavailableEndpoint: without a backup folder the page says so.
func TestBackupsUnavailableEndpoint(t *testing.T) {
	x := newAgentTC(t)
	var st BackupStatus
	if code := x.getJSON("/api/v1/backups", &st); code != 200 || st.Available || st.Files == nil || st.Recipients == nil {
		t.Fatalf("status %d %+v", code, st)
	}
	if code, _, _ := x.do("POST", "/api/v1/backups/run", "", x.csrfHdr()...); code != 503 {
		t.Fatalf("run %d", code)
	}
}
