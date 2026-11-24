package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Data-folder locks (PLAN 7.6a). A standalone server and a restore must never use the same
// SQLite database at once: each announces itself with a file in <data>/locks whose
// modification time it refreshes every lockBeat. A file younger than lockFresh belongs to a
// live process; an older one was left by a process that died, and is ignored.
//
// Both sides first write their own file and then look for the other's, so of two that start
// together at least one sees the other and gives up. The files are portable (no flock), so
// the same rule holds on Linux, macOS and Windows.
const (
	lockBeat  = 5 * time.Second
	lockFresh = 20 * time.Second

	lockDirName     = "locks"
	restoreLockName = "restore.lock"
	serveLockPrefix = "serve-"
)

var (
	// errServerRunning refuses a restore while a server uses the data folder.
	errServerRunning = errors.New("a cactusTrade server is using this data folder: stop it first")
	// errRestoreRunning refuses to serve (or restore) while a restore is replacing the database.
	errRestoreRunning = errors.New("a restore is replacing the database in this data folder: wait for it to finish")
)

// dataLock is one process's lock file, kept fresh until release.
type dataLock struct {
	path string
	stop chan struct{}
	once sync.Once
	wg   sync.WaitGroup
}

// release stops the heartbeat and removes the file.
func (l *dataLock) release() {
	l.once.Do(func() {
		close(l.stop)
		l.wg.Wait()
		_ = os.Remove(l.path)
	})
}

// lockServe announces a server in dataDir and refuses while a restore is running there.
func lockServe(dataDir string, now func() time.Time) (*dataLock, error) {
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return nil, fmt.Errorf("data folder lock: %w", err)
	}
	l, err := createLock(dataDir, serveLockPrefix+hex.EncodeToString(id)+".lock", now)
	if err != nil {
		return nil, err
	}
	busy, err := freshLocks(dataDir, func(n string) bool { return n == restoreLockName }, now)
	if err != nil || busy > 0 {
		l.release()
		if err != nil {
			return nil, err
		}
		return nil, errRestoreRunning
	}
	return l, nil
}

// lockRestore announces a restore in dataDir and refuses while a server (or another restore)
// uses it.
func lockRestore(dataDir string, now func() time.Time) (*dataLock, error) {
	l, err := createLock(dataDir, restoreLockName, now)
	if errors.Is(err, fs.ErrExist) {
		return nil, errRestoreRunning
	}
	if err != nil {
		return nil, err
	}
	busy, err := freshLocks(dataDir, func(n string) bool { return strings.HasPrefix(n, serveLockPrefix) }, now)
	if err != nil || busy > 0 {
		l.release()
		if err != nil {
			return nil, err
		}
		return nil, errServerRunning
	}
	return l, nil
}

// createLock writes the lock file name (failing with fs.ErrExist while a fresh one exists;
// a stale one is replaced) and starts its heartbeat.
func createLock(dataDir, name string, now func() time.Time) (*dataLock, error) {
	dir := filepath.Join(dataDir, lockDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("data folder lock: %w", err)
	}
	path := filepath.Join(dir, name)
	for attempt := 0; ; attempt++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, werr := fmt.Fprintf(f, "pid %d\nsince %s\n", os.Getpid(), now().UTC().Format(time.RFC3339))
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("data folder lock: %w", werr)
			}
			break
		}
		if !errors.Is(err, fs.ErrExist) || attempt > 0 {
			return nil, fmt.Errorf("data folder lock: %w", err)
		}
		st, serr := os.Stat(path)
		if serr == nil && now().Sub(st.ModTime()) < lockFresh {
			return nil, fmt.Errorf("data folder lock %s: %w", name, fs.ErrExist)
		}
		_ = os.Remove(path) // left by a process that died
	}
	l := &dataLock{path: path, stop: make(chan struct{})}
	if err := os.Chtimes(path, now(), now()); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("data folder lock: %w", err)
	}
	l.wg.Go(func() {
		t := time.NewTicker(lockBeat)
		defer t.Stop()
		for {
			select {
			case <-l.stop:
				return
			case <-t.C:
				_ = os.Chtimes(path, now(), now())
			}
		}
	})
	return l, nil
}

// freshLocks counts the fresh lock files in dataDir whose names match.
func freshLocks(dataDir string, match func(string) bool, now func() time.Time) (int, error) {
	entries, err := os.ReadDir(filepath.Join(dataDir, lockDirName))
	if err != nil {
		return 0, fmt.Errorf("data folder lock: %w", err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !match(e.Name()) {
			continue
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue // released meanwhile
		}
		if err != nil {
			return 0, fmt.Errorf("data folder lock: %w", err)
		}
		if now().Sub(info.ModTime()) < lockFresh {
			n++
		}
	}
	return n, nil
}
