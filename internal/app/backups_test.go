package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

const (
	rcptA = "age1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rcptB = "age1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// backupEnv is an env with a fake backup folder and an admin with an e-mail address.
func backupEnv(t *testing.T) (*env, *fakes.Backups) {
	t.Helper()
	e := newEnv(t)
	b := &fakes.Backups{}
	e.svc.d.Backups = b
	e.svc.cfg.TimeZone = "Europe/London"
	ctx := context.Background()
	u, err := e.store.Users().Get(ctx, e.admin.UserID)
	if err != nil {
		t.Fatal(err)
	}
	u.Email = "admin@example.com"
	if err := e.store.Users().Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	return e, b
}

func auditActions(e *env) []string {
	var out []string
	for _, a := range e.store.AuditEntries() {
		if strings.HasPrefix(a.Action, "backup.") {
			out = append(out, a.Action+" "+a.Details)
		}
	}
	return out
}

func hasAudit(e *env, action, contains string) bool {
	for _, a := range e.store.AuditEntries() {
		if a.Action == action && strings.Contains(a.Details+" "+a.Target, contains) {
			return true
		}
	}
	return false
}

func TestBackupRoles(t *testing.T) {
	e, b := backupEnv(t)
	ctx := context.Background()
	viewer := e.user(t, "viewer", domain.RoleViewer)
	operator := e.user(t, "operator", domain.RoleOperator)
	key := e.admin
	key.Via, key.APIKeyID = ViaAPIKey, "k1"
	if _, err := e.svc.SetBackupRecipients(ctx, e.admin, []string{rcptA}); err != nil {
		t.Fatal(err)
	}
	f, err := e.svc.BackupNow(ctx, e.admin)
	if err != nil {
		t.Fatal(err)
	}
	ops := map[string]func(p Principal) error{
		"status": func(p Principal) error { _, err := e.svc.BackupStatus(ctx, p); return err },
		"schedule": func(p Principal) error {
			_, err := e.svc.SetBackupSchedule(ctx, p, domain.BackupSchedule{Kind: domain.BackupOff})
			return err
		},
		"recipients": func(p Principal) error { _, err := e.svc.SetBackupRecipients(ctx, p, []string{rcptA}); return err },
		"now":        func(p Principal) error { _, err := e.svc.BackupNow(ctx, p); return err },
		"download": func(p Principal) error {
			rc, _, err := e.svc.OpenBackup(ctx, p, f.Name)
			if rc != nil {
				_ = rc.Close()
			}
			return err
		},
	}
	tests := []struct {
		who  string
		p    Principal
		want map[string]error // nil = allowed
	}{
		{"viewer", viewer, map[string]error{"status": domain.ErrForbidden, "schedule": domain.ErrForbidden, "recipients": domain.ErrForbidden, "now": domain.ErrForbidden, "download": domain.ErrForbidden}},
		{"operator", operator, map[string]error{"status": domain.ErrForbidden, "schedule": domain.ErrForbidden, "recipients": domain.ErrForbidden, "now": domain.ErrForbidden, "download": domain.ErrForbidden}},
		{"admin API key", key, map[string]error{"status": nil, "schedule": domain.ErrForbidden, "recipients": domain.ErrForbidden, "now": nil, "download": domain.ErrForbidden}},
		{"admin session", e.admin, map[string]error{"status": nil, "schedule": nil, "recipients": nil, "now": nil, "download": nil}},
		{"anonymous", Principal{}, map[string]error{"status": domain.ErrUnauthenticated, "schedule": domain.ErrUnauthenticated, "recipients": domain.ErrUnauthenticated, "now": domain.ErrUnauthenticated, "download": domain.ErrUnauthenticated}},
	}
	for _, tt := range tests {
		for op, call := range ops {
			t.Run(tt.who+" "+op, func(t *testing.T) {
				err := call(tt.p)
				want := tt.want[op]
				if (want == nil) != (err == nil) || (want != nil && !errors.Is(err, want)) {
					t.Fatalf("%s %s = %v, want %v", tt.who, op, err, want)
				}
			})
		}
	}
	if len(b.Names()) < 2 {
		t.Fatalf("files %v", b.Names())
	}
}

func TestBackupNeedsARecipient(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		env    []string
		ui     []string
		wantOK bool
		source string
	}{
		{"none: refused (OD29)", nil, nil, false, ""},
		{"deployment's recipient", []string{rcptB}, nil, true, "env"},
		{"the app's recipient", nil, []string{rcptA}, true, "ui"},
		{"the app's wins over the deployment's", []string{rcptB}, []string{rcptA}, true, "ui"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, b := backupEnv(t)
			e.svc.cfg.BackupRecipients = tt.env
			if tt.ui != nil {
				if _, err := e.svc.SetBackupRecipients(ctx, e.admin, tt.ui); err != nil {
					t.Fatal(err)
				}
			}
			_, serr := e.svc.SetBackupSchedule(ctx, e.admin, domain.BackupSchedule{Kind: domain.BackupHourly})
			_, nerr := e.svc.BackupNow(ctx, e.admin)
			if tt.wantOK != (serr == nil) || tt.wantOK != (nerr == nil) {
				t.Fatalf("schedule %v, now %v; want ok=%v", serr, nerr, tt.wantOK)
			}
			if !tt.wantOK {
				if !errors.Is(serr, domain.ErrInvalid) || !errors.Is(nerr, domain.ErrInvalid) || len(b.Names()) != 0 {
					t.Fatalf("schedule %v, now %v, files %v", serr, nerr, b.Names())
				}
				return
			}
			st, err := e.svc.BackupStatus(ctx, e.admin)
			if err != nil || st.RecipientSource != tt.source || len(st.Recipients) != 1 || b.Writes[0][0] != st.Recipients[0].Key {
				t.Fatalf("status %+v %v writes %v", st, err, b.Writes)
			}
		})
	}
}

func TestSetBackupValidation(t *testing.T) {
	ctx := context.Background()
	e, _ := backupEnv(t)
	if _, err := e.svc.SetBackupRecipients(ctx, e.admin, []string{rcptA}); err != nil {
		t.Fatal(err)
	}
	schedules := []struct {
		name string
		s    domain.BackupSchedule
		ok   bool
	}{
		{"daily", domain.BackupSchedule{Kind: domain.BackupDaily, At: "02:30", Zone: "Europe/London", Keep: 7}, true},
		{"every 5", domain.BackupSchedule{Kind: domain.BackupEvery, EveryHours: 5, At: "09:00"}, true},
		{"every 0", domain.BackupSchedule{Kind: domain.BackupEvery}, false},
		{"unknown zone", domain.BackupSchedule{Kind: domain.BackupHourly, Zone: "Mars/Olympus"}, false},
		{"bad time", domain.BackupSchedule{Kind: domain.BackupDaily, At: "25:00"}, false},
		{"keep -1", domain.BackupSchedule{Kind: domain.BackupHourly, Keep: -1}, false},
		{"weekly", domain.BackupSchedule{Kind: "weekly"}, false},
	}
	for _, tt := range schedules {
		t.Run(tt.name, func(t *testing.T) {
			st, err := e.svc.SetBackupSchedule(ctx, e.admin, tt.s)
			if tt.ok != (err == nil) || (!tt.ok && !errors.Is(err, domain.ErrInvalid)) {
				t.Fatalf("SetBackupSchedule = %v, want ok=%v", err, tt.ok)
			}
			if tt.ok && (st.Schedule.Kind != tt.s.Kind || st.NextRun.IsZero() || st.NextRunLocal == "") {
				t.Fatalf("status %+v", st.Schedule)
			}
		})
	}
	if st, _ := e.svc.BackupStatus(ctx, e.admin); st.Schedule.At != "" || st.Schedule.Zone != "Europe/London" {
		t.Fatalf("every-hours schedule kept %q, zone %q", st.Schedule.At, st.Schedule.Zone)
	}
	recipients := []struct {
		name string
		rs   []string
		ok   bool
	}{
		{"two", []string{rcptA, " " + rcptB + " "}, true},
		{"twice", []string{rcptA, rcptA}, false},
		{"not a key", []string{"ssh-ed25519 AAAA"}, false},
		{"eleven", strings.Split(strings.Repeat(rcptA+",", 11), ",")[:11], false},
		{"empty while a schedule runs and the deployment has none", nil, false},
	}
	for _, tt := range recipients {
		t.Run("recipients "+tt.name, func(t *testing.T) {
			_, err := e.svc.SetBackupRecipients(ctx, e.admin, tt.rs)
			if tt.ok != (err == nil) || (!tt.ok && !errors.Is(err, domain.ErrInvalid)) {
				t.Fatalf("SetBackupRecipients = %v, want ok=%v", err, tt.ok)
			}
		})
	}
	if _, err := e.svc.SetBackupSchedule(ctx, e.admin, domain.BackupSchedule{Kind: domain.BackupOff}); err != nil {
		t.Fatal(err)
	}
	if st, err := e.svc.SetBackupRecipients(ctx, e.admin, nil); err != nil || st.RecipientSource != "" {
		t.Fatalf("clearing with the schedule off: %+v %v", st, err)
	}
}

// TestBackupChangesAuditedAndMailed: every change is audited (keys by fingerprint only) and
// a recipient change e-mails every admin.
func TestBackupChangesAuditedAndMailed(t *testing.T) {
	ctx := context.Background()
	e, _ := backupEnv(t)
	if _, err := e.svc.SetBackupRecipients(ctx, e.admin, []string{rcptA}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetBackupSchedule(ctx, e.admin, domain.BackupSchedule{Kind: domain.BackupDaily, At: "02:30", Keep: 3}); err != nil {
		t.Fatal(err)
	}
	if !hasAudit(e, "backup.recipients_changed", "none → "+fingerprint(rcptA)) {
		t.Fatalf("recipients change not audited: %v", auditActions(e))
	}
	if !hasAudit(e, "backup.schedule_changed", "off → daily at 02:30, keep 3") {
		t.Fatalf("schedule change not audited: %v", auditActions(e))
	}
	for _, a := range e.store.AuditEntries() {
		if strings.Contains(a.Details, rcptA) {
			t.Fatalf("a key in the audit log: %+v", a)
		}
	}
	ms, err := e.store.Outbox().List(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	mailed := false
	for _, m := range ms {
		if m.To == "admin@example.com" && strings.Contains(m.Subject, "Backup recipients changed") {
			mailed = true
		}
	}
	if !mailed {
		t.Fatalf("no e-mail to the admin: %+v", ms)
	}
}

// TestRunDueBackupDaily ticks every minute over two days: the daily 02:30 Europe/London
// backup runs once a day at 02:30 local time.
func TestRunDueBackupDaily(t *testing.T) {
	ctx := context.Background()
	e, b := backupEnv(t)
	e.clock.T = time.Date(2027, 7, 9, 12, 0, 0, 0, time.UTC)
	if _, err := e.svc.SetBackupRecipients(ctx, e.admin, []string{rcptA}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetBackupSchedule(ctx, e.admin, domain.BackupSchedule{Kind: domain.BackupDaily, At: "02:30", Zone: "Europe/London"}); err != nil {
		t.Fatal(err)
	}
	var ranAt []time.Time
	for range 2 * 24 * 60 {
		e.clock.Advance(time.Minute)
		now := e.clock.T
		ran, err := e.svc.RunDueBackup(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ran {
			ranAt = append(ranAt, now)
		}
	}
	// 02:30 BST = 01:30 UTC
	want := []time.Time{time.Date(2027, 7, 10, 1, 30, 0, 0, time.UTC), time.Date(2027, 7, 11, 1, 30, 0, 0, time.UTC)}
	if len(ranAt) != 2 || !ranAt[0].Equal(want[0]) || !ranAt[1].Equal(want[1]) {
		t.Fatalf("ran at %v, want %v", ranAt, want)
	}
	if len(b.Names()) != 2 || !hasAudit(e, "backup.run", "trigger schedule") {
		t.Fatalf("files %v audit %v", b.Names(), auditActions(e))
	}
}

func TestRunDueBackupMissedSlotsRunOnce(t *testing.T) {
	ctx := context.Background()
	e, b := backupEnv(t)
	if _, err := e.svc.SetBackupRecipients(ctx, e.admin, []string{rcptA}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetBackupSchedule(ctx, e.admin, domain.BackupSchedule{Kind: domain.BackupHourly}); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(3 * 24 * time.Hour) // the process was down for three days
	for i, want := range []bool{true, false, false} {
		ran, err := e.svc.RunDueBackup(ctx)
		if err != nil || ran != want {
			t.Fatalf("tick %d: ran %v %v, want %v", i, ran, err, want)
		}
	}
	if len(b.Names()) != 1 {
		t.Fatalf("a burst of backups: %v", b.Names())
	}
}

func TestRunDueBackupIdleCases(t *testing.T) {
	ctx := context.Background()
	e, _ := backupEnv(t)
	if ran, err := e.svc.RunDueBackup(ctx); ran || err != nil { // no schedule stored
		t.Fatalf("off: %v %v", ran, err)
	}
	if err := e.store.Settings().Set(ctx, domain.SettingBackupSchedule, "{not json", e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if ran, err := e.svc.RunDueBackup(ctx); ran || err != nil { // unreadable = off
		t.Fatalf("unreadable: %v %v", ran, err)
	}
	if err := e.store.Settings().Set(ctx, domain.SettingBackupSchedule, `{"kind":"hourly","zone":"Mars/Olympus","keep":0}`, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if ran, err := e.svc.RunDueBackup(ctx); ran || err == nil {
		t.Fatalf("unknown zone: %v %v", ran, err)
	}
	e.svc.d.Backups = nil
	if ran, err := e.svc.RunDueBackup(ctx); ran || err != nil {
		t.Fatalf("no folder: %v %v", ran, err)
	}
}

// TestBackupFailure: a failed run is audited, raises a warning event, stores the error in
// backup.last, prunes nothing, and the next slot is still scheduled (and then runs).
func TestBackupFailure(t *testing.T) {
	ctx := context.Background()
	e, b := backupEnv(t)
	e.clock.Step = time.Millisecond // a running clock: the run takes time
	if _, err := e.svc.SetBackupRecipients(ctx, e.admin, []string{rcptA}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetBackupSchedule(ctx, e.admin, domain.BackupSchedule{Kind: domain.BackupHourly, Keep: 1}); err != nil {
		t.Fatal(err)
	}
	b.Put("cactustrade-20200101T000000Z.db.age", []byte("old"))
	b.Put("cactustrade-20200102T000000Z.db.age", []byte("old"))
	b.WriteErr = errors.New("disk full")
	e.clock.Advance(time.Hour)
	ran, err := e.svc.RunDueBackup(ctx)
	if !ran || err == nil {
		t.Fatalf("failing run: %v %v", ran, err)
	}
	if !hasAudit(e, "backup.failed", "disk full") {
		t.Fatalf("not audited: %v", auditActions(e))
	}
	evs, err := e.store.Events().List(ctx, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 || evs[0].Kind != "backup.failed" || evs[0].Severity != domain.SeverityWarning {
		t.Fatalf("events %+v", evs)
	}
	st, err := e.svc.BackupStatus(ctx, e.admin)
	if err != nil || st.Last == nil || st.Last.OK || !strings.Contains(st.Last.Error, "disk full") || st.NextRun.IsZero() || !st.NextRun.After(st.Last.At) {
		t.Fatalf("status after the failure %+v %v", st, err)
	}
	if len(b.Names()) != 2 {
		t.Fatalf("pruned after a failure: %v", b.Names())
	}
	if ran, _ := e.svc.RunDueBackup(ctx); ran { // not retried every minute
		t.Fatal("a failed slot ran again at once")
	}
	b.WriteErr = nil
	e.clock.Advance(time.Hour)
	if ran, err := e.svc.RunDueBackup(ctx); !ran || err != nil {
		t.Fatalf("the next slot: %v %v", ran, err)
	}
	names := b.Names()
	if len(names) != 1 || strings.HasPrefix(names[0], "cactustrade-2020") || !hasAudit(e, "backup.pruned", "keep 1") {
		t.Fatalf("after a success with keep 1: %v %v", names, auditActions(e))
	}
	if st, _ := e.svc.BackupStatus(ctx, e.admin); st.Last == nil || !st.Last.OK || st.Last.Ms <= 0 || st.Last.Trigger != "schedule" {
		t.Fatalf("last %+v", st.Last)
	}
}

func TestBackupPruneKeepsStrangers(t *testing.T) {
	ctx := context.Background()
	e, b := backupEnv(t)
	if _, err := e.svc.SetBackupRecipients(ctx, e.admin, []string{rcptA}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetBackupSchedule(ctx, e.admin, domain.BackupSchedule{Kind: domain.BackupOff, Keep: 2}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"cactustrade-20200101T000000Z.db.age", "cactustrade-20200102T000000Z.sql.gz.age", "cactustrade-20200103T000000Z.db.age", "notes.txt"} {
		b.Put(n, []byte("x"))
	}
	if _, err := e.svc.BackupNow(ctx, e.admin); err != nil {
		t.Fatal(err)
	}
	got := b.Names()
	if len(got) != 3 || got[0] != "cactustrade-20200103T000000Z.db.age" || got[2] != "notes.txt" {
		t.Fatalf("after pruning to 2: %v", got)
	}
}

func TestBackupOneAtATime(t *testing.T) {
	ctx := context.Background()
	e, b := backupEnv(t)
	if _, err := e.svc.SetBackupRecipients(ctx, e.admin, []string{rcptA}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	b.OnWrite = func() {
		once.Do(func() { close(entered); <-release })
	}
	done := make(chan error)
	go func() {
		_, err := e.svc.BackupNow(ctx, e.admin)
		done <- err
	}()
	<-entered
	if _, err := e.svc.BackupNow(ctx, e.admin); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second run: %v", err)
	}
	if st, err := e.svc.BackupStatus(ctx, e.admin); err != nil || !st.Running {
		t.Fatalf("status while running: %+v %v", st, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st, _ := e.svc.BackupStatus(ctx, e.admin); st.Running || len(st.Files) != 1 {
		t.Fatalf("status after: %+v", st)
	}
}

func TestOpenBackup(t *testing.T) {
	ctx := context.Background()
	e, b := backupEnv(t)
	b.Content = []byte("encrypted bytes")
	if _, err := e.svc.SetBackupRecipients(ctx, e.admin, []string{rcptA}); err != nil {
		t.Fatal(err)
	}
	f, err := e.svc.BackupNow(ctx, e.admin)
	if err != nil {
		t.Fatal(err)
	}
	rc, got, err := e.svc.OpenBackup(ctx, e.admin, f.Name)
	if err != nil || got.Name != f.Name {
		t.Fatalf("open %+v %v", got, err)
	}
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(data) != "encrypted bytes" || !hasAudit(e, "backup.downloaded", f.Name) {
		t.Fatalf("content %q audit %v", data, auditActions(e))
	}
	for _, bad := range []string{"../etc/passwd", "notes.txt", "cactustrade-20990101T000000Z.db.age"} {
		if _, _, err := e.svc.OpenBackup(ctx, e.admin, bad); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("open %q: %v", bad, err)
		}
	}
}

func TestBackupsUnavailable(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	st, err := e.svc.BackupStatus(ctx, e.admin)
	if err != nil || st.Available {
		t.Fatalf("status %+v %v", st, err)
	}
	if _, err := e.svc.BackupNow(ctx, e.admin); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("now: %v", err)
	}
	if _, err := e.svc.SetBackupSchedule(ctx, e.admin, domain.BackupSchedule{Kind: domain.BackupOff}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("schedule: %v", err)
	}
	if _, err := e.svc.SetBackupRecipients(ctx, e.admin, []string{rcptA}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("recipients: %v", err)
	}
	if _, _, err := e.svc.OpenBackup(ctx, e.admin, "x"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("open: %v", err)
	}
	e2, b := backupEnv(t)
	b.ListErr = errors.New("folder gone")
	if _, err := e2.svc.BackupStatus(ctx, e2.admin); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("list failure: %v", err)
	}
}
