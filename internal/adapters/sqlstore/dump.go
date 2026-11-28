package sqlstore

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrNotMariaDB is returned by DumpMariaDB on a SQLite store (SQLite is backed up with
// BackupTo, VACUUM INTO).
var ErrNotMariaDB = errors.New("sqlstore: the logical dump is for a MariaDB database")

// dumpStatementMax is the largest INSERT statement the dump writes with several rows; a
// larger row is written alone. dumpChunk is the largest piece of one value: a value whose
// literal is larger is assembled in a user variable first (SET @v = …; SET @v = CONCAT(@v,
// …)), so no statement of the dump comes near max_allowed_packet (16 MiB by default) even
// for a 10 MiB attachment, whose hex literal alone would be 20 MiB.
const (
	dumpStatementMax = 1 << 20
	dumpChunk        = 256 << 10
)

// dumpSelects reads every table of the schema the dump knows, as literal statements: the
// dump has no dynamic SQL (dumpSelect finds a table's statement). A table the database has and this list does not is refused (fail
// closed: the backup would miss it); TestDumpKnowsEveryTable pins the list to the migrations,
// so a migration that adds a table must add it here.
var dumpSelects = []string{
	"SELECT * FROM `account_shares`",
	"SELECT * FROM `accounts`",
	"SELECT * FROM `agent_leases`",
	"SELECT * FROM `agent_positions`",
	"SELECT * FROM `api_keys`",
	"SELECT * FROM `audit_log`",
	"SELECT * FROM `decisions`",
	"SELECT * FROM `equity_snapshots`",
	"SELECT * FROM `events`",
	"SELECT * FROM `goose_db_version`",
	"SELECT * FROM `instrument_blocks`",
	"SELECT * FROM `instruments`",
	"SELECT * FROM `llm_calls`",
	"SELECT * FROM `orders`",
	"SELECT * FROM `outbox`",
	"SELECT * FROM `outbox_attachments`",
	"SELECT * FROM `password_resets`",
	"SELECT * FROM `real_gates`",
	"SELECT * FROM `reviews`",
	"SELECT * FROM `risk_state`",
	"SELECT * FROM `secrets`",
	"SELECT * FROM `sessions`",
	"SELECT * FROM `settings`",
	"SELECT * FROM `statement_schedules`",
	"SELECT * FROM `theme_instruments`",
	"SELECT * FROM `themes`",
	"SELECT * FROM `trades`",
	"SELECT * FROM `users`",
	"SELECT * FROM `watchlist`",
}

// dumpSelect returns the statement that reads table, if the dump knows it.
func dumpSelect(table string) (string, bool) {
	for _, q := range dumpSelects {
		if q == dumpSelectPrefix+"`"+table+"`" {
			return q, true
		}
	}
	return "", false
}

// dumpSelectPrefix starts every statement of dumpSelects.
const dumpSelectPrefix = "SELECT * FROM "

// dumpHeader and dumpFooter frame the dump. No CREATE DATABASE / USE: the dump loads into
// any database (deploy.sh's restore loads it into a scratch database first). Backslash
// escapes are on (sql_mode without NO_BACKSLASH_ESCAPES) and an explicit 0 in an
// AUTO_INCREMENT column stays 0, as in mariadb-dump.
const (
	dumpHeader = "-- cactusTrade MariaDB dump (written by the app, R48)\n" +
		"SET NAMES utf8mb4;\nSET time_zone='+00:00';\nSET FOREIGN_KEY_CHECKS=0;\nSET UNIQUE_CHECKS=0;\n" +
		"SET sql_mode='NO_AUTO_VALUE_ON_ZERO';\n"
	dumpFooter = "SET FOREIGN_KEY_CHECKS=1;\nSET UNIQUE_CHECKS=1;\n-- end of dump\n"
)

// DumpMariaDB writes a consistent logical dump of the MariaDB database to w (R48.3): one
// connection, one REPEATABLE READ transaction started WITH CONSISTENT SNAPSHOT (so writers
// carry on and every table is read at the same instant), and per table DROP TABLE IF EXISTS,
// SHOW CREATE TABLE and extended INSERTs. It loads with deploy.sh's mariadb-restore.sh like
// a mariadb-dump file; goose_db_version is written as deploy.sh's dump_schema reads it.
// Views, triggers, routines and events are refused (fail closed: the dump would miss them).
func (s *Store) DumpMariaDB(ctx context.Context, w io.Writer) error {
	if s.driver != DriverMariaDB {
		return ErrNotMariaDB
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("sqlstore.DumpMariaDB: %w", err)
	}
	defer conn.Close()
	for _, q := range []string{
		"SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ",
		"SET SESSION time_zone = '+00:00'",
		"START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY",
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("sqlstore.DumpMariaDB: %s: %w", q, err)
		}
	}
	// The connection goes back to the pool: end the transaction whatever happened.
	defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK") }()

	if err := refuseSchemaObjects(ctx, conn); err != nil {
		return err
	}
	tables, err := dumpTables(ctx, conn)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(w, 64<<10)
	if _, err := bw.WriteString(dumpHeader); err != nil {
		return fmt.Errorf("sqlstore.DumpMariaDB: %w", err)
	}
	for _, t := range tables {
		if err := dumpTable(ctx, conn, bw, t); err != nil {
			return err
		}
	}
	if _, err := bw.WriteString(dumpFooter); err != nil {
		return fmt.Errorf("sqlstore.DumpMariaDB: %w", err)
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("sqlstore.DumpMariaDB: %w", err)
	}
	return nil
}

// refuseSchemaObjects fails when the database has objects the dump does not write.
func refuseSchemaObjects(ctx context.Context, conn *sql.Conn) error {
	for what, q := range map[string]string{
		"view":    "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type <> 'BASE TABLE'",
		"trigger": "SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema = DATABASE()",
		"routine": "SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema = DATABASE()",
		"event":   "SELECT COUNT(*) FROM information_schema.events WHERE event_schema = DATABASE()",
	} {
		var n int
		if err := conn.QueryRowContext(ctx, q).Scan(&n); err != nil {
			return fmt.Errorf("sqlstore.DumpMariaDB: counting %ss: %w", what, err)
		}
		if n > 0 {
			return fmt.Errorf("sqlstore.DumpMariaDB: the database has %d %s(s), which the dump does not write: use mariadb-dump (deploy.sh --backup-now)", n, what)
		}
	}
	return nil
}

// dumpTables lists the database's tables by name.
func dumpTables(ctx context.Context, conn *sql.Conn) ([]string, error) {
	rows, err := conn.QueryContext(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_name")
	if err != nil {
		return nil, fmt.Errorf("sqlstore.DumpMariaDB: tables: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("sqlstore.DumpMariaDB: tables: %w", err)
		}
		if _, ok := dumpSelect(n); !ok {
			return nil, fmt.Errorf("sqlstore.DumpMariaDB: table %q is unknown to this version's dump (fail closed): use mariadb-dump (deploy.sh --backup-now)", n)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore.DumpMariaDB: tables: %w", err)
	}
	return out, nil
}

// dumpTable writes one table: its definition, then its rows.
func dumpTable(ctx context.Context, conn *sql.Conn, w *bufio.Writer, table string) error {
	sel, ok := dumpSelect(table)
	if !ok {
		return fmt.Errorf("sqlstore.DumpMariaDB: table %q is unknown to this version's dump", table)
	}
	qt := "`" + table + "`"
	var name, create string
	if err := conn.QueryRowContext(ctx, "SHOW CREATE TABLE "+qt).Scan(&name, &create); err != nil {
		return fmt.Errorf("sqlstore.DumpMariaDB: %s: %w", table, err)
	}
	if _, err := fmt.Fprintf(w, "\nDROP TABLE IF EXISTS %s;\n%s;\n", qt, create); err != nil {
		return fmt.Errorf("sqlstore.DumpMariaDB: %w", err)
	}
	// No arguments: the text protocol, so every value arrives as the server's text.
	rows, err := conn.QueryContext(ctx, sel)
	if err != nil {
		return fmt.Errorf("sqlstore.DumpMariaDB: %s: %w", table, err)
	}
	defer rows.Close()
	cts, err := rows.ColumnTypes()
	if err != nil {
		return fmt.Errorf("sqlstore.DumpMariaDB: %s: %w", table, err)
	}
	kinds := make([]dumpKind, len(cts))
	for i, ct := range cts {
		kinds[i] = kindOf(ct.DatabaseTypeName())
	}
	vals := make([]any, len(cts))
	ptrs := make([]any, len(cts))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	ins := insertWriter{w: w, table: qt}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("sqlstore.DumpMariaDB: %s: %w", table, err)
		}
		if err := ins.row(kinds, vals); err != nil {
			return fmt.Errorf("sqlstore.DumpMariaDB: %s: %w", table, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlstore.DumpMariaDB: %s: %w", table, err)
	}
	if err := ins.flush(); err != nil {
		return fmt.Errorf("sqlstore.DumpMariaDB: %w", err)
	}
	return nil
}

// dumpKind is how a column's values are written.
type dumpKind int

const (
	kindText   dumpKind = iota // quoted, escaped
	kindNumber                 // bare
	kindBinary                 // X'hex'
)

// kindOf classifies a column by the driver's type name.
func kindOf(typeName string) dumpKind {
	t := strings.ToUpper(typeName)
	if strings.Contains(t, "BLOB") || strings.Contains(t, "BINARY") || t == "BIT" || t == "GEOMETRY" {
		return kindBinary
	}
	switch strings.TrimPrefix(t, "UNSIGNED ") {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "BIGINT", "DECIMAL", "FLOAT", "DOUBLE", "YEAR":
		return kindNumber
	default:
		return kindText
	}
}

// insertWriter batches rows into extended INSERTs of at most dumpStatementMax bytes.
type insertWriter struct {
	w     *bufio.Writer
	table string          // quoted
	buf   strings.Builder // the pending statement's rows: "(…),(…)"
}

// row adds one row.
func (iw *insertWriter) row(kinds []dumpKind, vals []any) error {
	lits := make([]string, len(vals))
	size := 2
	big := false
	for i, v := range vals {
		lit, err := encodeValue(kinds[i], v)
		if err != nil {
			return fmt.Errorf("column %d: %w", i+1, err)
		}
		lits[i] = lit
		size += len(lit) + 1
		if len(lit) > dumpChunk {
			big = true
		}
	}
	if big { // alone, its large values assembled in user variables first
		if err := iw.flush(); err != nil {
			return err
		}
		var vars []string
		for i, v := range vals {
			if len(lits[i]) <= dumpChunk {
				continue
			}
			name := fmt.Sprintf("@cactustrade_v%d", len(vars))
			if err := iw.setVar(name, kinds[i], v); err != nil {
				return err
			}
			lits[i] = name
			vars = append(vars, name)
		}
		if _, err := fmt.Fprintf(iw.w, "INSERT INTO %s VALUES (%s);\n", iw.table, strings.Join(lits, ",")); err != nil {
			return err
		}
		for _, v := range vars {
			if _, err := fmt.Fprintf(iw.w, "SET %s = NULL;\n", v); err != nil {
				return err
			}
		}
		return nil
	}
	if iw.buf.Len() > 0 && iw.buf.Len()+size > dumpStatementMax {
		if err := iw.flush(); err != nil {
			return err
		}
	}
	if iw.buf.Len() > 0 {
		iw.buf.WriteByte(',')
	}
	iw.buf.WriteByte('(')
	iw.buf.WriteString(strings.Join(lits, ","))
	iw.buf.WriteByte(')')
	return nil
}

// setVar writes SET statements that build v in the user variable name, a chunk at a time.
func (iw *insertWriter) setVar(name string, kind dumpKind, v any) error {
	var chunks []string
	switch kind {
	case kindBinary:
		b := valueBytes(v)
		for len(b) > 0 {
			n := min(len(b), dumpChunk/2)
			chunks = append(chunks, "X'"+hex.EncodeToString(b[:n])+"'")
			b = b[n:]
		}
	default: // text; numbers are never this long
		b := valueBytes(v)
		for len(b) > 0 {
			n := min(len(b), dumpChunk/2)
			for n < len(b) && n > 0 && !utf8.RuneStart(b[n]) { // never split a character
				n--
			}
			if n == 0 {
				n = min(len(b), dumpChunk/2)
			}
			chunks = append(chunks, quoteText(b[:n]))
			b = b[n:]
		}
	}
	for i, c := range chunks {
		var err error
		if i == 0 {
			_, err = fmt.Fprintf(iw.w, "SET %s = %s;\n", name, c)
		} else {
			_, err = fmt.Fprintf(iw.w, "SET %s = CONCAT(%s, %s);\n", name, name, c)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// flush writes the pending extended INSERT.
func (iw *insertWriter) flush() error {
	if iw.buf.Len() == 0 {
		return nil
	}
	_, err := fmt.Fprintf(iw.w, "INSERT INTO %s VALUES %s;\n", iw.table, iw.buf.String())
	iw.buf.Reset()
	return err
}

// valueBytes is a scanned value's bytes.
func valueBytes(v any) []byte {
	switch x := v.(type) {
	case []byte:
		return x
	case string:
		return []byte(x)
	default:
		return []byte(fmt.Sprint(x))
	}
}

// encodeValue is one value as an SQL literal: NULL, a bare number, X'hex' for binary, else
// a quoted string with \0 \' \\ \n \r \Z escaped.
func encodeValue(kind dumpKind, v any) (string, error) {
	if v == nil {
		return "NULL", nil
	}
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32), nil
	case bool:
		if x {
			return "1", nil
		}
		return "0", nil
	case time.Time:
		return "'" + x.UTC().Format("2006-01-02 15:04:05.999999") + "'", nil
	case []byte, string:
	default:
		return "", fmt.Errorf("unexpected value type %T", v)
	}
	b := valueBytes(v)
	switch kind {
	case kindNumber:
		if !isNumberLiteral(b) {
			return "", fmt.Errorf("not a number: %q", b)
		}
		return string(b), nil
	case kindBinary:
		return "X'" + hex.EncodeToString(b) + "'", nil
	default:
		return quoteText(b), nil
	}
}

// isNumberLiteral reports a plain decimal number as the server writes it.
func isNumberLiteral(b []byte) bool {
	if len(b) == 0 || len(b) > 100 {
		return false
	}
	digits := false
	for i, c := range b {
		switch {
		case c >= '0' && c <= '9':
			digits = true
		case c == '-' || c == '+':
			if i != 0 && b[i-1] != 'e' && b[i-1] != 'E' {
				return false
			}
		case c == '.' || c == 'e' || c == 'E':
		default:
			return false
		}
	}
	return digits
}

// quoteText quotes a string literal for MariaDB with backslash escapes.
func quoteText(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) + 2)
	sb.WriteByte('\'')
	for _, c := range b {
		switch c {
		case 0:
			sb.WriteString(`\0`)
		case '\'':
			sb.WriteString(`\'`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case 0x1a:
			sb.WriteString(`\Z`)
		default:
			sb.WriteByte(c)
		}
	}
	sb.WriteByte('\'')
	return sb.String()
}
