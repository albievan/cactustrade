package migrations

import (
	"io/fs"
	"regexp"
	"slices"
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
