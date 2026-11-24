package pdf

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"codeberg.org/go-pdf/fpdf"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// The analysis page as a PDF (R31): always exactly one page. The page is 210 mm wide (A4's
// width) and 297 mm tall when the content fits; otherwise it is as tall as the content,
// rounded up to the next 10 mm (R31-d). The height is found by laying the analysis out once
// on a tall measuring page and then again on a page of the chosen size, so both passes draw
// the same thing. Tables are capped at analysisMaxRows rows (the rest is counted in a last
// row), which bounds the height. Charts are vector graphics in a colour-blind-safe palette
// (Okabe–Ito: blue for gains, vermillion for losses); the group tables carry their P&L as a
// bar beside the figure (by exchange, account, theme, playbook …).

var _ ports.AnalysisRenderer = Renderer{}

// Analysis page geometry (millimetres).
const (
	analysisMinH     = 297.0  // A4 portrait
	analysisMeasureH = 5000.0 // the measuring page (PDF allows up to 5080 mm)
	analysisFooterH  = 14.0   // the footer's space at the bottom of the page
	analysisRowH     = 3.7
	analysisMaxRows  = 40 // rows per grouping
	analysisGap      = 4.0
	analysisHalfW    = (contentW - analysisGap) / 2
)

// Okabe–Ito colours: distinguishable with every common colour-vision deficiency.
var (
	cbPos    = rgb{0, 114, 178}   // blue: gains
	cbNeg    = rgb{213, 94, 0}    // vermillion: losses
	cbZero   = rgb{153, 153, 153} // grey: breakeven
	cbSeries = []rgb{{0, 114, 178}, {230, 159, 0}, {0, 158, 115}, {204, 121, 167}, {86, 180, 233}, {213, 94, 0}}
)

// AnalysisPageHeight is the page height for content ending at bottom millimetres: A4's 297
// mm when it fits, else the content and the footer rounded up to the next 10 mm (R31-d).
func AnalysisPageHeight(bottom float64) float64 {
	need := bottom + analysisFooterH
	if need <= analysisMinH {
		return analysisMinH
	}
	return math.Ceil(need/10) * 10
}

// RenderAnalysis lays out an as a one-page PDF.
func (Renderer) RenderAnalysis(ctx context.Context, an domain.Analysis) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("pdf.RenderAnalysis: %w", err)
	}
	measure := newAnalysisDoc(an, analysisMeasureH)
	h := AnalysisPageHeight(measure.layout())
	if h > analysisMeasureH {
		return nil, fmt.Errorf("pdf.RenderAnalysis: the analysis needs a %.0f mm page", h)
	}
	a := newAnalysisDoc(an, h)
	a.layout()
	a.footer()
	var buf bytes.Buffer
	if err := a.f.Output(&buf); err != nil {
		return nil, fmt.Errorf("pdf.RenderAnalysis: %w", err)
	}
	return buf.Bytes(), nil
}

// adoc is one analysis being laid out on a page h millimetres tall; y is where the next
// block starts.
type adoc struct {
	*doc
	an domain.Analysis
	h  float64
	y  float64
}

func newAnalysisDoc(an domain.Analysis, h float64) *adoc {
	f := fpdf.NewCustom(&fpdf.InitType{OrientationStr: "P", UnitStr: "mm", Size: fpdf.SizeType{Wd: pageW, Ht: h}})
	f.SetMargins(margin, margin, margin)
	f.SetAutoPageBreak(false, 0) // one page, always
	f.SetCompression(true)
	f.SetCreationDate(an.GeneratedAt.UTC())
	f.SetModificationDate(an.GeneratedAt.UTC())
	f.SetCatalogSort(true)
	f.SetProducer("cactusTrade", false)
	f.SetCreator("cactusTrade", false)
	f.SetTitle("Trading analysis - "+an.Title, true)
	f.SetSubject(an.Scope, true)
	f.AddPage()
	return &adoc{doc: &doc{f: f}, an: an, h: h}
}

// layout draws everything above the footer and returns where it ends.
func (a *adoc) layout() float64 {
	a.header()
	a.kpis()
	a.narrative()
	a.charts()
	a.tables()
	a.notes()
	return a.y
}

// text writes encoded, fitted text at (x, y) in a box w wide and h high.
func (a *adoc) text(x, y, w, h float64, s, align string) {
	a.f.SetXY(x, y)
	a.f.CellFormat(w, h, a.fit(enc(s), w), "", 0, align, false, 0, "")
}

func (a *adoc) header() {
	f := a.f
	a.logo(margin, 10, 15)
	f.SetXY(margin+17, 11)
	a.font("B", 16)
	a.color(colGreen)
	f.CellFormat(f.GetStringWidth("cactus"), 8, "cactus", "", 0, "L", false, 0, "")
	a.color(colDark)
	f.CellFormat(30, 8, "Trade", "", 0, "L", false, 0, "")
	a.font("", fontSmall)
	a.color(colMuted)
	a.text(margin+17, 18, 60, 4, "Trading analysis", "L")
	w := a.an.Window
	a.font("B", 11)
	a.color(colInk)
	a.text(pageW-margin-120, 10, 120, 6, "Analysis · "+a.an.Title, "R")
	a.font("", fontBody)
	a.color(colMuted)
	a.text(pageW-margin-120, 16, 120, 4.5, w.Preset.Label()+" · "+plural(w.Days, "risk day")+" from "+w.From.UTC().Format("2 Jan 2006 15:04")+
		" UTC · grouped by "+string(w.Bucket), "R")
	a.text(pageW-margin-120, 20.5, 120, 4.5, a.an.Scope, "R")
	a.draw(colGreen)
	f.SetLineWidth(0.6)
	f.Line(margin, 28, pageW-margin, 28)
	a.font("", fontSmall)
	a.color(colMuted)
	a.y = 30
	for _, l := range a.wrap(enc("Accounts: "+accountsText(a.an.Accounts)), contentW) {
		a.f.SetXY(margin, a.y)
		a.f.CellFormat(contentW, 3.6, l, "", 0, "L", false, 0, "")
		a.y += 3.6
	}
	a.y += 2
	a.color(colInk)
}

// tile is one KPI box.
func (a *adoc) tile(x, y, w, h float64, label, value, sub string, valueCol rgb) {
	a.fill(colBand)
	a.draw(colLight)
	a.f.SetLineWidth(0.3)
	a.f.RoundedRect(x, y, w, h, 1.5, "1234", "FD")
	a.font("", 6.3)
	a.color(colMuted)
	a.text(x+1.8, y+0.8, w-3.6, 3.4, label, "L")
	a.font("B", 10.5)
	a.color(valueCol)
	a.text(x+1.8, y+4.1, w-3.6, 5, value, "L")
	a.font("", 5.8)
	a.color(colMuted)
	a.text(x+1.8, y+9.2, w-3.6, 3, sub, "L")
	a.color(colInk)
}

func signCol(c domain.Cents) rgb {
	switch {
	case c > 0:
		return cbPos
	case c < 0:
		return cbNeg
	}
	return colInk
}

func tradeTile(r *domain.AnalysisTradeRef) (string, string, rgb) {
	if r == nil {
		return "-", "no trades", colInk
	}
	sym := r.Symbol
	if sym == "" {
		sym = "share not cached"
	}
	return usd(r.Net), sym + " on " + r.Account + ", " + r.ClosedAt.UTC().Format("2 Jan"), signCol(r.Net)
}

func (a *adoc) kpis() {
	an := a.an
	t := an.Totals.Stats
	const cols, h, gap = 5, 12.5, 2.2
	w := (contentW - (cols-1)*gap) / cols
	none := t.Count == 0
	dash := func(s string) string {
		if none {
			return "-"
		}
		return s
	}
	type kpi struct {
		label, value, sub string
		col               rgb
	}
	foreign := "all opened by cactusTrade"
	if an.ForeignTrades > 0 {
		foreign = "+" + strconv.Itoa(an.ForeignTrades) + " not opened by cactusTrade"
	}
	share, shareSub := "-", "no closed trades"
	if an.HasOursShare {
		share, shareSub = domain.FormatPercent(an.OursShareBp), "of "+plural(t.Count+an.ForeignTrades, "closed trade")
	}
	dd, ddSub := "not recorded", "no equity snapshots"
	if len(an.Curves) > 0 {
		var worst domain.BasisPoints
		parts := make([]string, 0, len(an.Curves))
		for _, c := range an.Curves {
			worst = max(worst, c.Drawdown.MaxDrawdownBp)
			parts = append(parts, money(c.Currency, c.Drawdown.MaxDrawdown))
		}
		dd, ddSub = domain.FormatPercent(worst), strings.Join(parts, " · ")
	}
	llmSub := plural(int(min(an.LLM.Calls, 1<<31)), "reviewer call")
	if !an.LLM.CostKnown() {
		llmSub += ", " + strconv.FormatInt(an.LLM.CostUnknownCalls, 10) + " without a price"
	}
	bv, bs, bc := tradeTile(an.Totals.Best)
	wv, ws, wc := tradeTile(an.Totals.Worst)
	tiles := []kpi{
		{"Closed trades", strconv.Itoa(t.Count), foreign, colInk},
		{"Win rate", winRate(t), fmt.Sprintf("%d wins / %d losses / %d even", t.Wins, t.Losses, t.Breakeven), colInk},
		{"Realised P&L (after fees)", dash(usd(t.Net)), "before reported fees " + usd(t.Gross), signCol(t.Net)},
		{"Average per trade", dash(usd(an.Totals.AvgNet)), plural(an.TradeAccounts, "account") + " with trades", signCol(an.Totals.AvgNet)},
		{"Best trade", bv, bs, bc},
		{"Fees reported by eToro", usd(t.Fees), unknownFeesText(t), colInk},
		{"Largest drawdown", dd, ddSub, colInk},
		{"Opened by cactusTrade", share, shareSub, colInk},
		{"LLM cost", domain.FormatMicrosUSD(an.LLM.Cost), llmSub, colInk},
		{"Worst trade", wv, ws, wc},
	}
	for i, k := range tiles {
		x := margin + float64(i%cols)*(w+gap)
		y := a.y + float64(i/cols)*(h+gap)
		a.tile(x, y, w, h, k.label, k.value, k.sub, k.col)
	}
	a.y += 2*(h+gap) + 1.5
}

func unknownFeesText(t domain.TradeSummary) string {
	if t.FeesUnknown == 0 {
		return "every trade's fees reported"
	}
	return "+ unknown for " + plural(t.FeesUnknown, "trade")
}

// heading writes a section heading at the current y.
func (a *adoc) heading(s string) {
	a.font("B", 9)
	a.color(colGreen)
	a.text(margin, a.y, contentW, 5, s, "L")
	a.y += 5.2
	a.color(colInk)
}

// wrapFirst breaks encoded text into lines: the first at most first millimetres wide, the
// others at most w.
func (a *adoc) wrapFirst(s string, first, w float64) []string {
	var lines []string
	line, limit := "", first
	for _, word := range strings.Fields(s) {
		next := word
		if line != "" {
			next = line + " " + word
		}
		if line != "" && a.f.GetStringWidth(next) > limit {
			lines = append(lines, line)
			next, limit = word, w
		}
		line = next
	}
	if line != "" {
		lines = append(lines, a.fit(line, limit))
	}
	return lines
}

// narrative writes the narrative with run-in headings: each section's heading in bold at the
// start of its first paragraph.
func (a *adoc) narrative() {
	const size, lh = 7.6, 3.25
	a.heading("What happened")
	for _, s := range a.an.Narrative {
		for i, p := range s.Paragraphs {
			indent := 0.0
			if i == 0 {
				a.font("B", size)
				a.color(colGreen)
				indent = a.f.GetStringWidth(enc(s.Heading)) + 2
				a.text(margin, a.y, indent+1, lh, s.Heading, "L")
			}
			a.font("", size)
			a.color(colInk)
			for j, l := range a.wrapFirst(enc(p), contentW-indent, contentW) {
				x := margin
				if j == 0 {
					x += indent
				}
				a.f.SetXY(x, a.y)
				a.f.CellFormat(contentW, lh, l, "", 0, "L", false, 0, "")
				a.y += lh
			}
		}
		a.y += 0.9
	}
	a.y += 1.5
}

// ---- charts -------------------------------------------------------------------------------

// Charts sit in a grid of three columns: the P&L by date bucket, the equity of each currency,
// then wins/losses/breakeven.
func (a *adoc) charts() {
	const h = 33.0
	w := (contentW - 2*analysisGap) / 3
	cells := []func(x, y float64){func(x, y float64) { a.dateBars(x, y, w, h) }}
	for _, c := range a.an.Curves {
		cells = append(cells, func(x, y float64) { a.equityChart(x, y, w, h, c) })
	}
	if len(a.an.Curves) == 0 {
		cells = append(cells, func(x, y float64) {
			a.chartTitle(x, y, w, "Equity")
			a.font("I", fontSmall)
			a.color(colMuted)
			a.text(x, y+5, w, 4, "No equity snapshots in the period.", "L")
		})
	}
	cells = append(cells, func(x, y float64) { a.outcomeDonut(x, y, w) })
	for i, draw := range cells {
		draw(margin+float64(i%3)*(w+analysisGap), a.y+float64(i/3)*(h+analysisGap))
	}
	a.y += float64((len(cells)+2)/3)*(h+analysisGap) + 1
	a.color(colInk)
}

// chartTitle writes a chart's caption.
func (a *adoc) chartTitle(x, y, w float64, s string) {
	a.font("B", 7)
	a.color(colInk)
	a.text(x, y, w, 4, s, "L")
}

// dateBars draws the realised P&L of each date bucket as a bar above or below zero.
func (a *adoc) dateBars(x, y, w, h float64) {
	f := a.f
	gs := a.an.Grouping(domain.DimDate)
	a.chartTitle(x, y, w, "Realised P&L by "+string(a.an.Window.Bucket)+" (US dollars)")
	top, bottom := y+6, y+h-5
	plotL, plotR := x+16, x+w
	mid := (top + bottom) / 2
	var most domain.Cents = 1
	for _, g := range gs {
		most = max(most, absC(g.Stats.Net))
	}
	a.font("", 5.5)
	a.color(colMuted)
	a.text(x, top-2, 15.5, 4, usd(most), "R")
	a.text(x, mid-2, 15.5, 4, usd(0), "R")
	a.text(x, bottom-2, 15.5, 4, usd(-most), "R")
	a.draw(colRule)
	f.SetLineWidth(0.2)
	f.Line(plotL, top, plotR, top)
	f.Line(plotL, bottom, plotR, bottom)
	a.draw(colMuted)
	f.Line(plotL, mid, plotR, mid)
	if len(gs) == 0 {
		return
	}
	step := (plotR - plotL) / float64(len(gs))
	for i, g := range gs {
		n := g.Stats.Net
		bh := float64(absC(n)) / float64(most) * (mid - top - 0.5)
		bx := plotL + float64(i)*step + step*0.15
		switch {
		case n > 0:
			a.fill(cbPos)
			f.Rect(bx, mid-bh, step*0.7, bh, "F")
		case n < 0:
			a.fill(cbNeg)
			f.Rect(bx, mid, step*0.7, bh, "F")
		}
	}
	a.font("", 5.5)
	a.color(colMuted)
	a.text(plotL, bottom+0.5, (plotR-plotL)/2, 3.5, gs[0].Label, "L")
	a.text(plotL+(plotR-plotL)/2, bottom+0.5, (plotR-plotL)/2, 3.5, gs[len(gs)-1].Label, "R")
}

func absC(c domain.Cents) domain.Cents {
	if c < 0 {
		if c == math.MinInt64 {
			return math.MaxInt64
		}
		return -c
	}
	return c
}

// outcomeDonut draws wins, losses and breakeven as a ring with a legend.
func (a *adoc) outcomeDonut(x, y, w float64) {
	f := a.f
	t := a.an.Totals.Stats
	a.chartTitle(x, y, w, "Wins, losses and breakeven (trades)")
	if t.Count == 0 {
		a.font("I", fontSmall)
		a.color(colMuted)
		a.text(x, y+5, w, 4, "No trades.", "L")
		return
	}
	const r = 12.0
	cx, cy := x+r+1, y+6+r
	parts := []struct {
		n     int
		label string
		col   rgb
	}{{t.Wins, "Wins", cbPos}, {t.Losses, "Losses", cbNeg}, {t.Breakeven, "Breakeven", cbZero}}
	start := 90.0
	for _, p := range parts {
		if p.n == 0 {
			continue
		}
		sweep := 360 * float64(p.n) / float64(t.Count)
		pts := []fpdf.PointType{{X: cx, Y: cy}}
		steps := max(2, int(sweep/4))
		for i := 0; i <= steps; i++ {
			ang := (start - sweep*float64(i)/float64(steps)) * math.Pi / 180
			pts = append(pts, fpdf.PointType{X: cx + r*math.Cos(ang), Y: cy - r*math.Sin(ang)})
		}
		a.fill(p.col)
		f.Polygon(pts, "F")
		start -= sweep
	}
	f.SetFillColor(255, 255, 255)
	f.Circle(cx, cy, r*0.55, "F")
	a.font("B", 7)
	a.color(colInk)
	a.text(cx-r*0.55, cy-2.5, r*1.1, 5, winRate(t), "C")
	lx := cx + r + 3
	for i, p := range parts {
		ly := y + 8 + float64(i)*6
		a.fill(p.col)
		f.Rect(lx, ly+1, 2.6, 2.6, "F")
		a.font("", 6.5)
		a.color(colInk)
		a.text(lx+3.6, ly, w-(lx-x)-3.6, 4.6, fmt.Sprintf("%s: %d of %d", p.label, p.n, t.Count), "L")
	}
}

// equityChart draws one currency's recorded equity over the period with its drawdown in the
// caption.
func (a *adoc) equityChart(x, y, w, h float64, c domain.AnalysisCurve) {
	f := a.f
	a.chartTitle(x, y, w, fmt.Sprintf("Equity in %s (%s)", c.Currency, plural(c.Accounts, "account")))
	a.font("", 5.8)
	a.color(colMuted)
	a.text(x, y+3.4, w, 3, "largest drawdown "+money(c.Currency, c.Drawdown.MaxDrawdown)+" ("+domain.FormatPercent(c.Drawdown.MaxDrawdownBp)+")", "L")
	top, bottom := y+8, y+h-5
	plotL, plotR := x+17, x+w
	lo, hi := c.Points[0].Equity, c.Points[0].Equity
	for _, p := range c.Points {
		lo, hi = min(lo, p.Equity), max(hi, p.Equity)
	}
	pad := max((hi-lo)/10, 100)
	lo, hi = lo-pad, hi+pad
	from, to := a.an.Window.From, a.an.Window.To
	if a.an.GeneratedAt.After(from) && a.an.GeneratedAt.Before(to) {
		to = maxTime(a.an.GeneratedAt, c.Points[len(c.Points)-1].At)
	}
	span := max(to.Sub(from).Seconds(), 1)
	px := func(t time.Time) float64 { return plotL + t.Sub(from).Seconds()/span*(plotR-plotL) }
	py := func(v domain.Cents) float64 { return bottom - float64(v-lo)/float64(hi-lo)*(bottom-top) }
	a.font("", 5.5)
	a.text(x, top-2, 16.5, 4, money(c.Currency, hi), "R")
	a.text(x, bottom-2, 16.5, 4, money(c.Currency, lo), "R")
	a.text(plotL, bottom+0.5, (plotR-plotL)/2, 3.5, from.UTC().Add(2*time.Hour).Format("2 Jan"), "L") // the first risk day
	a.text(plotL+(plotR-plotL)/2, bottom+0.5, (plotR-plotL)/2, 3.5, to.UTC().Format("2 Jan 15:04"), "R")
	a.draw(colRule)
	f.SetLineWidth(0.2)
	f.Line(plotL, top, plotR, top)
	f.Line(plotL, bottom, plotR, bottom)
	col := cbSeries[0]
	f.SetDrawColor(col.r, col.g, col.b)
	f.SetLineWidth(0.5)
	if len(c.Points) == 1 {
		a.fill(col)
		f.Circle(px(c.Points[0].At), py(c.Points[0].Equity), 0.8, "F")
	}
	for i := 1; i < len(c.Points); i++ {
		p0, p1 := c.Points[i-1], c.Points[i]
		f.Line(px(p0.At), py(p0.Equity), px(p1.At), py(p1.Equity))
	}
	a.color(colInk)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// ---- tables -------------------------------------------------------------------------------

// tcol is a column of a compact table.
type tcol struct {
	title string
	w     float64 // share of the table's width
	right bool
}

// trow is one row of a compact table: sep rows name a grouping across the whole width; bar
// rows draw their value as a bar (scaled to most) in the last column.
type trow struct {
	cells []string
	bold  bool
	sep   bool
	note  bool // a muted line across the whole width
	col   map[int]rgb
	bar   bool
	val   domain.Cents
	most  domain.Cents
}

// ctable draws a compact table at x, w wide, from the current y (no page breaks: the page is
// sized to fit).
func (a *adoc) ctable(x, w float64, cols []tcol, rows []trow) {
	f := a.f
	rh := analysisRowH
	a.font("B", 6.3)
	a.fill(colBand)
	a.color(colGreen)
	cx := x
	for _, c := range cols {
		f.SetXY(cx, a.y)
		f.CellFormat(c.w*w, rh, a.fit(enc(c.title), c.w*w-1.5), "", 0, align(c.right), true, 0, "")
		cx += c.w * w
	}
	a.y += rh
	zebra := false
	for _, r := range rows {
		if r.note {
			a.font("I", 6.2)
			a.color(colMuted)
			a.text(x, a.y, w, rh, r.cells[0], "L")
			a.y += rh
			continue
		}
		if r.sep {
			a.font("B", 6.5)
			a.color(colGreen)
			a.text(x, a.y+0.4, w, rh, r.cells[0], "L")
			a.draw(colRule)
			f.SetLineWidth(0.15)
			f.Line(x, a.y+rh+0.2, x+w, a.y+rh+0.2)
			a.y += rh + 0.6
			zebra = false
			continue
		}
		style := ""
		if r.bold {
			style = "B"
		}
		a.font(style, 6.5)
		a.fill(colZebra)
		cx = x
		for j, c := range cols {
			cw := c.w * w
			if zebra {
				f.Rect(cx, a.y, cw, rh, "F")
			}
			if r.bar && j == len(cols)-1 {
				a.cellBar(cx, a.y, cw, rh, r.val, r.most)
				cx += cw
				continue
			}
			a.color(colInk)
			if col, ok := r.col[j]; ok {
				a.color(col)
			}
			s := ""
			if j < len(r.cells) {
				s = r.cells[j]
			}
			f.SetXY(cx, a.y)
			f.CellFormat(cw, rh, a.fit(enc(s), cw-1.5), "", 0, align(c.right), false, 0, "")
			cx += cw
		}
		zebra = !zebra
		a.y += rh
	}
	a.draw(colRule)
	f.SetLineWidth(0.2)
	f.Line(x, a.y, x+w, a.y)
	a.color(colInk)
	a.y += 2.5
}

// cellBar draws v as a bar from the middle of the cell (right for gains, left for losses).
func (a *adoc) cellBar(x, y, w, h float64, v, most domain.Cents) {
	if most <= 0 || v == 0 {
		return
	}
	mid := x + w/2
	bw := float64(absC(v)) / float64(most) * (w/2 - 1)
	if v > 0 {
		a.fill(cbPos)
		a.f.Rect(mid, y+0.8, bw, h-1.6, "F")
	} else {
		a.fill(cbNeg)
		a.f.Rect(mid-bw, y+0.8, bw, h-1.6, "F")
	}
	a.draw(colMuted)
	a.f.SetLineWidth(0.15)
	a.f.Line(mid, y+0.4, mid, y+h-0.4)
	a.fill(colZebra)
}

func align(right bool) string {
	if right {
		return "R"
	}
	return "L"
}

// groupBlock is one grouping's rows for the two-column group tables.
func groupBlock(an domain.Analysis, gg domain.AnalysisGrouping) []trow {
	title := gg.Title
	var shown []domain.AnalysisGroup
	for _, g := range gg.Groups {
		if gg.Dimension == domain.DimDate && g.Stats.Count == 0 {
			continue // the chart shows the empty buckets
		}
		shown = append(shown, g)
	}
	if gg.Dimension == domain.DimDate {
		title += " (" + string(an.Window.Bucket) + "s with trades)"
	}
	rows := []trow{{cells: []string{title}, sep: true}}
	if len(shown) == 0 {
		return append(rows, trow{cells: []string{"No trades."}})
	}
	var most domain.Cents
	for _, g := range shown {
		most = max(most, absC(g.Stats.Net))
	}
	for _, g := range shown[:min(len(shown), analysisMaxRows)] {
		s := g.Stats
		rows = append(rows, trow{
			cells: []string{g.Label, strconv.Itoa(s.Count), fmt.Sprintf("%d / %d / %d", s.Wins, s.Losses, s.Breakeven), winRate(s), usd(s.Net)},
			col:   map[int]rgb{4: signCol(s.Net)}, bar: true, val: s.Net, most: most,
		})
	}
	if more := len(shown) - analysisMaxRows; more > 0 {
		rows = append(rows, trow{cells: []string{fmt.Sprintf("… and %d more (see the Analysis page)", more)}, note: true})
	}
	return rows
}

// tables draws the groupings in two columns (each grouping goes to the shorter column, in
// order), then recorded equity beside risk and activity.
func (a *adoc) tables() {
	an := a.an
	a.heading("Figures by group (cactusTrade's closed trades, US dollars, after eToro's fees)")
	var cols [2][]trow
	for _, gg := range an.Groupings {
		b := groupBlock(an, gg)
		i := 0
		if len(cols[1]) < len(cols[0]) {
			i = 1
		}
		cols[i] = append(cols[i], b...)
	}
	tc := []tcol{{"Group", 0.31, false}, {"Trades", 0.1, true}, {"W / L / B", 0.15, true}, {"Win rate", 0.13, true}, {"P&L", 0.15, true}, {"", 0.16, false}}
	x := [2]float64{margin, margin + analysisHalfW + analysisGap}
	var y [2]float64
	top := a.y
	for i := range 2 {
		a.y = top
		a.ctable(x[i], analysisHalfW, tc, cols[i])
		y[i] = a.y + 1
	}
	// Equity, then risk and activity, each in the column that ends higher.
	for _, block := range []func(x, w float64){a.equityTable, a.riskActivity} {
		i := 0
		if y[1] < y[0] {
			i = 1
		}
		a.y = y[i]
		block(x[i], analysisHalfW)
		y[i] = a.y + 1
	}
	a.y = max(y[0], y[1])
}

func (a *adoc) subheading(x, w float64, s string) {
	a.font("B", 7)
	a.color(colInk)
	a.text(x, a.y, w, 4, s, "L")
	a.y += 4.2
}

func (a *adoc) equityTable(x, w float64) {
	an := a.an
	a.subheading(x, w, "Recorded equity (per currency, never added together)")
	cols := []tcol{{"Account", 0.33, false}, {"First", 0.18, true}, {"Last", 0.18, true}, {"Change", 0.17, true}, {"Max DD", 0.14, true}}
	var rows []trow
	for _, e := range an.Equity[:min(len(an.Equity), analysisMaxRows)] {
		name := e.Account
		if e.ViewOnly {
			name += " (view-only)"
		}
		if !e.Known {
			rows = append(rows, trow{cells: []string{name, "not recorded", "", "", ""}})
			continue
		}
		rows = append(rows, trow{
			cells: []string{name, money(e.Currency, e.Opening), money(e.Currency, e.Closing), money(e.Currency, e.Change), domain.FormatPercent(e.Drawdown.MaxDrawdownBp)},
			col:   map[int]rgb{3: signCol(e.Change)},
		})
	}
	for _, t := range an.EquityTotals {
		rows = append(rows, trow{
			cells: []string{"Total " + t.Currency + " (" + plural(t.Accounts, "account") + ")", money(t.Currency, t.Opening), money(t.Currency, t.Closing), money(t.Currency, t.Change), ""},
			bold:  true, col: map[int]rgb{3: signCol(t.Change)},
		})
	}
	if len(rows) == 0 {
		rows = append(rows, trow{cells: []string{"No accounts."}})
	}
	a.ctable(x, w, cols, rows)
}

func (a *adoc) riskActivity(x, w float64) {
	an := a.an
	r := an.Risk
	a.subheading(x, w, "Risk & discipline, and activity")
	rows := []trow{
		{cells: []string{"Kill switch used / reset", fmt.Sprintf("%d / %d", r.KillSwitches, r.KillResets)}},
		{cells: []string{"Daily loss limit stopped new entries", strconv.Itoa(r.DailyStops)}},
		{cells: []string{"Risk limits changed · force-closed", fmt.Sprintf("%d · %d", r.LimitChanges, r.ForceCloses)}},
		{cells: []string{"Candidates refused by the risk engine", strconv.Itoa(r.RiskRefusals)}},
		{cells: []string{"Decisions logged", strconv.Itoa(an.DecisionsTotal)}, bold: true},
	}
	for _, c := range an.Decisions {
		rows = append(rows, trow{cells: []string{"   " + c.Label, strconv.Itoa(c.N)}})
	}
	for _, c := range an.Verdicts {
		rows = append(rows, trow{cells: []string{"   Reviewer: " + c.Label, strconv.Itoa(c.N)}})
	}
	u := an.LLM
	cost := domain.FormatMicrosUSD(u.Cost)
	if !u.CostKnown() {
		cost += " + " + strconv.FormatInt(u.CostUnknownCalls, 10) + " unknown"
	}
	rows = append(rows,
		trow{cells: []string{fmt.Sprintf("LLM calls %d (failed %d), tokens %d / %d", u.Calls, u.Failed, u.InputTokens, u.OutputTokens), cost}},
		trow{cells: []string{"Closed trades not opened by cactusTrade", strconv.Itoa(an.ForeignTrades)}},
	)
	a.ctable(x, w, []tcol{{"What", 0.74, false}, {"Count", 0.26, true}}, rows)
}

func (a *adoc) notes() {
	a.font("B", 6.5)
	a.color(colWarning)
	a.text(margin, a.y, contentW, 3.6, "How these figures are made", "L")
	a.y += 3.8
	a.font("", 6.1)
	a.color(colMuted)
	for _, n := range a.an.Notes {
		for _, l := range a.wrap(enc("• "+n), contentW) {
			a.f.SetXY(margin, a.y)
			a.f.CellFormat(contentW, 2.85, l, "", 0, "L", false, 0, "")
			a.y += 2.85
		}
	}
	a.color(colInk)
}

// footer closes the page: the generated time, the disclaimer and the page size.
func (a *adoc) footer() {
	f := a.f
	y := a.h - 12
	a.draw(colRule)
	f.SetLineWidth(0.2)
	f.Line(margin, y-1, pageW-margin, y-1)
	a.font("", fontSmall)
	a.color(colMuted)
	a.text(margin, y, contentW-40, 4, "Generated "+a.an.GeneratedAt.UTC().Format("2 January 2006 15:04 UTC")+" · cactusTrade is "+disclaimerTxt, "L")
	a.text(pageW-margin-40, y, 40, 4, fmt.Sprintf("One page · %.0f × %.0f mm", pageW, a.h), "R")
}
