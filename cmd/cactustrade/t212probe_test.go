package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/adapters/trading212"
	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// TestT212ProbeReaderIsReadOnly pins the probe's reader to the four reads: nothing that
// places or cancels an order is reachable from it.
func TestT212ProbeReaderIsReadOnly(t *testing.T) {
	tp := reflect.TypeFor[t212ProbeReader]()
	var got []string
	for i := range tp.NumMethod() {
		got = append(got, tp.Method(i).Name)
	}
	if want := []string{"Account", "History", "Instruments", "OpenOrders"}; !slices.Equal(got, want) {
		t.Fatalf("methods %v, want %v", got, want)
	}
}

// TestT212ProbeSendsGETsOnly runs the probe against a fake Trading 212 serving the adapter's
// synthetic fixtures: every request is a GET to the practice host, the output names what was
// read, and --record writes sanitised answers (the instrument list cut to its head).
func TestT212ProbeSendsGETsOnly(t *testing.T) {
	var (
		mu      sync.Mutex
		methods []string
	)
	fixture := map[string]string{
		"/api/v0/equity/account/summary":      "summary.json",
		"/api/v0/equity/positions":            "positions.json",
		"/api/v0/equity/orders":               "orders.json",
		"/api/v0/equity/metadata/instruments": "instruments.json",
		"/api/v0/equity/history/orders":       "history_orders_p1.json",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method+" "+r.URL.Path)
		mu.Unlock()
		name, ok := fixture[r.URL.Path]
		if !ok || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		data, err := os.ReadFile(filepath.Join("..", "..", "internal", "adapters", "trading212", "testdata", name))
		if err != nil {
			t.Errorf("fixture %s: %v", name, err)
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	acct := domain.Account{ID: "t212-practice", Name: "Practice", Mode: domain.ModeDemo, Platform: domain.PlatformTrading212}
	sec := &fakes.SecretStore{}
	ak, uk := domain.AccountKeyNames(acct)
	_ = sec.Put(ctx, ak, "probe-key-value")
	_ = sec.Put(ctx, uk, "probe-secret-value")
	raw := &t212Answers{}
	c, err := trading212.New(trading212.Config{
		DemoBaseURL: srv.URL, LiveBaseURL: "https://live.invalid", HTTP: srv.Client(), Secrets: sec, Log: slog.New(slog.DiscardHandler),
		RecordAnswer: raw.record, Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var out bytes.Buffer
	if err := t212Probe(ports.WithAccount(ctx, acct), c, raw, t212ProbeOptions{ticker: "BARCl_EQ", showRaw: true, recordDir: dir}, &out); err != nil {
		t.Fatalf("probe: %v\n%s", err, out.String())
	}
	for _, m := range methods {
		if !strings.HasPrefix(m, "GET ") {
			t.Fatalf("a %s from the probe", m)
		}
	}
	if len(methods) != 5 {
		t.Fatalf("requests %v", methods)
	}
	for _, want := range []string{"Account (GBP", "Positions:", "Active orders:", "Instruments:", "Order history (BARCl_EQ"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, out.String())
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 5 {
		t.Fatalf("recorded %v", files)
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if bytes.Contains(b, []byte("probe-key-value")) || bytes.Contains(b, []byte("probe-secret-value")) {
			t.Fatalf("%s holds a key", f)
		}
	}
	if strings.Contains(out.String(), "probe-key-value") {
		t.Fatal("the output holds a key")
	}
}

// TestT212ProbeAccount: only a Trading 212 practice account with both keys is read with.
func TestT212ProbeAccount(t *testing.T) {
	ctx := context.Background()
	st := fakes.NewStore()
	sec := &fakes.SecretStore{}
	u := domain.User{ID: "u1", Username: "olga", Role: domain.RoleOperator}
	if err := st.Users().Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	mk := func(id string, p domain.Platform, m domain.TradingMode, keys bool) {
		a := domain.Account{ID: id, Name: id, OwnerID: u.ID, Mode: m, Platform: p}
		if err := st.Accounts().Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		if keys {
			ak, uk := domain.AccountKeyNames(a)
			_ = sec.Put(ctx, ak, "k")
			_ = sec.Put(ctx, uk, "s")
		}
	}
	mk("practice", domain.PlatformTrading212, domain.ModeDemo, true)
	mk("isa", domain.PlatformTrading212, domain.ModeReal, true)
	mk("etoro", domain.PlatformEtoro, domain.ModeDemo, true)
	mk("nokeys", domain.PlatformTrading212, domain.ModeDemo, false)
	tests := []struct {
		id   string
		want error
	}{
		{"practice", nil},
		{"isa", domain.ErrRealModeGated},
		{"etoro", domain.ErrRealModeGated},
		{"nokeys", domain.ErrCredentialsMissing},
		{"missing", domain.ErrNotFound},
	}
	for _, tt := range tests {
		a, err := t212ProbeAccount(ctx, st.Accounts(), sec, tt.id)
		switch {
		case tt.want == nil && (err != nil || a.ID != tt.id):
			t.Errorf("%s: %v", tt.id, err)
		case tt.want != nil && !errors.Is(err, tt.want):
			t.Errorf("%s: %v, want %v", tt.id, err, tt.want)
		}
	}
}

// TestStartTradingReconcilesTrading212First (R45.10): with the real use cases on fakes, the
// start-up reconciliation reads the Trading 212 practice account before the supervisor takes
// its lease (ReconcileAll covers Trading 212 before any loop), it comes out reconciled, and
// nothing is placed at start-up.
func TestStartTradingReconcilesTrading212First(t *testing.T) {
	ctx := context.Background()
	sim := &fakes.T212Sim{Currency: "GBP", Cash: 100_000}
	var e *tradingEnv
	w := &leaseWatch{T212Broker: sim}
	e = newTradingEnvWith(t, func(d *app.Deps) {
		sim.Now = d.Clock.Now
		d.T212 = w
	})
	v, err := e.svc.CreateAccountOn(ctx, e.p, domain.PlatformTrading212, "Practice", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	w.leased = func() bool {
		l, err := e.st.AgentLeases().Get(ctx, v.ID)
		return err == nil && l.Holder == "this-process"
	}
	sec := e.sec
	ak, uk := domain.AccountKeyNames(v.Account)
	_ = sec.Put(ctx, ak, "k")
	_ = sec.Put(ctx, uk, "s")
	stop := startTrading(ctx, e.svc, slog.New(slog.DiscardHandler), prodSchedule())
	deadline := time.Now().Add(10 * time.Second)
	for !w.leased() {
		if time.Now().After(deadline) {
			t.Fatal("the supervisor did not take the Trading 212 lease")
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if first, ok := w.first(); !ok || first {
		t.Fatalf("the first Trading 212 read came after the supervisor took the lease (seen %v)", ok)
	}
	if r := e.svc.Reconciled(v.ID); !r.OK {
		t.Fatalf("not reconciled: %+v", r)
	}
	if sim.Places != 0 || len(sim.CancelIDs) != 0 {
		t.Fatalf("orders at start-up: %d places, %d cancels", sim.Places, len(sim.CancelIDs))
	}
}

// leaseWatch notes whether the account's lease was held at the first Trading 212 read.
type leaseWatch struct {
	ports.T212Broker
	mu       sync.Mutex
	leased   func() bool
	seen     bool
	atLeased bool
}

func (w *leaseWatch) note() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.seen && w.leased != nil {
		w.seen, w.atLeased = true, w.leased()
	}
}

func (w *leaseWatch) first() (leased, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.atLeased, w.seen
}

func (w *leaseWatch) Account(ctx context.Context) (domain.T212Account, error) {
	w.note()
	return w.T212Broker.Account(ctx)
}

// TestRealTrading212NeverTicks (R45.10, D41 g): with CACTUSTRADE_REAL_TRADING=on and its keys
// set, a real Trading 212 account is never reconciled, supervised, ticked or started, and
// Trading 212 sees no request for it.
func TestRealTrading212NeverTicks(t *testing.T) {
	ctx := context.Background()
	sim := &fakes.T212Sim{Currency: "GBP", Cash: 100_000}
	e := newTradingEnvCfg(t, func(d *app.Deps) {
		sim.Now = d.Clock.Now
		d.T212 = sim
	}, app.Config{Version: "test", LeaseHolder: "this-process", RealTrading: true})
	v, err := e.svc.CreateAccountOn(ctx, e.p, domain.PlatformTrading212, "ISA", domain.ModeReal)
	if err != nil {
		t.Fatal(err)
	}
	sec := e.sec
	ak, uk := domain.AccountKeyNames(v.Account)
	_ = sec.Put(ctx, ak, "k")
	_ = sec.Put(ctx, uk, "s")
	rs, err := e.svc.ReconcileAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.AccountID == v.ID {
			t.Fatalf("a real Trading 212 account was reconciled: %+v", r)
		}
	}
	e.svc.SetAgentLoop(true)
	if _, err := e.svc.StartAgent(ctx, e.p, v.ID); !errors.Is(err, domain.ErrViewOnly) && !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("start: %v", err)
	}
	if tr, err := e.svc.AgentTick(ctx, v.ID); err == nil && tr.Skipped == "" {
		t.Fatalf("ticked: %+v", tr)
	}
	if rep, err := e.svc.Supervise(ctx, v.ID); err == nil && rep.Skipped == "" {
		t.Fatalf("supervised: %+v", rep)
	}
	if sim.Reads != 0 || sim.Places != 0 || len(sim.Violations) != 0 {
		t.Fatalf("Trading 212 saw the real account: reads %d places %d violations %v", sim.Reads, sim.Places, sim.Violations)
	}
}

// TestCatalogueLoop (R45.10): the Trading 212 instrument list is refreshed first after
// first, then every interval, until the context ends; a schedule without it runs nothing.
func TestCatalogueLoop(t *testing.T) {
	h := newOrderHook()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		catalogueLoop(ctx, h, slog.New(slog.DiscardHandler), time.Millisecond, time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for countOf(h.seen(), "catalogue") < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("refreshed %d times", countOf(h.seen(), "catalogue"))
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	off := newOrderHook()
	catalogueLoop(context.Background(), off, slog.New(slog.DiscardHandler), 0, time.Hour) // returns at once
	if n := countOf(off.seen(), "catalogue"); n != 0 {
		t.Fatalf("a zero schedule refreshed %d times", n)
	}
}
