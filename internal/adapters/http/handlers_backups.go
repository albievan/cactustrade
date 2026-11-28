package httpapi

import (
	"io"
	"net/http"
	"strconv"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// Backups (R48). Who may do what is decided by the use cases (admin; schedule, recipients
// and downloads from a web session only, OD40); here only shapes are converted.

func toBackupFile(f domain.BackupFile) BackupFile {
	return BackupFile{Name: f.Name, Size: f.Size, At: f.At.UTC()}
}

func toBackupSchedule(s domain.BackupSchedule) BackupSchedule {
	out := BackupSchedule{Kind: BackupScheduleKind(s.Kind), Keep: s.Keep}
	if s.Kind == "" {
		out.Kind = BackupScheduleKindOff
	}
	if s.EveryHours > 0 {
		out.EveryHours = ptr(s.EveryHours)
	}
	if s.At != "" {
		out.At = ptr(s.At)
	}
	if s.Zone != "" {
		out.Zone = ptr(s.Zone)
	}
	return out
}

func toBackupStatus(st app.BackupStatus) BackupStatus {
	out := BackupStatus{
		Available: st.Available, Folder: st.Folder, Driver: st.Driver, Schedule: toBackupSchedule(st.Schedule),
		RecipientSource: BackupStatusRecipientSource(st.RecipientSource), Running: st.Running,
		Recipients: make([]BackupRecipient, 0, len(st.Recipients)), Files: make([]BackupFile, 0, len(st.Files)),
	}
	if !st.NextRun.IsZero() {
		out.NextRun, out.NextRunLocal = ptr(st.NextRun.UTC()), ptr(st.NextRunLocal)
	}
	for _, r := range st.Recipients {
		out.Recipients = append(out.Recipients, BackupRecipient{Key: r.Key, Fingerprint: r.Fingerprint, Source: BackupRecipientSource(r.Source)})
	}
	for _, f := range st.Files {
		out.Files = append(out.Files, toBackupFile(f))
	}
	if l := st.Last; l != nil {
		run := BackupRun{At: l.At.UTC(), Trigger: BackupRunTrigger(l.Trigger), Ok: l.OK, Ms: l.Ms}
		if l.File != "" {
			run.File, run.Bytes = ptr(l.File), ptr(l.Bytes)
		}
		if l.Error != "" {
			run.Error = ptr(l.Error)
		}
		out.Last = &run
	}
	return out
}

// GetBackups returns the Backups page.
func (s *Server) GetBackups(w http.ResponseWriter, r *http.Request) {
	st, err := s.svc.BackupStatus(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackupStatus(st))
}

// SetBackupSchedule stores the schedule.
func (s *Server) SetBackupSchedule(w http.ResponseWriter, r *http.Request) {
	var req BackupSchedule
	if !decode(w, r, &req) {
		return
	}
	sched := domain.BackupSchedule{Kind: domain.BackupKind(req.Kind), Keep: req.Keep, At: deref(req.At), Zone: deref(req.Zone)}
	if req.EveryHours != nil {
		sched.EveryHours = *req.EveryHours
	}
	st, err := s.svc.SetBackupSchedule(r.Context(), state(r).principal, sched)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackupStatus(st))
}

// SetBackupRecipients stores the recipients.
func (s *Server) SetBackupRecipients(w http.ResponseWriter, r *http.Request) {
	var req BackupRecipientsInput
	if !decode(w, r, &req) {
		return
	}
	st, err := s.svc.SetBackupRecipients(r.Context(), state(r).principal, req.Recipients)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackupStatus(st))
}

// RunBackup backs up now.
func (s *Server) RunBackup(w http.ResponseWriter, r *http.Request) {
	f, err := s.svc.BackupNow(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackupFile(f))
}

// DownloadBackup sends one encrypted backup file as an attachment. Every error is answered
// as problem+json before the first byte of the file.
func (s *Server) DownloadBackup(w http.ResponseWriter, r *http.Request, name string) {
	rc, f, err := s.svc.OpenBackup(r.Context(), state(r).principal, name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rc.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	// The name parsed as a backup name: ASCII letters, digits, '-', '.' only.
	h.Set("Content-Disposition", `attachment; filename="`+f.Name+`"`)
	h.Set("Content-Length", strconv.FormatInt(f.Size, 10))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		s.log.WarnContext(r.Context(), "backup download not completed", "file", f.Name, "err", err)
	}
}
