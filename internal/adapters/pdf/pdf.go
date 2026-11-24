// Package pdf renders trading statements (R30) as PDF documents with codeberg.org/go-pdf/fpdf
// (MIT, pure Go). It implements ports.StatementRenderer: it only lays out the figures the
// statement carries (no I/O, no network, no clock), so the same statement always gives the
// same bytes. Text uses the PDF standard fonts (Helvetica) in the Windows-1252 encoding, which
// covers £, €, ¥, A$ and Western European names; any other character prints as "?". The
// cactusTrade logo is drawn as vector shapes from the web app's SVG (no image file, no fetch).
// It also renders the analysis page (R31) as a one-page PDF (ports.AnalysisRenderer,
// analysis.go).
package pdf

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"codeberg.org/go-pdf/fpdf"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Renderer renders statements as A4 PDF documents and analyses as one-page PDF documents. It is safe for concurrent use: every
// Render builds its own document.
type Renderer struct{}

var _ ports.StatementRenderer = Renderer{}

// New returns a Renderer.
func New() Renderer { return Renderer{} }

// Page geometry (millimetres, A4 portrait) and colours.
const (
	pageW, pageH  = 210.0, 297.0
	margin        = 15.0
	contentW      = pageW - 2*margin
	footerSpace   = 18.0
	headerBottom  = 31.0
	rowH          = 6.0
	fontBody      = 9.0
	fontSmall     = 7.5
	fontHeading   = 11.5
	disclaimerTxt = "software, not financial advice — trading can lose money"
)

type rgb struct{ r, g, b int }

var (
	colInk     = rgb{33, 37, 41}
	colMuted   = rgb{108, 117, 125}
	colGreen   = rgb{47, 94, 72}    // headings, rules
	colLight   = rgb{143, 203, 176} // logo, light trace
	colDark    = rgb{90, 145, 116}  // logo, dark trace
	colBand    = rgb{232, 243, 237} // summary box, table header
	colZebra   = rgb{247, 250, 248}
	colNeg     = rgb{176, 42, 55}
	colRule    = rgb{206, 218, 211}
	colWarning = rgb{133, 100, 4}
)

// Render lays out st as a PDF.
func (Renderer) Render(ctx context.Context, st domain.Statement) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("pdf.Render: %w", err)
	}
	d := newDoc(st)
	d.body()
	var buf bytes.Buffer
	if err := d.f.Output(&buf); err != nil {
		return nil, fmt.Errorf("pdf.Render: %w", err)
	}
	return buf.Bytes(), nil
}

// doc is one statement being laid out.
type doc struct {
	f  *fpdf.Fpdf
	st domain.Statement
}

func newDoc(st domain.Statement) *doc {
	f := fpdf.New("P", "mm", "A4", "")
	f.SetMargins(margin, headerBottom+4, margin)
	f.SetAutoPageBreak(true, footerSpace)
	f.SetCompression(true)
	// Fixed metadata and object order: the same statement gives the same bytes.
	f.SetCreationDate(st.GeneratedAt.UTC())
	f.SetModificationDate(st.GeneratedAt.UTC())
	f.SetCatalogSort(true)
	f.SetProducer("cactusTrade", false)
	f.SetCreator("cactusTrade", false)
	f.SetTitle(st.Window.Period.Label()+" trading statement - "+st.Window.Title(), true)
	f.SetSubject(st.Title, true)
	f.AliasNbPages("{nb}")
	d := &doc{f: f, st: st}
	f.SetHeaderFunc(d.header)
	f.SetFooterFunc(d.footer)
	return d
}

// ---- text -----------------------------------------------------------------------------

// cp1252 maps the Unicode characters of Windows-1252's 0x80–0x9F range to their bytes.
var cp1252 = map[rune]byte{
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84, '…': 0x85, '†': 0x86, '‡': 0x87, 'ˆ': 0x88, '‰': 0x89, 'Š': 0x8A,
	'‹': 0x8B, 'Œ': 0x8C, 'Ž': 0x8E, '‘': 0x91, '’': 0x92, '“': 0x93, '”': 0x94, '•': 0x95, '–': 0x96, '—': 0x97,
	'˜': 0x98, '™': 0x99, 'š': 0x9A, '›': 0x9B, 'œ': 0x9C, 'ž': 0x9E, 'Ÿ': 0x9F,
}

// enc turns UTF-8 text into Windows-1252 for the standard fonts: control characters become
// spaces, the minus sign a hyphen, anything else outside the code page "?".
func enc(s string) string {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			b = append(b, ' ')
		case r < 0x80, r >= 0xA0 && r <= 0xFF:
			b = append(b, byte(r&0xFF)) // Latin-1 is the same in Windows-1252
		case r == '−':
			b = append(b, '-')
		default:
			if c, ok := cp1252[r]; ok {
				b = append(b, c)
			} else {
				b = append(b, '?')
			}
		}
	}
	return string(b)
}

func (d *doc) color(c rgb) { d.f.SetTextColor(c.r, c.g, c.b) }
func (d *doc) fill(c rgb)  { d.f.SetFillColor(c.r, c.g, c.b) }
func (d *doc) draw(c rgb)  { d.f.SetDrawColor(c.r, c.g, c.b) }
func (d *doc) font(style string, size float64) {
	d.f.SetFont("Helvetica", style, size)
}

// fit cuts s (already encoded) with "…" so it fits w millimetres at the current font.
func (d *doc) fit(s string, w float64) string {
	if d.f.GetStringWidth(s) <= w {
		return s
	}
	ell := string([]byte{0x85})
	for len(s) > 0 && d.f.GetStringWidth(s+ell) > w {
		s = s[:len(s)-1]
	}
	return s + ell
}

// wrap breaks encoded text into lines of at most w millimetres at the current font, at
// spaces (a word wider than w is cut by fit). fpdf's SplitText reads its input as UTF-8, which
// encoded text is not.
func (d *doc) wrap(s string, w float64) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		next := word
		if line != "" {
			next = line + " " + word
		}
		if line != "" && d.f.GetStringWidth(next) > w {
			lines = append(lines, line)
			next = word
		}
		line = next
	}
	if line != "" {
		lines = append(lines, d.fit(line, w))
	}
	return lines
}

// cell writes one encoded, fitted cell.
func (d *doc) cell(w, h float64, s, border string, ln int, align string, fill bool) {
	d.f.CellFormat(w, h, d.fit(enc(s), w-2), border, ln, align, fill, 0, "")
}

// para writes wrapped text over the content width.
func (d *doc) para(s string, h float64) {
	d.f.MultiCell(contentW, h, enc(s), "", "L", false)
}

// ensure starts a new page when less than h millimetres are left above the footer.
func (d *doc) ensure(h float64) {
	if d.f.GetY()+h > pageH-footerSpace {
		d.f.AddPage()
	}
}

// ---- page furniture ---------------------------------------------------------------------

// logo draws the cactusTrade mark (web/public/logo.svg: a cactus drawn as two circuit traces,
// each ending in a ring) in a size × size square at (x, y).
func (d *doc) logo(x, y, size float64) {
	k := size / 460
	px := func(v float64) float64 { return x + (v+35)*k }
	py := func(v float64) float64 { return y + (v-77)*k }
	f := d.f
	f.SetLineWidth(18 * k)
	f.SetLineCapStyle("round")
	f.SetLineJoinStyle("round")
	// The light trace: up, over the top, down, a step right, a curve up to the ring.
	d.draw(colLight)
	f.MoveTo(px(160), py(295))
	f.LineTo(px(160), py(145))
	f.ArcTo(px(197), py(145), 37*k, 37*k, 0, 180, 0)
	f.LineTo(px(234), py(385))
	f.LineTo(px(264), py(372))
	f.CurveTo(px(290), py(360), px(290), py(332))
	f.LineTo(px(290), py(260))
	f.DrawPath("D")
	f.Circle(px(290), py(243), 15*k, "D")
	// The dark trace: down, a curve, a step, down, round the bottom and up.
	d.draw(colDark)
	f.MoveTo(px(97), py(209))
	f.LineTo(px(97), py(280))
	f.CurveTo(px(97), py(302), px(119), py(311))
	f.LineTo(px(139), py(320))
	f.CurveTo(px(162), py(330), px(162), py(354))
	f.LineTo(px(162), py(475))
	f.ArcTo(px(199), py(475), 37*k, 37*k, 0, 180, 360)
	f.LineTo(px(236), py(430))
	f.DrawPath("D")
	f.Circle(px(97), py(192), 15*k, "D")
	f.SetLineCapStyle("butt")
	f.SetLineJoinStyle("miter")
}

func (d *doc) header() {
	f := d.f
	d.logo(margin, 10, 15)
	f.SetXY(margin+17, 11)
	d.font("B", 16)
	d.color(colGreen)
	f.CellFormat(f.GetStringWidth("cactus"), 8, "cactus", "", 0, "L", false, 0, "")
	d.color(colDark)
	f.CellFormat(30, 8, "Trade", "", 0, "L", false, 0, "")
	f.SetXY(margin+17, 18)
	d.font("", fontSmall)
	d.color(colMuted)
	f.CellFormat(60, 4, "Trading statement", "", 0, "L", false, 0, "")
	// Right: the period and the scope.
	f.SetXY(pageW-margin-110, 11)
	d.font("B", 11)
	d.color(colInk)
	d.cell(110, 6, d.st.Window.Period.Label()+" statement · "+d.st.Window.Title(), "", 2, "R", false)
	d.font("", fontBody)
	d.color(colMuted)
	d.cell(110, 5, d.st.Title, "", 2, "R", false)
	d.draw(colGreen)
	f.SetLineWidth(0.6)
	f.Line(margin, headerBottom-3, pageW-margin, headerBottom-3)
	f.SetXY(margin, headerBottom+4)
	d.color(colInk)
}

func (d *doc) footer() {
	f := d.f
	d.draw(colRule)
	f.SetLineWidth(0.2)
	f.Line(margin, pageH-13, pageW-margin, pageH-13)
	f.SetXY(margin, pageH-12)
	d.font("", fontSmall)
	d.color(colMuted)
	d.cell(contentW-30, 4, "Generated "+d.st.GeneratedAt.UTC().Format("2 January 2006 15:04 UTC")+" · cactusTrade is "+disclaimerTxt, "", 0, "L", false)
	f.CellFormat(30, 4, "Page "+strconv.Itoa(f.PageNo())+" of {nb}", "", 0, "R", false, 0, "")
}

// ---- the statement ------------------------------------------------------------------------

func (d *doc) body() {
	d.f.AddPage()
	d.overview()
	d.summary()
	if d.st.AccountWide {
		d.equity()
	}
	d.trades()
	d.breakdown("By exchange", "Exchange", d.st.ByExchange)
	if d.st.Scope != domain.ScopeAccount {
		d.breakdown("By account", "Account", d.st.ByAccount)
	}
	d.positions()
	if d.st.AccountWide {
		d.holdings()
		d.activity()
	}
	d.notes()
}

// overview is the block under the header: period, scope and accounts.
func (d *doc) overview() {
	w := d.st.Window
	rows := [][2]string{
		{"Period", w.Title() + " (risk days " + string(w.FirstDay) + " to " + string(w.LastDay) + ": " +
			w.From.UTC().Format("2 Jan 2006 15:04") + " to " + w.To.UTC().Format("2 Jan 2006 15:04") + " UTC)"},
		{"Scope", scopeText(d.st)},
		{"Accounts", accountsText(d.st.Accounts)},
	}
	for _, r := range rows {
		d.font("B", fontBody)
		d.color(colMuted)
		d.f.CellFormat(24, 5, enc(r[0]), "", 0, "L", false, 0, "")
		d.font("", fontBody)
		d.color(colInk)
		d.f.MultiCell(contentW-24, 5, enc(r[1]), "", "L", false)
	}
	d.f.Ln(3)
}

func scopeText(st domain.Statement) string {
	switch st.Scope {
	case domain.ScopeAccount:
		return "One account"
	case domain.ScopeExchange:
		return "Trades on " + st.Exchange + " across the accounts below"
	}
	return "Every account below, every exchange"
}

func accountsText(as []domain.StatementAccountLine) string {
	if len(as) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(as))
	for _, a := range as {
		p := a.Name + " (" + a.Platform + ", " + string(a.Mode)
		if a.ViewOnly {
			p += ", view-only"
		}
		parts = append(parts, p+")")
	}
	return strings.Join(parts, "; ")
}

// summary is the plain-English paragraph in a tinted box.
func (d *doc) summary() {
	f := d.f
	d.font("", fontBody+0.5)
	lines := d.wrap(enc(d.st.Summary), contentW-8)
	h := float64(len(lines))*5 + 10
	d.ensure(h + 2)
	y := f.GetY()
	d.fill(colBand)
	d.draw(colLight)
	f.SetLineWidth(0.3)
	f.RoundedRect(margin, y, contentW, h, 2, "1234", "FD")
	f.SetXY(margin+4, y+2)
	d.font("B", fontBody)
	d.color(colGreen)
	f.CellFormat(contentW-8, 5, "Summary", "", 2, "L", false, 0, "")
	d.font("", fontBody+0.5)
	d.color(colInk)
	for _, l := range lines {
		f.SetX(margin + 4)
		f.CellFormat(contentW-8, 5, l, "", 2, "L", false, 0, "")
	}
	f.SetY(y + h + 4)
}

func (d *doc) heading(s string, need float64) {
	d.ensure(need + 9)
	d.font("B", fontHeading)
	d.color(colGreen)
	d.f.CellFormat(contentW, 7, enc(s), "", 1, "L", false, 0, "")
	d.color(colInk)
}

// col is a table column.
type col struct {
	title string
	w     float64 // share of the content width, 0–1
	right bool
}

// row is one table row; bold rows are totals, neg marks cells to print in red.
type row struct {
	cells []string
	bold  bool
	neg   map[int]bool
}

// table draws a header row and the rows, repeating the header after a page break.
func (d *doc) table(cols []col, rows []row) {
	f := d.f
	head := func() {
		d.font("B", fontSmall+0.5)
		d.fill(colBand)
		d.color(colGreen)
		for _, c := range cols {
			align := "L"
			if c.right {
				align = "R"
			}
			d.cell(c.w*contentW, rowH, c.title, "", 0, align, true)
		}
		f.Ln(-1)
	}
	head()
	for i, r := range rows {
		if f.GetY()+rowH > pageH-footerSpace {
			f.AddPage()
			head()
		}
		style := ""
		if r.bold {
			style = "B"
		}
		d.font(style, fontBody)
		d.fill(colZebra)
		for j, c := range cols {
			align := "L"
			if c.right {
				align = "R"
			}
			d.color(colInk)
			if r.neg[j] {
				d.color(colNeg)
			}
			text := ""
			if j < len(r.cells) {
				text = r.cells[j]
			}
			d.cell(c.w*contentW, rowH, text, "", 0, align, i%2 == 1)
		}
		f.Ln(-1)
	}
	d.draw(colRule)
	f.SetLineWidth(0.2)
	f.Line(margin, f.GetY(), pageW-margin, f.GetY())
	d.color(colInk)
	f.Ln(4)
}

// none writes a muted line in place of an empty table.
func (d *doc) none(s string) {
	d.font("I", fontBody)
	d.color(colMuted)
	d.para(s, 5)
	d.color(colInk)
	d.f.Ln(3)
}

func money(cur string, c domain.Cents) string { return domain.FormatMoney(cur, c) }
func usd(c domain.Cents) string               { return domain.FormatMoney("USD", c) }

func negs(vals ...domain.Cents) map[int]bool {
	m := map[int]bool{}
	for i, v := range vals {
		if v < 0 {
			m[i] = true
		}
	}
	return m
}

func (d *doc) equity() {
	d.heading("Equity", 3*rowH)
	if len(d.st.Equity) == 0 {
		d.none("No accounts.")
		return
	}
	cols := []col{
		{"Account", 0.27, false},
		{"Currency", 0.09, false},
		{"First recorded", 0.16, true},
		{"Last recorded", 0.16, true},
		{"Change", 0.15, true},
		{"Max drawdown", 0.17, true},
	}
	var rows []row
	for _, e := range d.st.Equity {
		name := e.Account
		if e.ViewOnly {
			name += " (view-only)"
		}
		if !e.Known {
			rows = append(rows, row{cells: []string{name, e.Currency, "not recorded", "not recorded", "", ""}})
			continue
		}
		dd := money(e.Currency, e.Drawdown.MaxDrawdown) + " (" + domain.FormatPercent(e.Drawdown.MaxDrawdownBp) + ")"
		rows = append(rows, row{
			cells: []string{name, e.Currency, money(e.Currency, e.Opening), money(e.Currency, e.Closing), money(e.Currency, e.Change), dd},
			neg:   map[int]bool{4: e.Change < 0},
		})
	}
	for _, t := range d.st.EquityTotals {
		rows = append(rows, row{
			cells: []string{
				"Total " + t.Currency + " (" + plural(t.Accounts, "account") + ")", t.Currency, money(t.Currency, t.Opening),
				money(t.Currency, t.Closing), money(t.Currency, t.Change), "",
			},
			bold: true, neg: map[int]bool{4: t.Change < 0},
		})
	}
	d.table(cols, rows)
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + one + "s"
}

func winRate(s domain.TradeSummary) string {
	if !s.HasWinRate {
		return "-"
	}
	return domain.FormatPercent(s.WinRate)
}

func feesText(s domain.TradeSummary) string {
	t := usd(s.Fees)
	if s.FeesUnknown > 0 {
		t += " + unknown (" + plural(s.FeesUnknown, "trade") + ")"
	}
	return t
}

func (d *doc) trades() {
	t := d.st.Trades
	d.heading("Closed trades (positions cactusTrade opened, US dollars)", 6*rowH)
	line := func(l *domain.StatementTradeLine) string {
		if l == nil {
			return "-"
		}
		sym := l.Symbol
		if sym == "" {
			sym = "share not cached"
		}
		return usd(l.Net) + " (" + sym + ", " + l.Account + ")"
	}
	rows := []row{
		{cells: []string{"Trades closed", strconv.Itoa(t.Count)}},
		{cells: []string{"Wins / losses / breakeven", fmt.Sprintf("%d / %d / %d", t.Wins, t.Losses, t.Breakeven)}},
		{cells: []string{"Win rate", winRate(t)}},
		{cells: []string{"Realised P&L (after fees)", usd(t.Net)}, neg: map[int]bool{1: t.Net < 0}, bold: true},
		{cells: []string{"Fees reported by eToro", feesText(t)}},
		{cells: []string{"Before reported fees", usd(t.Gross)}, neg: map[int]bool{1: t.Gross < 0}},
		{cells: []string{"Largest win", line(d.st.LargestWin)}},
		{cells: []string{"Largest loss", line(d.st.LargestLoss)}, neg: map[int]bool{1: d.st.LargestLoss != nil}},
		{cells: []string{"Trades of positions cactusTrade did not open (not counted)", strconv.Itoa(d.st.ForeignTrades)}},
	}
	d.table([]col{{"Figure", 0.55, false}, {"Value", 0.45, true}}, rows)
}

func (d *doc) breakdown(title, what string, bs []domain.StatementBreakdown) {
	if len(bs) == 0 {
		return
	}
	d.heading(title, 3*rowH)
	cols := []col{
		{what, 0.21, false},
		{"Trades", 0.08, true},
		{"Wins", 0.07, true},
		{"Losses", 0.08, true},
		{"Win rate", 0.11, true},
		{"Realised P&L", 0.15, true},
		{"Fees reported", 0.16, true},
		{"Fees unknown", 0.14, true},
	}
	rows := make([]row, 0, len(bs))
	for _, b := range bs {
		s := b.Stats
		unknown := "-"
		if s.FeesUnknown > 0 {
			unknown = plural(s.FeesUnknown, "trade")
		}
		rows = append(rows, row{
			cells: []string{b.Name, strconv.Itoa(s.Count), strconv.Itoa(s.Wins), strconv.Itoa(s.Losses), winRate(s), usd(s.Net), usd(s.Fees), unknown},
			neg:   negs(0, 0, 0, 0, 0, s.Net),
		})
	}
	d.table(cols, rows)
}

func (d *doc) positions() {
	d.heading("Positions cactusTrade opened, open at the end of the period", 2*rowH)
	if len(d.st.OpenPositions) == 0 {
		d.none("None: every position cactusTrade opened was closed by the end of the period.")
		return
	}
	cols := []col{{"Account", 0.26, false}, {"Share", 0.18, false}, {"Exchange", 0.14, false}, {"Direction", 0.14, false}, {"Opened (UTC)", 0.28, true}}
	rows := make([]row, 0, len(d.st.OpenPositions))
	for _, p := range d.st.OpenPositions {
		sym := p.Symbol
		if sym == "" {
			sym = "#" + strconv.FormatInt(p.PositionID, 10)
		}
		ex := string(p.Market)
		if ex == "" {
			ex = domain.OtherExchange
		}
		rows = append(rows, row{cells: []string{p.Account, sym, ex, string(p.Direction), p.OpenedAt.UTC().Format("2 Jan 2006 15:04")}})
	}
	d.table(cols, rows)
}

func (d *doc) holdings() {
	if len(d.st.Holdings) == 0 {
		return
	}
	for _, h := range d.st.Holdings {
		title := "Holdings: " + h.Account + " (" + h.Platform + ", view-only)"
		if h.Holdings == nil {
			d.heading(title, rowH)
			reason := h.Error
			if reason == "" {
				reason = "not read"
			}
			d.none("Could not be read: " + reason + ".")
			continue
		}
		hs := h.Holdings
		d.heading(title, 3*rowH)
		d.font("", fontSmall)
		d.color(colMuted)
		d.para("As read "+hs.AsOf.UTC().Format("2 Jan 2006 15:04 UTC")+" (when the statement was made, not at the end of the period). "+
			"Cash "+money(hs.Currency, hs.Cash)+", equity "+money(hs.Currency, hs.Equity)+".", 4)
		d.color(colInk)
		d.f.Ln(1)
		if len(hs.Positions) == 0 {
			d.none("No open positions.")
			continue
		}
		cols := []col{{"Share", 0.30, false}, {"Quantity", 0.16, true}, {"Value", 0.18, true}, {"Cost", 0.18, true}, {"Unrealised P&L", 0.18, true}}
		rows := make([]row, 0, len(hs.Positions))
		for _, p := range hs.Positions {
			name := p.Symbol
			if p.Name != "" {
				name += " " + p.Name
			}
			if p.Unknown {
				rows = append(rows, row{cells: []string{name, p.Quantity.String(), "unknown", "unknown", "unknown"}})
				continue
			}
			rows = append(rows, row{
				cells: []string{name, p.Quantity.String(), money(hs.Currency, p.Value), money(hs.Currency, p.Cost), money(hs.Currency, p.PnL)},
				neg:   negs(0, 0, 0, 0, p.PnL),
			})
		}
		d.table(cols, rows)
	}
}

func (d *doc) activity() {
	st := d.st
	d.heading("Risk events and agent activity", 4*rowH)
	rows := []row{
		{cells: []string{"Kill switch used", strconv.Itoa(st.KillSwitches)}},
		{cells: []string{"Daily loss limit stopped new entries", strconv.Itoa(st.DailyStops)}},
		{cells: []string{"Decisions logged by the agent", strconv.Itoa(st.DecisionsTotal)}, bold: true},
	}
	for _, c := range st.Decisions {
		rows = append(rows, row{cells: []string{"    " + c.Label, strconv.Itoa(c.N)}})
	}
	rows = append(rows, row{cells: []string{"Orders stored by cactusTrade (by state now)", strconv.Itoa(st.OrdersTotal)}, bold: true})
	for _, c := range st.Orders {
		rows = append(rows, row{cells: []string{"    " + c.Label, strconv.Itoa(c.N)}})
	}
	u := st.LLM
	cost := domain.FormatMicrosUSD(u.Cost)
	if !u.CostKnown() {
		cost += " + unknown (" + plural(int(min(u.CostUnknownCalls, 1<<31)), "call") + " without a price)"
	}
	rows = append(rows,
		row{cells: []string{"LLM reviewer calls (failed)", fmt.Sprintf("%d (%d)", u.Calls, u.Failed)}, bold: true},
		row{cells: []string{"    Tokens in / out", fmt.Sprintf("%d / %d", u.InputTokens, u.OutputTokens)}},
		row{cells: []string{"    Cost", cost}},
	)
	d.table([]col{{"What", 0.7, false}, {"Count", 0.3, true}}, rows)
}

func (d *doc) notes() {
	d.ensure(20)
	d.font("B", fontSmall+0.5)
	d.color(colWarning)
	d.f.CellFormat(contentW, 5, "Notes", "", 1, "L", false, 0, "")
	d.font("", fontSmall)
	d.color(colMuted)
	for _, n := range d.st.Notes {
		d.para("• "+n, 3.8)
	}
	d.color(colInk)
}
