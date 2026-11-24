package httpapi

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// CSV exports (PLAN 6.7, OD17): comma-separated with a header row, times in UTC RFC 3339,
// US dollars as exact decimal strings from cents, and every text cell a spreadsheet could
// take for a formula prefixed with "'".

// formulaStarts are the first characters that make a spreadsheet read a text cell as a
// formula (or, for tab and carriage return, hide one behind whitespace).
const formulaStarts = "=+-@\t\r"

// neutralise prefixes a text cell that starts like a formula with "'", so a spreadsheet
// shows it as text. Every text cell goes through it, whatever its source (eToro symbols,
// order reasons, LLM verdict reasons).
func neutralise(s string) string {
	if s != "" && strings.ContainsRune(formulaStarts, rune(s[0])) {
		return "'" + s
	}
	return s
}

// csvCell formats one export cell (app.Export lists the types). Numbers are written by this
// function from integers, so they hold only digits, one '-' and one '.': a number, never a
// formula, and they are not prefixed (a spreadsheet keeps them numeric). Anything else is
// text and is neutralised.
func csvCell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return neutralise(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		return strconv.FormatBool(x)
	case domain.Cents:
		return fixed(int64(x), 2, false)
	case domain.Micros:
		return fixed(int64(x), 6, true)
	case domain.BasisPoints:
		return strconv.FormatInt(int64(x), 10)
	case time.Time:
		if x.IsZero() {
			return ""
		}
		return x.UTC().Format(time.RFC3339Nano)
	default:
		return neutralise(fmt.Sprint(x))
	}
}

// fixed writes v / 10^places (places 1–18) as an exact decimal, for every int64
// (math.MinInt64 too): cents keep both places ("-12.30"), micros drop trailing zeros when
// trim is set ("189.1").
func fixed(v int64, places int, trim bool) string {
	p := int64(1)
	for range places {
		p *= 10
	}
	// Quotient and remainder are taken before the sign is dropped, so neither overflows.
	q, r := v/p, v%p
	neg := v < 0
	if neg {
		q, r = -q, -r
	}
	frac := strconv.FormatInt(r, 10)
	frac = strings.Repeat("0", places-len(frac)) + frac
	if trim {
		frac = strings.TrimRight(frac, "0")
	}
	out := strconv.FormatInt(q, 10)
	if frac != "" {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

// writeCSV writes an export row by row as it formats them (csv.Writer buffers a few KiB).
func writeCSV(w io.Writer, e app.Export) error {
	cw := csv.NewWriter(w)
	header := make([]string, len(e.Columns))
	for i, c := range e.Columns {
		header[i] = neutralise(c)
	}
	if err := cw.Write(header); err != nil {
		return err
	}
	rec := make([]string, len(e.Columns))
	for _, row := range e.Rows {
		if len(row) != len(e.Columns) {
			return fmt.Errorf("csv: a %s row has %d cells for %d columns", e.Kind, len(row), len(e.Columns))
		}
		for i, c := range row {
			rec[i] = csvCell(c)
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// exportFileName is the download's file name: cactustrade-<kind>-<account id>.csv, with
// anything but letters, digits, '-' and '_' left out of the id.
func exportFileName(kind app.ExportKind, accountID string) string {
	id := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || ('0' <= r && r <= '9') || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') {
			return r
		}
		return -1
	}, accountID)
	return "cactustrade-" + string(kind) + "-" + id + ".csv"
}
