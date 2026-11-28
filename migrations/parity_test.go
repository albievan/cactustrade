package migrations

import (
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var versionRE = regexp.MustCompile(`^(\d{5})_[a-z0-9_]+\.sql$`)

func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := fs.ReadDir(FS, dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		if !versionRE.MatchString(e.Name()) {
			t.Errorf("%s/%s: migrations are named 00001_snake_case.sql", dir, e.Name())
		}
		out = append(out, e.Name())
	}
	return out
}

// TestMigrationParity: every schema change exists in both dialects with the same name, and
// each file has Up and Down sections (CLAUDE.md §3: both databases, always).
func TestMigrationParity(t *testing.T) {
	s, m := names(t, "sqlite"), names(t, "mariadb")
	if !slices.Equal(s, m) {
		t.Fatalf("sqlite and mariadb migrations differ:\n sqlite:  %v\n mariadb: %v", s, m)
	}
	for _, dir := range []string{"sqlite", "mariadb"} {
		for _, n := range s {
			b, _ := fs.ReadFile(FS, dir+"/"+n)
			if !strings.Contains(string(b), "-- +goose Up") || !strings.Contains(string(b), "-- +goose Down") {
				t.Errorf("%s/%s needs -- +goose Up and -- +goose Down", dir, n)
			}
		}
	}
}

// oneStatementFrom is the first version held to one statement per migration: MariaDB commits
// every DDL statement implicitly, so a migration with two could stop half applied with no
// goose version recorded (re-review 2026-11-28, minor 4; the earlier ones are deployed).
const oneStatementFrom = 17

// TestMigrationOneStatement: from oneStatementFrom on, the Up section of every migration
// holds exactly one SQL statement, in both dialects.
func TestMigrationOneStatement(t *testing.T) {
	for _, dir := range []string{"sqlite", "mariadb"} {
		for _, n := range names(t, dir) {
			m := versionRE.FindStringSubmatch(n)
			if m == nil {
				continue
			}
			if v, err := strconv.Atoi(m[1]); err != nil || v < oneStatementFrom {
				continue
			}
			b, _ := fs.ReadFile(FS, dir+"/"+n)
			up, _, _ := strings.Cut(string(b), "-- +goose Down")
			stmts := 0
			for line := range strings.SplitSeq(up, "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "--") && strings.HasSuffix(line, ";") {
					stmts++
				}
			}
			if stmts != 1 {
				t.Errorf("%s/%s: %d statements in its Up section, want 1 (MariaDB commits each DDL statement)", dir, n, stmts)
			}
		}
	}
}
