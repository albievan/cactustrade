package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/albievan/cactustrade/internal/domain"
)

// Scheduled backups from the web app (R48). The schedule, the recipients and the last run
// are settings rows; the files live in the backup folder fixed at deploy time
// (ports.Backups). Every backup is age-encrypted to public keys only (OD29): without a
// recipient there is no schedule and no "Back up now". A failed backup raises a warning
// event; trading is not affected (backups are no input to entry safety).

// maxBackupRecipients bounds the recipients list.
const maxBackupRecipients = 10

// maxBackupError bounds the error text kept in backup.last.
const maxBackupError = 300

// backupState serialises backup runs: one at a time in this process. running is set by
// runBackup for as long as a run holds mu, so the status page reads it without touching the
// lock (a TryLock there could make a concurrent run look busy).
type backupState struct {
	mu      sync.Mutex
	running atomic.Bool
}

// storedBackupSchedule is the setting backup.schedule: the schedule and when it was set
// (the first slot counted is the first one after that).
type storedBackupSchedule struct {
	domain.BackupSchedule
	SetAt time.Time `json:"setAt"`
}

// BackupRecipient is one age public key that backups are encrypted to.
type BackupRecipient struct {
	Key         string // age1…
	Fingerprint string // sha256:<16 hex>, for the audit log
	Source      string // ui | env (CACTUSTRADE_BACKUP_RECIPIENT[_FILE], read-only here)
}

// BackupStatus is the Backups page: where backups go, when the next one runs, who can read
// them, how the last one went, and the files.
type BackupStatus struct {
	// Available is false when this deployment has no backup folder (nothing else is set).
	Available bool
	Folder    string
	Driver    string
	Schedule  domain.BackupSchedule // Zone is the zone in force
	// NextRun is the next scheduled backup (zero = none); NextRunLocal is it in the zone.
	NextRun      time.Time
	NextRunLocal string
	Recipients   []BackupRecipient
	// RecipientSource says which list is in force: ui, env or "" (none: no backups).
	RecipientSource string
	Last            *domain.BackupRun
	Files           []domain.BackupFile
	Running         bool
}

// fingerprint is a short, stable name for a recipient key (never the key itself in logs).
func fingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:8])
}

// backupAdmin checks the admin permission and, when session is set, a web session.
func (s *Service) backupAdmin(p Principal, session bool) error {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return err
	}
	if session && p.Via != ViaSession {
		return fmt.Errorf("only from the web UI: %w", domain.ErrForbidden)
	}
	return nil
}

// backupsAvailable fails when this deployment has no backup folder.
func (s *Service) backupsAvailable() error {
	if s.d.Backups == nil {
		return fmt.Errorf("backups from the app are not set up in this deployment: %w", domain.ErrUnavailable)
	}
	return nil
}

// backupSchedule reads the stored schedule (off when none is stored or it is unreadable).
func (s *Service) backupSchedule(ctx context.Context) storedBackupSchedule {
	off := storedBackupSchedule{BackupSchedule: domain.BackupSchedule{Kind: domain.BackupOff}}
	v, err := s.d.Store.Settings().Get(ctx, domain.SettingBackupSchedule)
	if err != nil {
		return off
	}
	var st storedBackupSchedule
	if err := json.Unmarshal([]byte(v), &st); err != nil || st.Validate() != nil {
		s.d.Log.WarnContext(ctx, "backups: the stored schedule is unreadable; treated as off", "err", err)
		return off
	}
	return st
}

// backupZone loads the schedule's time zone ("" = the deployment's, else UTC).
func (s *Service) backupZone(zone string) (string, *time.Location, error) {
	if zone == "" {
		zone = s.cfg.TimeZone
	}
	if zone == "" {
		zone = "UTC"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return zone, nil, domain.Invalid("zone", "an IANA time zone such as Europe/London")
	}
	return zone, loc, nil
}

// uiBackupRecipients reads the recipients entered in the app.
func (s *Service) uiBackupRecipients(ctx context.Context) []string {
	v, err := s.d.Store.Settings().Get(ctx, domain.SettingBackupRecipients)
	if err != nil {
		return nil
	}
	var rs []string
	if err := json.Unmarshal([]byte(v), &rs); err != nil {
		s.d.Log.WarnContext(ctx, "backups: the stored recipients are unreadable", "err", err)
		return nil
	}
	return rs
}

// backupRecipients returns the recipients in force: the app's list, else the deployment's.
func (s *Service) backupRecipients(ctx context.Context) ([]string, string) {
	if rs := s.uiBackupRecipients(ctx); len(rs) > 0 {
		return rs, "ui"
	}
	if len(s.cfg.BackupRecipients) > 0 {
		return slices.Clone(s.cfg.BackupRecipients), "env"
	}
	return nil, ""
}

// lastBackup reads backup.last (nil = never).
func (s *Service) lastBackup(ctx context.Context) *domain.BackupRun {
	v, err := s.d.Store.Settings().Get(ctx, domain.SettingBackupLast)
	if err != nil {
		return nil
	}
	var r domain.BackupRun
	if err := json.Unmarshal([]byte(v), &r); err != nil {
		return nil
	}
	return &r
}

// putSetting stores a JSON setting.
func (s *Service) putSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.d.Store.Settings().Set(ctx, key, string(b), s.d.Clock.Now().UTC())
}

// BackupStatus is the Backups page (admin; a web session or an admin API key).
func (s *Service) BackupStatus(ctx context.Context, p Principal) (BackupStatus, error) {
	if err := s.backupAdmin(p, false); err != nil {
		return BackupStatus{}, err
	}
	if s.d.Backups == nil {
		return BackupStatus{}, nil
	}
	st := s.backupSchedule(ctx)
	out := BackupStatus{
		Available: true, Folder: s.d.Backups.Folder(), Driver: s.d.Backups.Driver(),
		Schedule: st.BackupSchedule, Last: s.lastBackup(ctx),
	}
	zone, loc, err := s.backupZone(st.Zone)
	out.Schedule.Zone = zone
	if err == nil && st.Enabled() {
		ref := st.SetAt
		if out.Last != nil && out.Last.At.After(ref) {
			ref = out.Last.At
		}
		now := s.d.Clock.Now()
		if next := st.Next(ref, loc); !next.IsZero() {
			if next.Before(now) { // missed while stopped: runs at the next tick
				next = now
			}
			out.NextRun, out.NextRunLocal = next.UTC(), next.In(loc).Format("Mon 2 Jan 2006 15:04 MST")
		}
	}
	rs, src := s.backupRecipients(ctx)
	out.RecipientSource = src
	for _, r := range rs {
		out.Recipients = append(out.Recipients, BackupRecipient{Key: r, Fingerprint: fingerprint(r), Source: src})
	}
	out.Running = s.backups.running.Load()
	files, err := s.d.Backups.List(ctx)
	if err != nil {
		return out, fmt.Errorf("backups: listing the folder: %w: %w", domain.ErrUnavailable, err)
	}
	out.Files = files
	return out, nil
}

// SetBackupSchedule stores the schedule (admin, web session). A schedule that runs needs a
// recipient (OD29). Audited old → new.
func (s *Service) SetBackupSchedule(ctx context.Context, p Principal, sched domain.BackupSchedule) (BackupStatus, error) {
	if err := s.backupAdmin(p, true); err != nil {
		return BackupStatus{}, err
	}
	if err := s.backupsAvailable(); err != nil {
		return BackupStatus{}, err
	}
	sched.Zone = strings.TrimSpace(sched.Zone)
	sched.At = strings.TrimSpace(sched.At)
	if err := sched.Validate(); err != nil {
		return BackupStatus{}, err
	}
	if _, _, err := s.backupZone(sched.Zone); err != nil {
		return BackupStatus{}, err
	}
	switch sched.Kind { // only the fields of the kind are kept
	case domain.BackupOff, domain.BackupHourly:
		sched.EveryHours, sched.At = 0, ""
	case domain.BackupEvery:
		sched.At = ""
	case domain.BackupDaily:
		sched.EveryHours = 0
	}
	if rs, _ := s.backupRecipients(ctx); sched.Enabled() && len(rs) == 0 {
		return BackupStatus{}, domain.Invalid("recipients", "add an age public key first: backups are always encrypted")
	}
	old := s.backupSchedule(ctx)
	if err := s.putSetting(ctx, domain.SettingBackupSchedule, storedBackupSchedule{BackupSchedule: sched, SetAt: s.d.Clock.Now().UTC()}); err != nil {
		return BackupStatus{}, fmt.Errorf("backups: storing the schedule: %w", err)
	}
	s.audit(ctx, p, "backup.schedule_changed", "", old.String()+" → "+sched.String())
	return s.BackupStatus(ctx, p)
}

// SetBackupRecipients stores the age public keys backups are encrypted to (admin, web
// session). An empty list falls back to the deployment's recipients; it is refused while a
// schedule runs and the deployment has none. Every admin is e-mailed; the audit log names
// the keys by fingerprint.
func (s *Service) SetBackupRecipients(ctx context.Context, p Principal, recipients []string) (BackupStatus, error) {
	if err := s.backupAdmin(p, true); err != nil {
		return BackupStatus{}, err
	}
	if err := s.backupsAvailable(); err != nil {
		return BackupStatus{}, err
	}
	var rs []string
	for _, r := range recipients {
		if r = strings.TrimSpace(r); r == "" {
			continue
		}
		if slices.Contains(rs, r) {
			return BackupStatus{}, domain.Invalid("recipients", "a key is listed twice")
		}
		rs = append(rs, r)
	}
	if len(rs) > maxBackupRecipients {
		return BackupStatus{}, domain.Invalid("recipients", "at most 10 keys")
	}
	if len(rs) > 0 {
		if err := s.d.Backups.ParseRecipients(rs); err != nil {
			return BackupStatus{}, err
		}
	}
	if len(rs) == 0 && len(s.cfg.BackupRecipients) == 0 && s.backupSchedule(ctx).Enabled() {
		return BackupStatus{}, domain.Invalid("recipients", "switch the schedule off first: backups are always encrypted")
	}
	old := s.uiBackupRecipients(ctx)
	if rs == nil {
		rs = []string{}
	}
	if err := s.putSetting(ctx, domain.SettingBackupRecipients, rs); err != nil {
		return BackupStatus{}, fmt.Errorf("backups: storing the recipients: %w", err)
	}
	fps := func(keys []string) string {
		if len(keys) == 0 {
			return "none"
		}
		out := make([]string, len(keys))
		for i, k := range keys {
			out[i] = fingerprint(k)
		}
		return strings.Join(out, ", ")
	}
	change := fps(old) + " → " + fps(rs)
	s.audit(ctx, p, "backup.recipients_changed", "", change)
	s.mailAdmins(ctx, p.Username, "Backup recipients changed",
		"The keys that cactusTrade's backups are encrypted to were changed ("+change+").\n"+
			"Only the holder of a matching private key can read the backups taken from now on.")
	return s.BackupStatus(ctx, p)
}

// mailAdmins e-mails every enabled admin with an address.
func (s *Service) mailAdmins(ctx context.Context, actor, title, body string) {
	users, err := s.d.Store.Users().List(ctx)
	if err != nil {
		s.d.Log.WarnContext(ctx, "listing users for the e-mail failed", "err", err)
		return
	}
	if actor == "" {
		actor = "system"
	}
	for _, u := range users {
		if u.Disabled || u.Email == "" || u.Role != domain.RoleAdmin {
			continue
		}
		s.enqueue(ctx, u.Email, "[cactusTrade] "+title, title+"\n\n"+body+"\n\nWhen: "+
			s.d.Clock.Now().UTC().Format("Mon 2 Jan 2006 15:04 MST")+"\nBy: "+actor+
			"\n\nYou get this because you are an administrator.\n")
	}
}

// BackupNow takes a backup at once (admin; a web session or an admin API key). Only one
// backup runs at a time (domain.ErrConflict).
func (s *Service) BackupNow(ctx context.Context, p Principal) (domain.BackupFile, error) {
	if err := s.backupAdmin(p, false); err != nil {
		return domain.BackupFile{}, err
	}
	return s.runBackup(ctx, p, domain.BackupTriggerManual)
}

// OpenBackup opens a backup file for download (admin, web session only, OD40). Audited.
func (s *Service) OpenBackup(ctx context.Context, p Principal, name string) (io.ReadCloser, domain.BackupFile, error) {
	if err := s.backupAdmin(p, true); err != nil {
		return nil, domain.BackupFile{}, err
	}
	if err := s.backupsAvailable(); err != nil {
		return nil, domain.BackupFile{}, err
	}
	rc, f, err := s.d.Backups.Open(ctx, name)
	if err != nil {
		return nil, domain.BackupFile{}, err
	}
	s.audit(ctx, p, "backup.downloaded", f.Name, fmt.Sprintf("%d bytes", f.Size))
	return rc, f, nil
}

// RunDueBackup is the scheduler (a 1-minute tick in cmd/cactustrade): it takes a backup when
// a slot of the schedule has come since the last run (or since the schedule was set). Missed
// slots run once. It reports whether a backup was attempted.
func (s *Service) RunDueBackup(ctx context.Context) (bool, error) {
	if s.d.Backups == nil {
		return false, nil
	}
	st := s.backupSchedule(ctx)
	if !st.Enabled() {
		return false, nil
	}
	_, loc, err := s.backupZone(st.Zone)
	if err != nil {
		return false, fmt.Errorf("backups: the schedule's time zone %q: %w", st.Zone, err)
	}
	ref := st.SetAt
	if last := s.lastBackup(ctx); last != nil && last.At.After(ref) {
		ref = last.At
	}
	if !st.Due(ref, s.d.Clock.Now(), loc) {
		return false, nil
	}
	_, err = s.runBackup(ctx, Principal{}, domain.BackupTriggerSchedule)
	return true, err
}

// runBackup takes one backup: one at a time, recipients required (OD29), backup.last stored
// either way; old files are pruned after a success only. A failure is audited and raises a
// warning event.
func (s *Service) runBackup(ctx context.Context, p Principal, trigger string) (domain.BackupFile, error) {
	if err := s.backupsAvailable(); err != nil {
		return domain.BackupFile{}, err
	}
	if !s.backups.mu.TryLock() {
		return domain.BackupFile{}, fmt.Errorf("a backup is running: %w", domain.ErrConflict)
	}
	defer s.backups.mu.Unlock()
	s.backups.running.Store(true)
	defer s.backups.running.Store(false)
	start := s.d.Clock.Now().UTC()
	var f domain.BackupFile
	err := domain.Invalid("recipients", "add an age public key first: backups are always encrypted")
	// Without a recipient the run fails like any other (backup.last, audit, warning event):
	// a schedule left on without one is not silent. backup.last moves the scheduler on to
	// the next slot, so this is reported once per slot, not every minute.
	if rs, _ := s.backupRecipients(ctx); len(rs) > 0 {
		f, err = s.d.Backups.Write(ctx, rs, start)
	}
	run := domain.BackupRun{At: start, Trigger: trigger, Ms: s.d.Clock.Now().Sub(start).Milliseconds()}
	if err != nil {
		msg := err.Error()
		if len(msg) > maxBackupError {
			msg = msg[:maxBackupError]
			for !utf8.ValidString(msg) {
				msg = msg[:len(msg)-1]
			}
		}
		run.Error = msg
		if serr := s.putSetting(ctx, domain.SettingBackupLast, run); serr != nil {
			s.d.Log.ErrorContext(ctx, "backups: storing the last run failed", "err", serr)
		}
		s.audit(ctx, p, "backup.failed", "", "trigger "+trigger+": "+msg)
		s.emit(ctx, p.Username, "backup.failed", domain.SeverityWarning, "Backup failed",
			"The "+trigger+" backup failed: "+msg+". Trading is not affected.")
		if errors.Is(err, domain.ErrInvalid) {
			return domain.BackupFile{}, err
		}
		return domain.BackupFile{}, fmt.Errorf("backup failed: %w: %w", domain.ErrUnavailable, err)
	}
	run.OK, run.File, run.Bytes = true, f.Name, f.Size
	if serr := s.putSetting(ctx, domain.SettingBackupLast, run); serr != nil {
		s.d.Log.ErrorContext(ctx, "backups: storing the last run failed", "err", serr)
	}
	s.audit(ctx, p, "backup.run", f.Name, fmt.Sprintf("trigger %s, %d bytes, %d ms", trigger, f.Size, run.Ms))
	s.pruneBackups(ctx, p, f.Name)
	return f, nil
}

// pruneBackups deletes the oldest backup files beyond the schedule's keep (0 = keep all).
// Only names that parse as backups are touched, never written (the file just written).
// Called after a successful backup only.
func (s *Service) pruneBackups(ctx context.Context, p Principal, written string) {
	keep := s.backupSchedule(ctx).Keep
	if keep <= 0 {
		return
	}
	files, err := s.d.Backups.List(ctx)
	if err != nil {
		s.d.Log.WarnContext(ctx, "backups: listing for pruning failed", "err", err)
		return
	}
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	var removed []string
	var errs []error
	for _, n := range domain.PruneSelect(names, keep, written) {
		if err := s.d.Backups.Remove(ctx, n); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, n)
	}
	if len(removed) > 0 {
		s.audit(ctx, p, "backup.pruned", "", fmt.Sprintf("keep %d: %s", keep, strings.Join(removed, ", ")))
	}
	if err := errors.Join(errs...); err != nil {
		s.d.Log.WarnContext(ctx, "backups: pruning failed", "err", err)
	}
}
