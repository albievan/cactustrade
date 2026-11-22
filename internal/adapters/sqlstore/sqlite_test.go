package sqlstore_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/albievan/cactustrade/internal/adapters/sqlstore"
	"github.com/albievan/cactustrade/internal/adapters/sqlstore/storetest"
)

func TestSQLiteStore(t *testing.T) {
	ctx := context.Background()
	s, err := sqlstore.OpenSQLite(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil { // idempotent
		t.Fatal(err)
	}
	if s.Driver() != sqlstore.DriverSQLite || s.DB() == nil {
		t.Fatal("driver")
	}
	storetest.Run(t, s)
}

func TestSQLiteMigrateDownUp(t *testing.T) {
	ctx := context.Background()
	s, err := sqlstore.OpenSQLite(ctx, filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.Migrator()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if v, err := p.GetDBVersion(ctx); err != nil || v < 1 {
		t.Fatalf("version %d %v", v, err)
	}
}
