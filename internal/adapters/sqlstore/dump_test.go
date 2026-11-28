package sqlstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDumpValueEncoding(t *testing.T) {
	every := make([]byte, 256)
	for i := range every {
		every[i] = byte(i)
	}
	tests := []struct {
		name string
		kind dumpKind
		v    any
		want string
	}{
		{"NULL text", kindText, nil, "NULL"},
		{"NULL blob", kindBinary, nil, "NULL"},
		{"NULL number", kindNumber, nil, "NULL"},
		{"empty text", kindText, []byte{}, "''"},
		{"empty blob", kindBinary, []byte{}, "X''"},
		{"every byte in a blob", kindBinary, every, "X'" + hex.EncodeToString(every) + "'"},
		{"quotes", kindText, []byte(`it's "x"`), `'it\'s "x"'`},
		{"backslash", kindText, []byte(`a\b`), `'a\\b'`},
		{"NUL", kindText, []byte("a\x00b"), `'a\0b'`},
		{"ctrl-Z", kindText, []byte("a\x1ab"), `'a\Zb'`},
		{"newlines", kindText, []byte("a\nb\rc\td"), "'a\\nb\\rc\td'"},
		{"UTF-8", kindText, []byte("Zürich – 東京 🌵"), "'Zürich – 東京 🌵'"},
		{"injection attempt", kindText, []byte(`'); DROP TABLE users; --`), `'\'); DROP TABLE users; --'`},
		{"number bytes", kindNumber, []byte("-12345678901234"), "-12345678901234"},
		{"decimal", kindNumber, []byte("12.50"), "12.50"},
		{"exponent", kindNumber, []byte("1.5e-07"), "1.5e-07"},
		{"int64", kindNumber, int64(-7), "-7"},
		{"uint64", kindNumber, uint64(18446744073709551615), "18446744073709551615"},
		{"string value", kindText, "x'y", `'x\'y'`},
		{"time", kindText, time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC), "'2027-01-02 03:04:05'"},
		{"bool", kindNumber, true, "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := encodeValue(tt.kind, tt.v)
			if err != nil || got != tt.want {
				t.Fatalf("encodeValue = %q %v, want %q", got, err, tt.want)
			}
		})
	}
	for _, bad := range []string{"1; DROP TABLE x", "", "0x10", "1-2", "abc"} {
		if got, err := encodeValue(kindNumber, []byte(bad)); err == nil {
			t.Errorf("number %q encoded as %q, want an error", bad, got)
		}
	}
	if _, err := encodeValue(kindText, struct{}{}); err == nil {
		t.Error("an unexpected type must fail")
	}
}

// TestDumpEscapedTextHasNoBareSpecials: an escaped text literal never contains a bare quote,
// NUL, newline, carriage return or ctrl-Z, so it cannot end early or break a line.
func TestDumpEscapedTextHasNoBareSpecials(t *testing.T) {
	every := make([]byte, 256)
	for i := range every {
		every[i] = byte(i)
	}
	q := quoteText(every)
	inner := q[1 : len(q)-1]
	for i := 0; i < len(inner); i++ {
		switch inner[i] {
		case '\\':
			i++ // the escaped character
		case '\'', 0, '\n', '\r', 0x1a:
			t.Fatalf("bare byte %#x at %d", inner[i], i)
		}
	}
}

func TestDumpKindOf(t *testing.T) {
	for name, want := range map[string]dumpKind{
		"BIGINT": kindNumber, "UNSIGNED BIGINT": kindNumber, "TINYINT": kindNumber, "INT": kindNumber,
		"DECIMAL": kindNumber, "DOUBLE": kindNumber, "YEAR": kindNumber,
		"BLOB": kindBinary, "MEDIUMBLOB": kindBinary, "VARBINARY": kindBinary, "BINARY": kindBinary, "BIT": kindBinary,
		"VARCHAR": kindText, "TEXT": kindText, "MEDIUMTEXT": kindText, "LONGTEXT": kindText, "CHAR": kindText,
		"DATETIME": kindText, "TIMESTAMP": kindText, "JSON": kindText, "ENUM": kindText, "POINT": kindText,
	} {
		if got := kindOf(name); got != want {
			t.Errorf("kindOf(%s) = %d, want %d", name, got, want)
		}
	}
}

// TestDumpInsertBatching: rows share an INSERT up to the statement limit; a row with a value
// over a chunk is written alone, its value built in a user variable chunk by chunk (never a
// character split, never a statement near max_allowed_packet).
func TestDumpInsertBatching(t *testing.T) {
	var out bytes.Buffer
	w := bufio.NewWriter(&out)
	iw := insertWriter{w: w, table: "`t`"}
	kinds := []dumpKind{kindNumber, kindText, kindBinary}
	small := strings.Repeat("a", 100<<10)
	for i := range 25 { // 25 × ~100 KiB: three statements of ≤ 1 MiB
		if err := iw.row(kinds, []any{[]byte{byte('0' + i%10)}, []byte(small), []byte{1}}); err != nil {
			t.Fatal(err)
		}
	}
	bigText := strings.Repeat("ü", 300<<10) // 600 KiB of two-byte characters
	bigBlob := bytes.Repeat([]byte{0, 0xff, '\''}, 200<<10)
	if err := iw.row(kinds, []any{[]byte("7"), []byte(bigText), bigBlob}); err != nil {
		t.Fatal(err)
	}
	if err := iw.row(kinds, []any{[]byte("8"), nil, nil}); err != nil {
		t.Fatal(err)
	}
	if err := iw.flush(); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	inserts, sets := 0, 0
	var text, blob strings.Builder
	for line := range strings.SplitSeq(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if len(line) > dumpStatementMax+(200<<10) {
			t.Fatalf("a statement of %d bytes", len(line))
		}
		switch {
		case strings.HasPrefix(line, "INSERT INTO `t` VALUES "):
			inserts++
			if !strings.HasSuffix(line, ");") {
				t.Fatalf("insert does not end with );: %.60s…", line[len(line)-60:])
			}
		case strings.HasPrefix(line, "SET @cactustrade_v0 = "), strings.HasPrefix(line, "SET @cactustrade_v1 = "):
			sets++
			lit := line[strings.LastIndex(line, " ")+1 : len(line)-1]
			lit = strings.TrimSuffix(lit, ")")
			switch {
			case strings.HasPrefix(line, "SET @cactustrade_v0") && lit != "NULL":
				if !strings.HasPrefix(lit, "'") || !strings.HasSuffix(lit, "'") {
					t.Fatalf("text chunk %.40s", lit)
				}
				text.WriteString(lit[1 : len(lit)-1])
			case strings.HasPrefix(line, "SET @cactustrade_v1") && lit != "NULL":
				b, err := hex.DecodeString(strings.TrimSuffix(strings.TrimPrefix(lit, "X'"), "'"))
				if err != nil {
					t.Fatal(err)
				}
				blob.Write(b)
			}
		default:
			t.Fatalf("unexpected line %.80s", line)
		}
	}
	if inserts != 5 { // 3 batches, the big row, the last row
		t.Fatalf("%d INSERTs, want 5", inserts)
	}
	if text.String() != bigText || blob.String() != string(bigBlob) {
		t.Fatalf("the chunked values do not add up: text %d/%d, blob %d/%d", text.Len(), len(bigText), blob.Len(), len(bigBlob))
	}
	if !strings.Contains(out.String(), "INSERT INTO `t` VALUES (7,@cactustrade_v0,@cactustrade_v1);\nSET @cactustrade_v0 = NULL;\nSET @cactustrade_v1 = NULL;\n") {
		t.Fatal("the big row does not use its variables or does not clear them")
	}
	if !strings.HasSuffix(out.String(), "INSERT INTO `t` VALUES (8,NULL,NULL);\n") || sets < 6 {
		t.Fatalf("tail %q sets %d", out.String()[out.Len()-60:], sets)
	}
}

func TestDumpMariaDBRefusesSQLite(t *testing.T) {
	s, err := OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.DumpMariaDB(context.Background(), &bytes.Buffer{}); !errors.Is(err, ErrNotMariaDB) {
		t.Fatalf("DumpMariaDB on SQLite = %v", err)
	}
}

// TestDumpKnowsEveryTable: the dump's literal statements cover exactly the tables of the
// migrated schema (the dialects are in lock-step, so SQLite's list is MariaDB's): a
// migration that adds a table must add its statement, or MariaDB backups would refuse.
func TestDumpKnowsEveryTable(t *testing.T) {
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
	var known []string
	for _, q := range dumpSelects {
		name := strings.Trim(strings.TrimPrefix(q, dumpSelectPrefix), "`")
		if q != dumpSelectPrefix+"`"+name+"`" {
			t.Fatalf("statement %q is not SELECT * FROM `table`", q)
		}
		if sel, ok := dumpSelect(name); !ok || sel != q {
			t.Fatalf("dumpSelect(%s) = %q %v", name, sel, ok)
		}
		known = append(known, name)
	}
	slices.Sort(known)
	if !slices.Equal(schema, known) {
		t.Fatalf("the schema's tables %v\nthe dump's tables    %v", schema, known)
	}
	if _, ok := dumpSelect("unknown"); ok {
		t.Fatal("an unknown table has a statement")
	}
}
