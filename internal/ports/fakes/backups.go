package fakes

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Backups is an in-memory backup folder (ports.Backups) with failure injection.
type Backups struct {
	mu sync.Mutex
	// Dir and DB are what Folder and Driver answer ("/backups" and "sqlite" by default).
	Dir, DB string
	// WriteErr, ListErr and RemoveErr make those calls fail.
	WriteErr, ListErr, RemoveErr error
	// OnWrite, when set, runs inside Write before the file exists (a test can block there).
	OnWrite func()
	// Content is the bytes of every written file ("backup" by default).
	Content []byte
	files   map[string][]byte
	// Writes records the recipients of every Write, in order.
	Writes [][]string
}

var _ ports.Backups = (*Backups)(nil)

// Folder returns Dir.
func (b *Backups) Folder() string {
	if b.Dir == "" {
		return "/backups"
	}
	return b.Dir
}

// Driver returns DB.
func (b *Backups) Driver() string {
	if b.DB == "" {
		return "sqlite"
	}
	return b.DB
}

// ParseRecipients accepts age1… strings without spaces (the fake does not check the key).
func (b *Backups) ParseRecipients(rs []string) error {
	if len(rs) == 0 {
		return domain.Invalid("recipients", "add at least one age public key")
	}
	for _, r := range rs {
		if !strings.HasPrefix(r, "age1") || strings.ContainsAny(r, " \t\r\n") || len(r) < 10 {
			return domain.Invalid("recipients", "not an age public key (age1…)")
		}
	}
	return nil
}

// Write stores a file named for at (a clash adds a suffix).
func (b *Backups) Write(_ context.Context, rs []string, at time.Time) (domain.BackupFile, error) {
	if err := b.ParseRecipients(rs); err != nil {
		return domain.BackupFile{}, err
	}
	if b.OnWrite != nil {
		b.OnWrite()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Writes = append(b.Writes, slices.Clone(rs))
	if b.WriteErr != nil {
		return domain.BackupFile{}, b.WriteErr
	}
	ext := domain.BackupExtSQLite
	if b.Driver() == "mariadb" {
		ext = domain.BackupExtMariaDB
	}
	if b.files == nil {
		b.files = map[string][]byte{}
	}
	name, err := domain.BackupName(at, ext, "")
	for i := 0; err == nil && b.files[name] != nil; i++ {
		name, err = domain.BackupName(at, ext, fmt.Sprintf("%04x", i))
	}
	if err != nil {
		return domain.BackupFile{}, err
	}
	c := b.Content
	if c == nil {
		c = []byte("backup")
	}
	b.files[name] = slices.Clone(c)
	return b.file(name), nil
}

// Put adds a file directly (any name; List shows only names that parse, as the real folder).
func (b *Backups) Put(name string, content []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.files == nil {
		b.files = map[string][]byte{}
	}
	b.files[name] = slices.Clone(content)
}

// Names returns every stored name, sorted (parsed or not).
func (b *Backups) Names() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.files))
	for n := range b.files {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func (b *Backups) file(name string) domain.BackupFile {
	p, _ := domain.ParseBackupName(name)
	return domain.BackupFile{Name: name, Size: int64(len(b.files[name])), At: p.At}
}

// List returns the files whose names parse, newest first.
func (b *Backups) List(context.Context) ([]domain.BackupFile, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ListErr != nil {
		return nil, b.ListErr
	}
	var out []domain.BackupFile
	for n := range b.files {
		if _, ok := domain.ParseBackupName(n); ok {
			out = append(out, b.file(n))
		}
	}
	slices.SortFunc(out, func(x, y domain.BackupFile) int {
		if c := y.At.Compare(x.At); c != 0 {
			return c
		}
		return strings.Compare(y.Name, x.Name)
	})
	return out, nil
}

// Remove deletes a file whose name parses.
func (b *Backups) Remove(_ context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.RemoveErr != nil {
		return b.RemoveErr
	}
	if _, ok := domain.ParseBackupName(name); !ok || b.files[name] == nil {
		return domain.ErrNotFound
	}
	delete(b.files, name)
	return nil
}

// Open returns a reader over a file whose name parses.
func (b *Backups) Open(_ context.Context, name string) (io.ReadCloser, domain.BackupFile, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := domain.ParseBackupName(name); !ok || b.files[name] == nil {
		return nil, domain.BackupFile{}, domain.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(slices.Clone(b.files[name]))), b.file(name), nil
}
