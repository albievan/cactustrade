package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/adapters/etoro"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// probeReader is everything etoro-probe may ask eToro: read operations only. It has no method
// that places, closes or cancels anything, so the probe cannot reach one
// (TestProbeReaderIsReadOnly pins the method set).
type probeReader interface {
	Search(ctx context.Context, query string) ([]domain.Instrument, error)
	Instruments(ctx context.Context, ids []domain.InstrumentID) ([]domain.Instrument, error)
	Quotes(ctx context.Context, ids []domain.InstrumentID) ([]domain.Quote, error)
	Candles(ctx context.Context, id domain.InstrumentID, interval time.Duration, count int) ([]domain.Candle, error)
	Eligibility(ctx context.Context, ids []domain.InstrumentID) ([]domain.Eligibility, error)
	Costs(ctx context.Context, id domain.InstrumentID, d domain.Direction, amount domain.Cents) ([]domain.CostItem, error)
}

var _ probeReader = (*etoro.Client)(nil)

// probeAmount is the hypothetical order the probe asks the costs of ($100).
const probeAmount domain.Cents = 100_00

// etoroProbeCmd prints what eToro says about one share, read only, with one demo account's
// own keys (PLAN 2.7: settles Q1 — LSE prices in GBX pence or GBP —, real settlement
// availability and isPotential from real answers):
//
//	cactustrade etoro-probe --account ID --symbol SYM [--raw] [--record DIR]
//
// It asks search, instrument metadata, rates, the last 3 one-minute and one-day candles,
// eligibility, and the costs of a $100 long (and of a $100 short when eligible). --raw also
// prints each answer, --record writes each answer to DIR as a JSON file; both sanitised
// (account ids, gcid, cid, user names and token-looking fields replaced).
func etoroProbeCmd(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("etoro-probe", flag.ContinueOnError)
	fs.SetOutput(out)
	acct := fs.String("account", "", "demo eToro account id (required)")
	symbol := fs.String("symbol", "", "eToro symbol, e.g. BARC.L or AAPL (required)")
	showRaw := fs.Bool("raw", false, "also print each answer's JSON (sanitised)")
	record := fs.String("record", "", "write each answer, sanitised, as a JSON file into this folder")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *acct == "" || strings.TrimSpace(*symbol) == "" {
		return errors.New("etoro-probe: --account and --symbol are required")
	}
	raw := &rawAnswers{}
	r, err := openResearch(ctx, getenv, *acct, false, raw.record)
	if err != nil {
		return fmt.Errorf("etoro-probe: %w", err)
	}
	defer r.Close()
	return probe(ports.WithAccount(ctx, r.acct), r.client, raw, probeOptions{
		symbol: strings.TrimSpace(*symbol), showRaw: *showRaw, recordDir: *record,
	}, out)
}

// rawAnswer is one read answer as eToro sent it.
type rawAnswer struct {
	op   etoro.Op
	body []byte
}

// rawAnswers collects the answers of the probe's reads (etoro.Config.RecordAnswer).
type rawAnswers struct {
	mu   sync.Mutex
	list []rawAnswer
}

func (a *rawAnswers) record(op etoro.Op, body []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.list = append(a.list, rawAnswer{op: op, body: body})
}

// take returns the answers collected since the last take.
func (a *rawAnswers) take() []rawAnswer {
	a.mu.Lock()
	defer a.mu.Unlock()
	l := a.list
	a.list = nil
	return l
}

type probeOptions struct {
	symbol    string
	showRaw   bool
	recordDir string
}

// prober runs the probe's steps; a failed step is reported and the next one still runs.
type prober struct {
	r    probeReader
	raw  *rawAnswers
	o    probeOptions
	out  *strings.Builder
	errs []error
}

// probe asks eToro, read only, everything PLAN 2.7 needs about one share and prints it.
func probe(ctx context.Context, r probeReader, raw *rawAnswers, o probeOptions, out io.Writer) error {
	p := &prober{r: r, raw: raw, o: o, out: &strings.Builder{}}
	if o.recordDir != "" {
		if err := os.MkdirAll(o.recordDir, 0o700); err != nil {
			return fmt.Errorf("etoro-probe: %w", err)
		}
	}
	in, err := p.instrument(ctx)
	if err == nil {
		p.quote(ctx, in)
		p.candles(ctx, in.ID)
		el, ok := p.eligibility(ctx, in.ID)
		p.costs(ctx, in.ID, el, ok)
		if in.Market == domain.MarketLSE {
			p.printf("Q1 (LSE units): compare the bid above with the share's price on londonstockexchange.com: about 100× the GBP price means eToro quotes GBX (pence).\n")
		}
	} else {
		p.errs = append(p.errs, err)
	}
	if _, werr := io.WriteString(out, p.out.String()); werr != nil {
		return werr
	}
	if len(p.errs) > 0 {
		return fmt.Errorf("etoro-probe: %w", errors.Join(p.errs...))
	}
	return nil
}

func (p *prober) printf(format string, a ...any) { fmt.Fprintf(p.out, format, a...) }

// fail reports a failed step and keeps its error for the exit status.
func (p *prober) fail(step string, err error) {
	p.printf("  %s failed: %v\n", step, err)
	p.errs = append(p.errs, fmt.Errorf("%s: %w", step, err))
}

// answers takes the answers of the last step, prints them with --raw, writes them with
// --record (always sanitised) and returns them as received for parsing.
func (p *prober) answers(step string) []rawAnswer {
	got := p.raw.take()
	for i, a := range got {
		if !p.o.showRaw && p.o.recordDir == "" {
			break
		}
		clean, err := sanitizeJSON(a.body)
		if err != nil {
			p.fail("sanitising the "+step+" answer", err)
			continue
		}
		if p.o.showRaw {
			p.printf("  raw %s (%s, sanitised): %s\n", step, a.op, compactJSON(clean))
		}
		if p.o.recordDir != "" {
			name := recordName(p.o.symbol, step, i)
			if err := os.WriteFile(filepath.Join(p.o.recordDir, name), clean, 0o600); err != nil {
				p.fail("recording "+name, err)
				continue
			}
			p.printf("  recorded %s\n", filepath.Join(p.o.recordDir, name))
		}
	}
	return got
}

// recordName is the file for answer i of a step: "<symbol>_<step>.json" ("_2", "_3"… for
// further pages), the symbol in lower case with anything but letters and digits as "_".
func recordName(symbol, step string, i int) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '_'
	}, symbol)
	if i > 0 {
		return fmt.Sprintf("%s_%s_%d.json", slug, step, i+1)
	}
	return fmt.Sprintf("%s_%s.json", slug, step)
}

func compactJSON(b []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return string(b)
	}
	return buf.String()
}

// instrument finds the share by its exact symbol and reads its metadata.
func (p *prober) instrument(ctx context.Context) (domain.Instrument, error) {
	p.printf("Instrument %s\n", p.o.symbol)
	found, err := p.r.Search(ctx, p.o.symbol)
	searchAnswers := p.answers("search")
	if err != nil {
		return domain.Instrument{}, fmt.Errorf("search: %w", err)
	}
	i := slices.IndexFunc(found, func(in domain.Instrument) bool { return strings.EqualFold(in.Symbol, p.o.symbol) })
	if i < 0 {
		names := make([]string, 0, len(found))
		for _, in := range found[:min(len(found), 8)] {
			names = append(names, fmt.Sprintf("%s (%d)", in.Symbol, in.ID))
		}
		return domain.Instrument{}, fmt.Errorf("search: no instrument with the symbol %q (found: %s): %w", p.o.symbol, strings.Join(names, ", "), domain.ErrNotFound)
	}
	in := found[i]
	meta, err := p.r.Instruments(ctx, []domain.InstrumentID{in.ID})
	metaAnswers := p.answers("instrument")
	if err != nil {
		p.fail("instrument metadata", err)
	} else if j := slices.IndexFunc(meta, func(m domain.Instrument) bool { return m.ID == in.ID }); j >= 0 {
		in = meta[j]
	}
	market := string(in.Market)
	if market == "" {
		market = "not a supported market"
	}
	p.printf("  id %d, symbol %s, name %q, type %s, market %s, exchange id %d\n", in.ID, in.Symbol, in.Name, in.Type, market, in.ExchangeID)
	cur := currencyFields(slices.Concat(searchAnswers, metaAnswers), int64(in.ID))
	if len(cur) == 0 {
		p.printf("  currency: not in eToro's search or instrument answer (the spec's instrument has no currency field)\n")
	} else {
		p.printf("  currency fields: %s\n", strings.Join(cur, ", "))
	}
	return in, nil
}

// currencyFields lists "key=value" for every field whose name mentions "currency" in the
// answers' result object for the instrument id.
func currencyFields(answers []rawAnswer, id int64) []string {
	var out []string
	for _, a := range answers {
		var resp struct {
			Results []map[string]json.RawMessage `json:"results"`
		}
		if json.Unmarshal(a.body, &resp) != nil {
			continue
		}
		for _, obj := range resp.Results {
			var got int64
			if json.Unmarshal(obj["instrumentId"], &got) != nil || got != id {
				continue
			}
			for k, v := range obj {
				if s := k + "=" + string(v); strings.Contains(strings.ToLower(k), "currency") && !slices.Contains(out, s) {
					out = append(out, s)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// quote prints the latest bid/ask as cactusTrade reads it and the numbers eToro sent.
func (p *prober) quote(ctx context.Context, in domain.Instrument) {
	p.printf("Quote\n")
	qs, err := p.r.Quotes(ctx, []domain.InstrumentID{in.ID})
	got := p.answers("rates")
	if err != nil {
		p.fail("rates", err)
		return
	}
	if len(qs) == 0 {
		p.printf("  no usable quote (bid, ask or date missing): treated as stale\n")
	}
	for _, q := range qs {
		kind := "realtime"
		if q.Delayed {
			kind = "delayed"
		}
		p.printf("  bid %s, ask %s, spread %s, at %s (%s)\n", q.Bid, q.Ask, q.Ask-q.Bid, q.At.Format(time.RFC3339), kind)
	}
	for _, a := range got {
		var resp struct {
			Results []struct {
				Rate map[string]json.RawMessage `json:"rate"`
			} `json:"results"`
		}
		if json.Unmarshal(a.body, &resp) != nil {
			continue
		}
		for _, r := range resp.Results {
			p.printf("  as sent: bid %s, ask %s, date %s, quoteType %s\n", r.Rate["bid"], r.Rate["ask"], r.Rate["date"], r.Rate["quoteType"])
		}
	}
}

// candles prints the last 3 one-minute and one-day bars.
func (p *prober) candles(ctx context.Context, id domain.InstrumentID) {
	for _, c := range []struct {
		name, step string
		iv         time.Duration
	}{{"one-minute", "candles_1m", time.Minute}, {"one-day", "candles_1d", 24 * time.Hour}} {
		p.printf("Last 3 %s candles\n", c.name)
		bars, err := p.r.Candles(ctx, id, c.iv, 3)
		p.answers(c.step)
		if err != nil {
			p.fail(c.step, err)
			continue
		}
		if len(bars) == 0 {
			p.printf("  none\n")
		}
		for _, b := range bars {
			p.printf("  %s  open %s high %s low %s close %s volume %d\n", b.Start.Format(time.RFC3339), b.Open, b.High, b.Low, b.Close, b.Volume)
		}
	}
}

// eligibility prints cactusTrade's reading and every leverage configuration eToro sent,
// isPotential included.
func (p *prober) eligibility(ctx context.Context, id domain.InstrumentID) (domain.Eligibility, bool) {
	p.printf("Eligibility\n")
	els, err := p.r.Eligibility(ctx, []domain.InstrumentID{id})
	got := p.answers("eligibility")
	if err != nil {
		p.fail("eligibility", err)
		return domain.Eligibility{}, false
	}
	i := slices.IndexFunc(els, func(e domain.Eligibility) bool { return e.Instrument == id })
	if i < 0 {
		p.printf("  no answer for instrument %d\n", id)
		return domain.Eligibility{}, false
	}
	el := els[i]
	p.printf("  long (real, x1):  %s\n", allowed(el.RealLong, el.Reason, el.MinPositionAmount))
	p.printf("  short (cfd, x1):  %s\n", allowed(el.ShortCFD, el.ShortReason, el.MinShortAmount))
	for _, line := range leverageConfigLines(got, int64(id)) {
		p.printf("  %s\n", line)
	}
	return el, true
}

func allowed(ok bool, reason string, minAmount domain.Cents) string {
	if !ok {
		return "no: " + reason
	}
	if minAmount > 0 {
		return fmt.Sprintf("yes (minimum $%s)", minAmount)
	}
	return "yes"
}

// leverageConfigLines lists, as eToro sent them, allowOpenPosition, the answer's currency and
// every leverageConfigs entry for the instrument (all fields, sorted by name).
func leverageConfigLines(answers []rawAnswer, id int64) []string {
	var out []string
	for _, a := range answers {
		var resp struct {
			Currency      json.RawMessage `json:"currency"`
			Eligibilities []struct {
				InstrumentID      int64                        `json:"instrumentId"`
				AllowOpenPosition json.RawMessage              `json:"allowOpenPosition"`
				LeverageConfigs   []map[string]json.RawMessage `json:"leverageConfigs"`
			} `json:"eligibilities"`
		}
		if json.Unmarshal(a.body, &resp) != nil {
			continue
		}
		for _, e := range resp.Eligibilities {
			if e.InstrumentID != id {
				continue
			}
			out = append(out, fmt.Sprintf("as sent: allowOpenPosition %s, currency %s, %d leverageConfigs", e.AllowOpenPosition, resp.Currency, len(e.LeverageConfigs)))
			for _, lc := range e.LeverageConfigs {
				keys := slices.Sorted(maps.Keys(lc))
				parts := make([]string, 0, len(keys))
				for _, k := range keys {
					parts = append(parts, k+"="+compactJSON(lc[k]))
				}
				out = append(out, "  "+strings.Join(parts, " "))
			}
		}
	}
	return out
}

// costs prints eToro's costs for a $100 long, and for a $100 short when eToro offers one.
func (p *prober) costs(ctx context.Context, id domain.InstrumentID, el domain.Eligibility, known bool) {
	for _, d := range []domain.Direction{domain.Long, domain.Short} {
		p.printf("Costs of a $%s %s\n", probeAmount, d)
		if d == domain.Short && (!known || !el.ShortCFD) {
			p.printf("  not asked: no CFD short at x1 is offered\n")
			continue
		}
		items, err := p.r.Costs(ctx, id, d, probeAmount)
		p.answers("costs_" + string(d))
		if err != nil {
			p.fail("costs "+string(d), err)
			continue
		}
		if len(items) == 0 {
			p.printf("  none listed\n")
		}
		for _, it := range items {
			p.printf("  %-16s %s %s\n", it.Type, it.Value, it.Currency)
		}
	}
}
