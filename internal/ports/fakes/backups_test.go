package fakes

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

func TestFakeBackups(t *testing.T) {
	ctx := context.Background()
	b := &Backups{}
	at := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := b.Write(ctx, nil, at); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("no recipient: %v", err)
	}
	f1, err := b.Write(ctx, []string{"age1recipientkey"}, at)
	if err != nil || f1.Name != "cactustrade-20270102T030405Z.db.age" || f1.Size != 6 {
		t.Fatalf("write: %+v %v", f1, err)
	}
	f2, err := b.Write(ctx, []string{"age1recipientkey"}, at)
	if err != nil || f2.Name == f1.Name {
		t.Fatalf("a clash must not overwrite: %+v %v", f2, err)
	}
	b.Put("notes.txt", []byte("x"))
	list, err := b.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %v %v", list, err)
	}
	rc, f, err := b.Open(ctx, f1.Name)
	if err != nil || f.Name != f1.Name {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(data) != "backup" {
		t.Fatalf("content %q", data)
	}
	if _, _, err := b.Open(ctx, "notes.txt"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("open unparsed: %v", err)
	}
	if err := b.Remove(ctx, f1.Name); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(ctx, f1.Name); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("remove twice: %v", err)
	}
	b.WriteErr = errors.New("disk full")
	if _, err := b.Write(ctx, []string{"age1recipientkey"}, at); err == nil {
		t.Fatal("injected failure ignored")
	}
	if len(b.Names()) != 2 || b.Folder() != "/backups" || b.Driver() != "sqlite" || len(b.Writes) != 3 {
		t.Fatalf("names %v writes %d", b.Names(), len(b.Writes))
	}
}
