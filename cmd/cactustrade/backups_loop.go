package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/adapters/backup"
)

// backupRunner is the use case the backup loop runs (app.Service).
type backupRunner interface {
	RunDueBackup(ctx context.Context) (bool, error)
}

// backupLoop is the in-process backup scheduler (R48): once first after the start-up
// settles (a slot missed while stopped runs then, once), then every interval. A failed
// backup is logged here; the use case audits it and raises a warning event. Trading never
// waits for it.
func backupLoop(ctx context.Context, svc backupRunner, log *slog.Logger, first, every time.Duration) {
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if ran, err := svc.RunDueBackup(ctx); err != nil && ctx.Err() == nil {
				log.WarnContext(ctx, "scheduled backup failed", "err", err)
			} else if ran {
				log.InfoContext(ctx, "scheduled backup written")
			}
			t.Reset(every)
		}
	}
}

// openBackupFolder makes the backup folder ready (standalone: created 0700; in a container
// it is the mount deploy.sh prepared) and returns it. A folder that cannot be made leaves
// backups from the app unavailable (logged); trading is not affected.
func openBackupFolder(cfg config, src backup.Source, log *slog.Logger) *backup.Folder {
	if err := os.MkdirAll(cfg.BackupDir, 0o700); err != nil {
		log.Warn("backup folder not usable: backups from the app are unavailable", "folder", cfg.BackupDir, "err", err)
		return nil
	}
	return backup.NewFolder(src, cfg.DataDir, cfg.BackupDir, cfg.BackupGroupRead)
}

// deploymentBackupRecipients reads the deployment's backup recipients
// (CACTUSTRADE_BACKUP_RECIPIENT, else the file CACTUSTRADE_BACKUP_RECIPIENT_FILE): the
// fallback while none is entered in the app. Unusable ones are logged and ignored (the app
// then asks for a recipient; nothing is written unencrypted).
func deploymentBackupRecipients(getenv func(string) string, log *slog.Logger) []string {
	rs, err := backupRecipients(nil, getenv)
	if err != nil {
		if getenv(envBackupRecipient) != "" || getenv(envBackupRecipientFile) != "" {
			log.Warn("the deployment's backup recipients are not usable", "err", err)
		}
		return nil
	}
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		if s, ok := r.(interface{ String() string }); ok {
			out = append(out, strings.TrimSpace(s.String()))
		}
	}
	return out
}

// deploymentTimeZone is the deployment's IANA time zone (TZ, else the system's), the
// default zone of the backup schedule; "" = UTC.
func deploymentTimeZone(getenv func(string) string) string {
	if tz := strings.TrimPrefix(strings.TrimSpace(getenv("TZ")), ":"); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if n := time.Local.String(); n != "Local" && n != "" {
		return n
	}
	return ""
}
