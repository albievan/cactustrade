package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/adapters/sqlstore"
	"github.com/albievan/cactustrade/internal/adapters/system"
	"github.com/albievan/cactustrade/internal/adapters/trading212"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// t212ProbeReader is everything t212-probe may ask Trading 212: reads only. It has no method
// that places or cancels anything, so the probe cannot reach one (TestT212ProbeReaderIsReadOnly
// pins the method set; the adapter's allowlist sends these as GETs).
type t212ProbeReader interface {
	Account(ctx context.Context) (domain.T212Account, error)
	Instruments(ctx context.Context) ([]domain.T212Instrument, error)
	OpenOrders(ctx context.Context) ([]domain.BrokerOrder, error)
	History(ctx context.Context, ticker, cursor string) (domain.T212HistoryPage, error)
}

var _ t212ProbeReader = (*trading212.Client)(nil)

// t212ProbeInstruments is how many catalogue entries the probe prints and records.
const t212ProbeInstruments = 20

// t212ProbeCmd prints what a Trading 212 PRACTICE account shows, read only, with its own
// keys (R45.10, for the owner's practice run R45.13):
//
//	cactustrade t212-probe --account ID [--ticker T] [--raw] [--record DIR]
//
// It reads the account summary and positions, the active orders, the head of the instrument
// list (and the --ticker entry) and the first page of the order history (of --ticker, or of
// every ticker). --raw also prints each answer, --record writes each answer to DIR as a JSON
// file; both sanitised (account ids and token-looking fields replaced; the instrument list cut
// to its head). A real Trading 212 account is refused before any request.
func t212ProbeCmd(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("t212-probe", flag.ContinueOnError)
	fs.SetOutput(out)
	acct := fs.String("account", "", "Trading 212 practice account id (required)")
	ticker := fs.String("ticker", "", "a Trading 212 ticker, e.g. BARCl_EQ (its catalogue entry and history)")
	showRaw := fs.Bool("raw", false, "also print each answer's JSON (sanitised)")
	record := fs.String("record", "", "write each answer, sanitised, as a JSON file into this folder")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *acct == "" {
		return errors.New("t212-probe: --account is required")
	}
	raw := &t212Answers{}
	r, err := openT212Research(ctx, getenv, *acct, raw.record)
	if err != nil {
		return fmt.Errorf("t212-probe: %w", err)
	}
	defer func() { _ = r.store.Close() }()
	return t212Probe(ports.WithAccount(ctx, r.acct), r.client, raw, t212ProbeOptions{
		ticker: strings.TrimSpace(*ticker), showRaw: *showRaw, recordDir: *record,
	}, out)
}

// t212Research is what the probe works with: the client, the practice account, the store.
type t212Research struct {
	client *trading212.Client
	acct   domain.Account
	store  *sqlstore.Store
}

// openT212Research opens the store and the secrets as the server does and builds a Trading
// 212 client reading with one practice account's own keys. record receives every read answer.
func openT212Research(ctx context.Context, getenv func(string) string, accountID string, record func(trading212.Op, []byte)) (*t212Research, error) {
	cfg, err := loadConfig(nil, getenv)
	if err != nil {
		return nil, err
	}
	log := newLoggerTo(os.Stderr, cfg.LogLevel)
	store, err := openStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*t212Research, error) { return nil, errors.Join(err, store.Close()) }
	secrets, _, err := openSecrets(ctx, cfg, store, getenv, system.Clock{}, log)
	if err != nil {
		return fail(err)
	}
	a, err := t212ProbeAccount(ctx, store.Accounts(), secrets, accountID)
	if err != nil {
		return fail(err)
	}
	client, err := trading212.New(trading212.Config{HTTP: &http.Client{Timeout: 30 * time.Second}, Secrets: secrets, Log: log, RecordAnswer: record})
	if err != nil {
		return fail(err)
	}
	return &t212Research{client: client, acct: a, store: store}, nil
}

// t212ProbeAccount is the account the probe reads with: a Trading 212 practice account with
// both keys. Anything else (a real Trading 212 account included) is refused before any request.
func t212ProbeAccount(ctx context.Context, accounts ports.Accounts, secrets ports.SecretStore, id string) (domain.Account, error) {
	a, err := accounts.Get(ctx, id)
	if err != nil {
		return domain.Account{}, fmt.Errorf("account %q: %w", id, err)
	}
	if !a.T212Trading() {
		return domain.Account{}, fmt.Errorf("account %q is not a Trading 212 practice account: %w", id, domain.ErrRealModeGated)
	}
	ak, uk := domain.AccountKeyNames(a)
	for _, k := range []domain.CredentialName{ak, uk} {
		if v, err := secrets.Get(ctx, k); err != nil || v == "" {
			return domain.Account{}, fmt.Errorf("account %q does not have both Trading 212 keys set: %w: %w", id, domain.ErrCredentialsMissing, domain.ErrUnavailable)
		}
	}
	return a, nil
}

// t212Answer is one read answer as Trading 212 sent it.
type t212Answer struct {
	op   trading212.Op
	body []byte
}

// t212Answers collects the answers of the probe's reads (trading212.Config.RecordAnswer).
type t212Answers struct {
	mu   sync.Mutex
	list []t212Answer
}

func (a *t212Answers) record(op trading212.Op, body []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.list = append(a.list, t212Answer{op: op, body: body})
}

func (a *t212Answers) take() []t212Answer {
	a.mu.Lock()
	defer a.mu.Unlock()
	l := a.list
	a.list = nil
	return l
}

type t212ProbeOptions struct {
	ticker    string
	showRaw   bool
	recordDir string
}

// t212Prober runs the probe's steps; a failed step is reported and the next one still runs.
type t212Prober struct {
	r    t212ProbeReader
	raw  *t212Answers
	o    t212ProbeOptions
	out  *strings.Builder
	errs []error
	n    int
}

func (p *t212Prober) printf(format string, args ...any) { fmt.Fprintf(p.out, format, args...) }

// t212Probe asks Trading 212, read only, what the practice run needs to see, and prints it.
func t212Probe(ctx context.Context, r t212ProbeReader, raw *t212Answers, o t212ProbeOptions, out io.Writer) error {
	p := &t212Prober{r: r, raw: raw, o: o, out: &strings.Builder{}}
	if o.recordDir != "" {
		if err := os.MkdirAll(o.recordDir, 0o700); err != nil {
			return fmt.Errorf("t212-probe: %w", err)
		}
	}
	p.account(ctx)
	p.orders(ctx)
	p.instruments(ctx)
	p.history(ctx)
	if _, err := io.WriteString(out, p.out.String()); err != nil {
		return err
	}
	if len(p.errs) > 0 {
		return fmt.Errorf("t212-probe: %w", errors.Join(p.errs...))
	}
	return nil
}

// step reports a step's error (if any) and then its answers.
func (p *t212Prober) step(name string, err error) {
	if err != nil {
		p.printf("%s: FAILED: %v\n", name, err)
		p.errs = append(p.errs, fmt.Errorf("%s: %w", name, err))
	}
	for _, a := range p.raw.take() {
		body := a.body
		if a.op == trading212.OpInstruments {
			body = headOf(body, t212ProbeInstruments)
		}
		clean, serr := sanitizeJSON(body)
		if serr != nil {
			p.printf("  (%s: the answer is not JSON; not shown)\n", a.op)
			continue
		}
		if p.o.showRaw {
			p.printf("  --- %s ---\n%s\n", a.op, clean)
		}
		if p.o.recordDir != "" {
			p.n++
			f := filepath.Join(p.o.recordDir, fmt.Sprintf("%02d_%s.json", p.n, a.op))
			if werr := os.WriteFile(f, clean, 0o600); werr != nil {
				p.errs = append(p.errs, werr)
			}
		}
	}
}

// headOf is the first n elements of a JSON array (the answer itself when it is not one).
func headOf(body []byte, n int) []byte {
	var all []json.RawMessage
	if err := json.Unmarshal(body, &all); err != nil || len(all) <= n {
		return body
	}
	b, err := json.Marshal(all[:n])
	if err != nil {
		return body
	}
	return b
}

func (p *t212Prober) account(ctx context.Context) {
	a, err := p.r.Account(ctx)
	if err == nil {
		p.printf("Account (%s, as of %s): free cash %s, held for orders %s, invested %s, total %s\n", a.Currency, a.AsOf.Format(time.RFC3339),
			domain.FormatMoney(a.Currency, a.AvailableToTrade), domain.FormatMoney(a.Currency, a.ReservedForOrders),
			domain.FormatMoney(a.Currency, a.Invested), domain.FormatMoney(a.Currency, a.Equity))
		p.printf("Positions: %d\n", len(a.Positions))
		for _, x := range a.Positions {
			p.printf("  %-14s qty %s (available %s) avg %s now %s cost %s P&L %s\n", x.Ticker, x.Quantity, x.QuantityAvailable,
				x.AvgPrice, x.CurrentPrice, domain.FormatMoney(a.Currency, x.Cost), domain.FormatMoney(a.Currency, x.PnL))
		}
	}
	p.step("account", err)
}

func (p *t212Prober) printOrder(o domain.BrokerOrder) {
	p.printf("  %d %-14s %s %s qty %s filled %s stop %s %s (%s) from %s at %s\n", o.ID, o.Ticker, o.Side, o.Type, o.Quantity, o.FilledQuantity,
		o.StopPrice, o.Status, o.State, o.InitiatedFrom, o.CreatedAt.Format(time.RFC3339))
}

func (p *t212Prober) orders(ctx context.Context) {
	act, err := p.r.OpenOrders(ctx)
	if err == nil {
		p.printf("Active orders: %d\n", len(act))
		for _, o := range act {
			p.printOrder(o)
		}
	}
	p.step("orders", err)
}

func (p *t212Prober) instruments(ctx context.Context) {
	ins, err := p.r.Instruments(ctx)
	if err == nil {
		p.printf("Instruments: %d (first %d shown)\n", len(ins), min(len(ins), t212ProbeInstruments))
		for i, in := range ins {
			if i < t212ProbeInstruments || (p.o.ticker != "" && in.Ticker == p.o.ticker) {
				p.printf("  %-14s %-8s %-4s %-6s max %s ISIN %s %s\n", in.Ticker, in.ShortName, in.Currency, in.Type, in.MaxOpenQuantity, in.ISIN, in.Name)
			}
		}
	}
	p.step("instruments", err)
}

func (p *t212Prober) history(ctx context.Context) {
	h, err := p.r.History(ctx, p.o.ticker, "")
	if err == nil {
		what := "every ticker"
		if p.o.ticker != "" {
			what = p.o.ticker
		}
		p.printf("Order history (%s, first page): %d, more: %v\n", what, len(h.Orders), h.Next != "")
		for _, o := range h.Orders {
			p.printOrder(o)
		}
	}
	p.step("history", err)
}
