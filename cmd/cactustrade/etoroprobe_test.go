package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/adapters/etoro"
	"github.com/albievan/cactustrade/internal/domain"
)

// methodNames lists an interface's methods.
func methodNames(t reflect.Type) []string {
	var out []string
	for i := range t.NumMethod() {
		out = append(out, t.Method(i).Name)
	}
	slices.Sort(out)
	return out
}

// TestProbeReaderIsReadOnly: the console research commands reach eToro only through
// interfaces of read methods. A new method (say Open, Close or Cancel) fails here first.
func TestProbeReaderIsReadOnly(t *testing.T) {
	for _, c := range []struct {
		iface reflect.Type
		want  []string
	}{
		{reflect.TypeFor[probeReader](), []string{"Candles", "Costs", "Eligibility", "Instruments", "Quotes", "Search"}},
		{reflect.TypeFor[backtestReader](), []string{"Candles", "Costs"}},
	} {
		got := methodNames(c.iface)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s has methods %v, want exactly the reads %v", c.iface, got, c.want)
		}
		for _, m := range got {
			for _, mut := range []string{"Open", "Close", "Cancel", "SetStop", "Place", "Modify", "Lookup", "Portfolio"} {
				if strings.Contains(m, mut) {
					t.Errorf("%s.%s looks like an account operation", c.iface, m)
				}
			}
		}
	}
	// Each read the probe makes is a non-mutating, allowlisted operation.
	for _, op := range []etoro.Op{etoro.OpSearch, etoro.OpInstruments, etoro.OpRates, etoro.OpCandles, etoro.OpEligibility, etoro.OpCosts} {
		if ep, ok := etoro.Allowlist[op]; !ok || ep.Mutates {
			t.Errorf("%s: allowlisted %v, mutates %v", op, ok, ep.Mutates)
		}
	}
}

// fakeProbe answers the probe's reads like eToro would and records each answer as the eToro
// client's RecordAnswer hook does. Its account-changing methods fail the test: they are not
// on probeReader, so the probe cannot reach them.
type fakeProbe struct {
	t       *testing.T
	raw     *rawAnswers
	in      domain.Instrument
	el      domain.Eligibility
	elErr   error
	costs   map[domain.Direction]int
	answers map[etoro.Op]string
}

func (f *fakeProbe) answer(op etoro.Op) { f.raw.record(op, []byte(f.answers[op])) }

func (f *fakeProbe) Search(context.Context, string) ([]domain.Instrument, error) {
	f.answer(etoro.OpSearch)
	return []domain.Instrument{{ID: 9, Symbol: "BAR"}, f.in}, nil
}

func (f *fakeProbe) Instruments(context.Context, []domain.InstrumentID) ([]domain.Instrument, error) {
	f.answer(etoro.OpInstruments)
	return []domain.Instrument{f.in}, nil
}

func (f *fakeProbe) Quotes(context.Context, []domain.InstrumentID) ([]domain.Quote, error) {
	f.answer(etoro.OpRates)
	return []domain.Quote{{Instrument: f.in.ID, Bid: 245_300_000, Ask: 245_500_000, At: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)}}, nil
}

func (f *fakeProbe) Candles(_ context.Context, _ domain.InstrumentID, iv time.Duration, n int) ([]domain.Candle, error) {
	if n != 3 {
		f.t.Errorf("asked for %d candles", n)
	}
	f.answer(etoro.OpCandles)
	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	return []domain.Candle{{Start: t0, Open: 1_000_000, High: 2_000_000, Low: 500_000, Close: 1_500_000, Volume: 7}, {Start: t0.Add(iv), Close: 1}}, nil
}

func (f *fakeProbe) Eligibility(context.Context, []domain.InstrumentID) ([]domain.Eligibility, error) {
	f.answer(etoro.OpEligibility)
	return []domain.Eligibility{f.el}, f.elErr
}

func (f *fakeProbe) Costs(_ context.Context, _ domain.InstrumentID, d domain.Direction, amount domain.Cents) ([]domain.CostItem, error) {
	if amount != 100_00 {
		f.t.Errorf("costs asked for $%s", amount)
	}
	f.costs[d]++
	f.answer(etoro.OpCosts)
	return []domain.CostItem{{Type: "marketSpread", Value: 120_000, Currency: "USD"}}, nil
}

func (f *fakeProbe) Open(context.Context, domain.OrderIntent) (domain.OrderStatus, error) {
	f.t.Fatal("the probe placed an order")
	return domain.OrderStatus{}, nil
}

func (f *fakeProbe) Close(context.Context, int64, domain.InstrumentID, string) (domain.OrderStatus, error) {
	f.t.Fatal("the probe closed a position")
	return domain.OrderStatus{}, nil
}

func (f *fakeProbe) Cancel(context.Context, int64, string) error {
	f.t.Fatal("the probe cancelled an order")
	return nil
}

func newFakeProbe(t *testing.T) *fakeProbe {
	return &fakeProbe{
		t: t, raw: &rawAnswers{}, costs: map[domain.Direction]int{},
		in: domain.Instrument{ID: 2002, Symbol: "EXM.L", Name: "Example plc", Type: "Stocks", ExchangeID: 7, Market: domain.MarketLSE},
		el: domain.Eligibility{Instrument: 2002, RealLong: true, MinPositionAmount: 10_00, ShortCFD: true, MinShortAmount: 50_00},
		answers: map[etoro.Op]string{
			etoro.OpSearch:      `{"results":[{"instrumentId":2002,"symbol":"EXM.L","displayName":"Example plc","priceCurrency":"GBX"}]}`,
			etoro.OpInstruments: `{"results":[{"instrumentId":2002,"symbol":"EXM.L","exchangeId":7}],"pagination":{"hasNext":false,"nextPageToken":"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl"}}`,
			etoro.OpRates:       `{"results":[{"rate":{"instrumentId":2002,"bid":245.30,"ask":245.50,"date":"2026-11-24T10:00:00Z","quoteType":"realtime"}}]}`,
			etoro.OpCandles:     `{"candles":[{"candles":[{"fromDate":"2026-11-24T10:00:00Z","open":1,"high":2,"low":0.5,"close":1.5,"volume":7}]}]}`,
			etoro.OpEligibility: `{"currency":"USD","gcid":31337001,"eligibilities":[{"instrumentId":2002,"symbol":"EXM.L","allowOpenPosition":true,"leverageConfigs":[{"settlementType":"real","direction":"long","leverageValues":[1],"isPotential":true,"minPositionAmount":10}]}]}`,
			etoro.OpCosts:       `{"instrumentId":2002,"accountId":31337002,"username":"someone","costs":[{"costType":"marketSpread","value":0.12,"currency":"USD"}]}`,
		},
	}
}

// TestProbe: every section is printed from read answers only; the raw answers are printed and
// recorded sanitised, with eToro's numbers exactly as sent.
func TestProbe(t *testing.T) {
	f := newFakeProbe(t)
	dir := filepath.Join(t.TempDir(), "rec")
	var out strings.Builder
	if err := probe(context.Background(), f, f.raw, probeOptions{symbol: "exm.l", showRaw: true, recordDir: dir}, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{
		"id 2002, symbol EXM.L", "market LSE, exchange id 7", `currency fields: priceCurrency="GBX"`,
		"bid 245.3, ask 245.5, spread 0.2", "as sent: bid 245.30, ask 245.50",
		"Last 3 one-minute candles", "Last 3 one-day candles", "open 1 high 2 low 0.5 close 1.5 volume 7",
		"long (real, x1):  yes (minimum $10.00)", "short (cfd, x1):  yes (minimum $50.00)",
		"as sent: allowOpenPosition true, currency \"USD\", 1 leverageConfigs", "isPotential=true", `leverageValues=[1]`,
		"Costs of a $100.00 long", "Costs of a $100.00 short", "marketSpread     0.12 USD", "Q1 (LSE units)",
		"raw rates (rates, sanitised)", `"bid":245.30`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
	if f.costs[domain.Long] != 1 || f.costs[domain.Short] != 1 {
		t.Errorf("costs asked %v, want one long and one short", f.costs)
	}
	for _, leak := range []string{"31337001", "31337002", "someone", "eyJhbGci"} {
		if strings.Contains(s, leak) {
			t.Errorf("output leaks %q", leak)
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, leak := range []string{"31337001", "31337002", "someone", "eyJhbGci"} {
			if strings.Contains(string(b), leak) {
				t.Errorf("%s leaks %q:\n%s", f, leak, b)
			}
		}
	}
	want := []string{
		"exm_l_candles_1d.json", "exm_l_candles_1m.json", "exm_l_costs_long.json", "exm_l_costs_short.json",
		"exm_l_eligibility.json", "exm_l_instrument.json", "exm_l_rates.json", "exm_l_search.json",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("recorded %v, want %v", names, want)
	}
	el, err := os.ReadFile(filepath.Join(dir, "exm_l_eligibility.json"))
	if err != nil || !strings.Contains(string(el), `"gcid": 0`) || !strings.Contains(string(el), `"isPotential": true`) {
		t.Fatalf("eligibility recording:\n%s %v", el, err)
	}
	rates, err := os.ReadFile(filepath.Join(dir, "exm_l_rates.json"))
	if err != nil || !strings.Contains(string(rates), `"bid": 245.30`) {
		t.Fatalf("rates recording must keep eToro's numbers as sent:\n%s %v", rates, err)
	}
}

// TestProbeFailuresAndShort: a failed step is reported and the rest still runs; no short
// costs are asked when no CFD short is offered; the exit status reports the failure; without
// --raw or --record nothing is printed raw or written.
func TestProbeFailuresAndShort(t *testing.T) {
	f := newFakeProbe(t)
	f.el = domain.Eligibility{Instrument: 2002, RealLong: false, Reason: "not offered as a real share, long, at leverage 1", ShortCFD: false, ShortReason: "nope"}
	var out strings.Builder
	if err := probe(context.Background(), f, f.raw, probeOptions{symbol: "EXM.L"}, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if f.costs[domain.Short] != 0 || !strings.Contains(s, "not asked: no CFD short at x1 is offered") || !strings.Contains(s, "long (real, x1):  no: not offered") {
		t.Fatalf("short costs asked %d:\n%s", f.costs[domain.Short], s)
	}
	if strings.Contains(s, "raw ") || strings.Contains(s, "recorded ") {
		t.Fatalf("raw answers shown without --raw:\n%s", s)
	}

	f = newFakeProbe(t)
	f.elErr = domain.ErrUnavailable
	out.Reset()
	err := probe(context.Background(), f, f.raw, probeOptions{symbol: "EXM.L"}, &out)
	if !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(out.String(), "eligibility failed") || f.costs[domain.Long] != 1 || f.costs[domain.Short] != 0 {
		t.Fatalf("eligibility failure: %v, costs %v:\n%s", err, f.costs, out.String())
	}

	f = newFakeProbe(t)
	f.answers[etoro.OpRates] = `not json`
	out.Reset()
	err = probe(context.Background(), f, f.raw, probeOptions{symbol: "EXM.L", showRaw: true}, &out)
	if err == nil || !strings.Contains(out.String(), "sanitising the rates answer failed") || strings.Contains(out.String(), "not json") {
		t.Fatalf("an unreadable answer must never be shown or written as is: %v\n%s", err, out.String())
	}

	f = newFakeProbe(t)
	out.Reset()
	err = probe(context.Background(), f, f.raw, probeOptions{symbol: "NOPE"}, &out)
	if !errors.Is(err, domain.ErrNotFound) || !strings.Contains(err.Error(), "EXM.L (2002)") || f.costs[domain.Long] != 0 {
		t.Fatalf("unknown symbol: %v", err)
	}
}

func TestRecordName(t *testing.T) {
	if n := recordName("BARC.L", "rates", 0); n != "barc_l_rates.json" {
		t.Fatal(n)
	}
	if n := recordName("../x", "instrument", 1); n != "___x_instrument_2.json" {
		t.Fatal(n)
	}
}
