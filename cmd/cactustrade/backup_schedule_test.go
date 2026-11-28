package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// scheduleEnv runs deploy.sh with a throwaway HOME, .env.local and fake launchctl / crontab
// (they record their arguments and stdin), so nothing on this machine is scheduled.
type scheduleEnv struct {
	root, home, envLocal, calls, cronTab string
}

func newScheduleEnv(t *testing.T) *scheduleEnv {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	e := &scheduleEnv{
		root: root, home: filepath.Join(dir, "home"), envLocal: filepath.Join(dir, "env.local"),
		calls: filepath.Join(dir, "calls"), cronTab: filepath.Join(dir, "crontab"),
	}
	bin := filepath.Join(dir, "bin")
	for _, d := range []string{e.home, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fake := map[string]string{
		"launchctl": `echo "launchctl $*" >>"` + e.calls + `"`,
		"crontab": `if [ "$1" = "-l" ]; then cat "` + e.cronTab + `" 2>/dev/null; exit 0; fi
cat >"` + e.cronTab + `"; echo "crontab $*" >>"` + e.calls + `"`,
	}
	for name, body := range fake {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return e
}

func (e *scheduleEnv) run(t *testing.T, scheduler string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", append([]string{filepath.Join(e.root, "deploy.sh")}, args...)...)
	cmd.Env = append(os.Environ(), "HOME="+e.home, "CACTUSTRADE_ENV_LOCAL="+e.envLocal, "CACTUSTRADE_SCHEDULER="+scheduler)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// TestBackupScheduleLaunchd: a daily schedule writes the launchd job (the time, deploy.sh
// --backup-now for the target, the log in the chosen folder) and saves the folder and
// retention in .env.local; "off" removes it.
func TestBackupScheduleLaunchd(t *testing.T) {
	e := newScheduleEnv(t)
	backups := filepath.Join(t.TempDir(), "my backups")
	out, err := e.run(t, "launchd", "--backup-schedule", "daily 02:30", "--backup-dir", backups, "--backup-keep", "14", "--dockerdesktop")
	if err != nil {
		t.Fatalf("schedule: %v\n%s", err, out)
	}
	plist := read(t, filepath.Join(e.home, "Library", "LaunchAgents", "com.cactustrade.backup.plist"))
	for _, want := range []string{
		"<key>Hour</key><integer>2</integer><key>Minute</key><integer>30</integer>",
		"<string>" + filepath.Join(e.root, "deploy.sh") + "</string><string>--backup-now</string><string>--dockerdesktop</string>",
		"<string>" + filepath.Join(backups, "backup-schedule.log") + "</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %s:\n%s", want, plist)
		}
	}
	env := read(t, e.envLocal)
	for _, want := range []string{`BACKUP_DIR="` + backups + `"`, `BACKUP_KEEP="14"`, `BACKUP_SCHEDULE="daily 02:30"`} {
		if !strings.Contains(env, want) {
			t.Errorf(".env.local lacks %s:\n%s", want, env)
		}
	}
	if fi, err := os.Stat(backups); err != nil || !fi.IsDir() {
		t.Errorf("backup folder not created: %v", err)
	}
	if calls := read(t, e.calls); !strings.Contains(calls, "launchctl bootstrap gui/") {
		t.Errorf("launchd not loaded: %s", calls)
	}
	// every 6 h, then off
	if out, err := e.run(t, "launchd", "--backup-schedule", "every 6h", "--dockerdesktop"); err != nil {
		t.Fatalf("every 6h: %v\n%s", err, out)
	}
	if p := read(t, filepath.Join(e.home, "Library", "LaunchAgents", "com.cactustrade.backup.plist")); !strings.Contains(p, "<key>StartInterval</key><integer>21600</integer>") ||
		!strings.Contains(p, filepath.Join(backups, "backup-schedule.log")) {
		t.Errorf("every 6h plist (folder kept from .env.local):\n%s", p)
	}
	if out, err := e.run(t, "launchd", "--backup-schedule", "off", "--dockerdesktop"); err != nil || !strings.Contains(out, "scheduled backups are off") {
		t.Fatalf("off: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(e.home, "Library", "LaunchAgents", "com.cactustrade.backup.plist")); !os.IsNotExist(err) {
		t.Errorf("plist still there after off: %v", err)
	}
}

// TestBackupScheduleCron: on Linux the schedule is one marked crontab line, replaced on a
// change and removed by "off"; other lines stay.
func TestBackupScheduleCron(t *testing.T) {
	e := newScheduleEnv(t)
	if err := os.WriteFile(e.cronTab, []byte("5 4 * * * /usr/bin/other-job\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backups := t.TempDir()
	if out, err := e.run(t, "cron", "--backup-schedule", "daily 3:05", "--backup-dir", backups, "--podman"); err != nil {
		t.Fatalf("schedule: %v\n%s", err, out)
	}
	if out, err := e.run(t, "cron", "--backup-schedule", "hourly", "--podman"); err != nil {
		t.Fatalf("hourly: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(read(t, e.cronTab)), "\n")
	if len(lines) != 2 || lines[0] != "5 4 * * * /usr/bin/other-job" || !strings.HasPrefix(lines[1], "0 */1 * * * ") ||
		!strings.Contains(lines[1], "deploy.sh --backup-now --podman") || !strings.HasSuffix(lines[1], "# cactustrade-backup") {
		t.Fatalf("crontab:\n%s", strings.Join(lines, "\n"))
	}
	if out, err := e.run(t, "cron", "--backup-schedule", "off", "--podman"); err != nil {
		t.Fatalf("off: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(read(t, e.cronTab)); got != "5 4 * * * /usr/bin/other-job" {
		t.Fatalf("crontab after off:\n%s", got)
	}
}

func TestBackupScheduleRefusals(t *testing.T) {
	e := newScheduleEnv(t)
	for _, when := range []string{"daily 25:00", "daily 2pm", "every 0h", "every 30h", "weekly", "--yes"} {
		if out, err := e.run(t, "launchd", "--backup-schedule", when, "--dockerdesktop"); err == nil {
			t.Errorf("%q accepted:\n%s", when, out)
		}
	}
	if out, err := e.run(t, "launchd", "--backup-schedule", "hourly", "--dockerdesktop", "--ssh-access", "root@192.0.2.1"); err == nil {
		t.Errorf("remote schedule accepted:\n%s", out)
	}
	if out, err := e.run(t, "launchd", "--backup-now", "--backup-keep", "x", "--dockerdesktop"); err == nil {
		t.Errorf("--backup-keep x accepted:\n%s", out)
	}
}

// TestBackupPrune keeps the newest BACKUP_KEEP of cactusTrade's own backups and touches no
// other file.
func TestBackupPrune(t *testing.T) {
	e := newScheduleEnv(t)
	dir := t.TempDir()
	names := []string{
		"cactustrade-20261125T010000Z.sql.gz", "cactustrade-20261126T010000Z.sql.gz.age",
		"cactustrade-20261127T010000Z.sql.gz", "cactustrade-20261127T020000Z.sql.gz.age",
		"notes.txt", "backup-schedule.log",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := e.run(t, "launchd", "--backup-prune", "--backup-dir", dir, "--backup-keep", "2", "--dockerdesktop"); err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, en := range entries {
		left = append(left, en.Name())
	}
	slices.Sort(left)
	want := []string{"backup-schedule.log", "cactustrade-20261127T010000Z.sql.gz", "cactustrade-20261127T020000Z.sql.gz.age", "notes.txt"}
	if !slices.Equal(left, want) {
		t.Fatalf("left %v, want %v", left, want)
	}
	// keep 0 = keep all
	if out, err := e.run(t, "launchd", "--backup-prune", "--backup-dir", dir, "--backup-keep", "0", "--dockerdesktop"); err != nil {
		t.Fatalf("prune 0: %v\n%s", err, out)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 4 {
		t.Fatalf("keep 0 removed files: %d left", len(entries))
	}
}

// TestBackupPruneIgnoresAppFolder (R48.8): deploy.sh's pruning keeps to BACKUP_DIR itself;
// the app's own backups in BACKUP_DIR/app (pruned by the app) are never touched.
func TestBackupPruneIgnoresAppFolder(t *testing.T) {
	e := newScheduleEnv(t)
	dir := t.TempDir()
	app := filepath.Join(dir, "app")
	if err := os.Mkdir(app, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(dir, "cactustrade-20261125T010000Z.sql.gz.age"), filepath.Join(dir, "cactustrade-20261126T010000Z.sql.gz.age"),
		filepath.Join(app, "cactustrade-20201125T010000Z.sql.gz.age"), filepath.Join(app, "cactustrade-20201126T010000Z.sql.gz.age"),
	} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := e.run(t, "launchd", "--backup-prune", "--backup-dir", dir, "--backup-keep", "1", "--dockerdesktop"); err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	if left, _ := os.ReadDir(app); len(left) != 2 {
		t.Fatalf("the app's folder was pruned: %d left", len(left))
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "cactustrade-*")); len(left) != 1 {
		t.Fatalf("deploy.sh's own backups: %v", left)
	}
}

// TestBackupDirChangeNamesUpgrade (R48.8): a new --backup-dir says that --upgrade moves the
// app's backup mount (Docker/Podman).
func TestBackupDirChangeNamesUpgrade(t *testing.T) {
	e := newScheduleEnv(t)
	dir := filepath.Join(t.TempDir(), "elsewhere")
	out, err := e.run(t, "launchd", "--backup-schedule", "off", "--backup-dir", dir, "--dockerdesktop")
	if err != nil || !strings.Contains(out, "run ./deploy.sh --upgrade --dockerdesktop to move the app's backup mount") {
		t.Fatalf("schedule: %v\n%s", err, out)
	}
}
