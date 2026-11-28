package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filippo.io/age"
)

type countingBackups struct {
	calls atomic.Int32
	err   error
}

func (c *countingBackups) RunDueBackup(context.Context) (bool, error) {
	c.calls.Add(1)
	return c.err == nil, c.err
}

// syncBuffer is a log sink safe for the loop's goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestBackupLoop: the scheduler calls the use case first after the start-up wait, then each
// tick; an error is logged and the loop goes on; it stops with its context.
func TestBackupLoop(t *testing.T) {
	for _, err := range []error{nil, errors.New("disk full")} {
		s := &countingBackups{err: err}
		var logs syncBuffer
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			backupLoop(ctx, s, slog.New(slog.NewTextHandler(&logs, nil)), time.Millisecond, time.Millisecond)
			close(done)
		}()
		deadline := time.Now().Add(5 * time.Second)
		for s.calls.Load() < 3 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		cancel()
		<-done
		if s.calls.Load() < 3 {
			t.Fatalf("calls = %d", s.calls.Load())
		}
		if err != nil && !strings.Contains(logs.String(), "disk full") {
			t.Fatalf("the error was not logged: %s", logs.String())
		}
		if err == nil && !strings.Contains(logs.String(), "scheduled backup written") {
			t.Fatalf("the run was not logged: %s", logs.String())
		}
	}
}

func TestOpenBackupFolder(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	base := t.TempDir()
	cfg := config{DataDir: base, BackupDir: filepath.Join(base, "a", "backups")}
	if f := openBackupFolder(cfg, nil, log); f == nil || f.Folder() != cfg.BackupDir {
		t.Fatal("folder not opened")
	}
	if fi, err := os.Stat(cfg.BackupDir); err != nil || !fi.IsDir() || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700) {
		t.Fatalf("folder %v %v", fi, err)
	}
	blocker := filepath.Join(base, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f := openBackupFolder(config{DataDir: base, BackupDir: filepath.Join(blocker, "sub")}, nil, log); f != nil {
		t.Fatal("an unusable folder was opened")
	}
}

func TestDeploymentBackupRecipients(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	r := id.Recipient().String()
	if got := deploymentBackupRecipients(envOf(map[string]string{envBackupRecipient: r}), log); len(got) != 1 || got[0] != r {
		t.Fatalf("recipients %v", got)
	}
	if got := deploymentBackupRecipients(envOf(map[string]string{envBackupRecipient: "nonsense"}), log); got != nil {
		t.Fatalf("bad recipient kept: %v", got)
	}
	if got := deploymentBackupRecipients(envOf(nil), log); got != nil {
		t.Fatalf("none: %v", got)
	}
}

func TestDeploymentTimeZone(t *testing.T) {
	if got := deploymentTimeZone(envOf(map[string]string{"TZ": "Europe/London"})); got != "Europe/London" {
		t.Fatalf("TZ: %q", got)
	}
	if got := deploymentTimeZone(envOf(map[string]string{"TZ": ":Asia/Tokyo"})); got != "Asia/Tokyo" {
		t.Fatalf("TZ with a colon: %q", got)
	}
	if got := deploymentTimeZone(envOf(map[string]string{"TZ": "Mars/Olympus"})); got == "Mars/Olympus" {
		t.Fatal("an unknown zone was used")
	}
}
