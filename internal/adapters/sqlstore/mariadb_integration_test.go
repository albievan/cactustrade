//go:build integration

package sqlstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/mariadb"

	"github.com/albievan/cactustrade/internal/adapters/sqlstore"
	"github.com/albievan/cactustrade/internal/adapters/sqlstore/storetest"
)

// TestMariaDBStore runs the shared repository suite against MariaDB 11, with migrations
// up, down to zero and up again first (make test-int; needs Docker).
func TestMariaDBStore(t *testing.T) {
	ctx := context.Background()
	c, err := mariadb.Run(ctx, "mariadb:11",
		mariadb.WithDatabase("cactustrade"), mariadb.WithUsername("cactustrade"), mariadb.WithPassword("test-only-password"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatal(err)
	}
	octx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	s, err := sqlstore.OpenMariaDB(octx, sqlstore.MariaDBConfig{
		Host: host + ":" + port.Port(), Name: "cactustrade", User: "cactustrade", Password: "test-only-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
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
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Driver() != sqlstore.DriverMariaDB {
		t.Fatal("driver")
	}
	storetest.Run(t, s)
	t.Run("reset_classifies_every_table", func(t *testing.T) {
		// TestResetClassifiesEveryTable for MariaDB: Counts covers exactly the schema's tables.
		c, err := s.DataReset().Counts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		schema := tableNames(t, s.DB())
		if len(c) != len(schema) {
			t.Fatalf("the reset knows %d tables, the schema has %d: %v", len(c), len(schema), schema)
		}
		for _, n := range schema {
			if _, ok := c[n]; !ok {
				t.Errorf("table %s is neither wiped nor kept by a data reset", n)
			}
		}
	})
}
