package sqlstore

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

// TestResetClassifiesEveryTable: every table of the migrated schema is either wiped or kept
// by a data reset (PLAN R49.2), and Counts covers exactly those tables. A migration that adds
// a table fails here until the table is classified in reset.go. The MariaDB schema is checked
// by TestMariaDBResetClassifiesEveryTable (make test-int).
func TestResetClassifiesEveryTable(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.DB().QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var schema []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		schema = append(schema, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var classified []string
	for _, tb := range resetWiped {
		if tb.count != "SELECT COUNT(*) FROM "+tb.name || tb.wipe != "DELETE FROM "+tb.name {
			t.Errorf("wiped %s: statements %q, %q", tb.name, tb.count, tb.wipe)
		}
		classified = append(classified, tb.name)
	}
	for _, tb := range resetKept {
		if tb.count != "SELECT COUNT(*) FROM "+tb.name {
			t.Errorf("kept %s: statement %q", tb.name, tb.count)
		}
		if tb.wipe != "" && (tb.name != "watchlist" || tb.wipe != "DELETE FROM watchlist") {
			t.Errorf("kept %s is wiped by %q: only the watchlist is, and only when asked", tb.name, tb.wipe)
		}
		if slices.Contains(classified, tb.name) {
			t.Errorf("%s is both wiped and kept", tb.name)
		}
		classified = append(classified, tb.name)
	}
	for _, name := range schema {
		if !slices.Contains(classified, name) {
			t.Errorf("table %s is neither wiped nor kept by a data reset: classify it in reset.go", name)
		}
	}
	slices.Sort(classified)
	if !slices.Equal(schema, classified) {
		t.Fatalf("the schema's tables %v\nthe reset's tables  %v", schema, classified)
	}
	for _, keep := range []string{"risk_state", "real_gates", "audit_log", "users", "accounts", "instrument_matches", "t212_instruments"} {
		if slices.ContainsFunc(resetWiped, func(tb resetTable) bool { return tb.name == keep }) {
			t.Errorf("%s must be kept", keep)
		}
	}
	var wiped []string
	for _, tb := range resetWiped {
		wiped = append(wiped, tb.name)
	}
	if !slices.Equal(wiped, domain.DataResetWipedTables()) {
		t.Fatalf("wiped %v, the domain says %v", wiped, domain.DataResetWipedTables())
	}
	c, err := s.DataReset().Counts(ctx)
	if err != nil || len(c) != len(schema) {
		t.Fatalf("Counts = %v %v", c, err)
	}
}
