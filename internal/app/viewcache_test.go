package app

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

func TestViewCacheAgentStatusViewConcurrent(t *testing.T) {
	x := newExecEnv(t)
	x.svc.rt.views = newViewCache()
	cb := &countingBroker{SimBroker: x.b, delay: 20 * time.Millisecond}
	x.svc.d.Broker = cb
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := range 50 {
		wg.Go(func() {
			<-start
			if i%10 == 9 {
				x.clock.Advance(100 * time.Millisecond) // the 50 calls spread over < 10 s
			}
			v, err := x.svc.AgentStatusView(x.ctx, x.admin, x.a.ID)
			switch {
			case err != nil:
				errs <- err
			case !v.EquityKnown || v.Equity != x.b.Actual().Equity():
				errs <- fmt.Errorf("view equity %v %d", v.EquityKnown, v.Equity)
			}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := cb.count(); n > 1 {
		t.Fatalf("50 concurrent views made %d portfolio reads, want ≤ 1", n)
	}
	// Once the cached read is viewCacheTTL old, the next view reads again (once).
	x.clock.Advance(viewCacheTTL)
	for range 3 {
		if _, err := x.svc.AgentStatusView(x.ctx, x.admin, x.a.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n := cb.count(); n != 2 {
		t.Fatalf("reads after the TTL = %d, want 2", n)
	}
}

func TestViewCache(t *testing.T) {
	tests := []struct {
		name  string
		run   func(t *testing.T, x *execEnv, cb *countingBroker)
		reads int
	}{
		{"supervisor's read is used: no extra call", func(t *testing.T, x *execEnv, _ *countingBroker) {
			x.supervise(t)
			x.clock.Advance(viewCacheTTL - time.Second)
			if v, err := x.svc.AgentStatusView(x.ctx, x.admin, x.a.ID); err != nil || !v.EquityKnown {
				t.Fatalf("view = %+v %v", v, err)
			}
		}, 1},
		{"an error is not cached", func(t *testing.T, x *execEnv, _ *countingBroker) {
			x.b.PortfolioErr = fmt.Errorf("HTTP 503: %w", domain.ErrUnavailable)
			if v, _ := x.svc.AgentStatusView(x.ctx, x.admin, x.a.ID); v.EquityKnown || !strings.Contains(v.EquityError, "503") {
				t.Fatalf("view = %+v", v)
			}
			x.b.PortfolioErr = nil
			if v, _ := x.svc.AgentStatusView(x.ctx, x.admin, x.a.ID); !v.EquityKnown {
				t.Fatalf("view = %+v", v)
			}
		}, 2},
		{"an older read never replaces a newer one", func(t *testing.T, x *execEnv, _ *countingBroker) {
			now := x.clock.Now()
			x.svc.rt.views.put(x.a.ID, domain.Portfolio{AvailableCash: 2}, now)
			x.svc.rt.views.put(x.a.ID, domain.Portfolio{AvailableCash: 1}, now.Add(-time.Second))
			if pf, at, ok := x.svc.rt.views.peek(x.a.ID); !ok || pf.AvailableCash != 2 || !at.Equal(now) {
				t.Fatalf("cached = %+v %s", pf, at)
			}
		}, 0},
		{"each account has its own entry", func(t *testing.T, x *execEnv, _ *countingBroker) {
			other := x.account(t, "Other", domain.ModeDemo)
			for _, id := range []string{x.a.ID, other.ID, x.a.ID, other.ID} {
				if _, err := x.svc.AgentStatusView(x.ctx, x.admin, id); err != nil {
					t.Fatal(err)
				}
			}
		}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newExecEnv(t)
			x.svc.rt.views = newViewCache()
			cb := &countingBroker{SimBroker: x.b}
			x.svc.d.Broker = cb
			tt.run(t, x, cb)
			if n := cb.count(); n != tt.reads {
				t.Fatalf("portfolio reads = %d, want %d", n, tt.reads)
			}
		})
	}
}

// slowBroker holds every portfolio read until release is closed.
type slowBroker struct {
	*countingBroker
	release chan struct{}
}

func (s *slowBroker) Portfolio(ctx context.Context) (domain.Portfolio, error) {
	<-s.release
	return s.countingBroker.Portfolio(ctx)
}

func TestViewCacheWaiterLeaves(t *testing.T) {
	x := newExecEnv(t)
	x.svc.rt.views = newViewCache()
	sb := &slowBroker{countingBroker: &countingBroker{SimBroker: x.b}, release: make(chan struct{})}
	x.svc.d.Broker = sb
	v := domain.AccountView{Account: x.a, Access: domain.AccessOwner}
	firstCtx, cancelFirst := context.WithCancel(x.ctx)
	first := make(chan error, 1)
	go func() {
		_, _, err := x.svc.viewPortfolio(firstCtx, v)
		first <- err
	}()
	// wait until the first read is in flight
	for {
		x.svc.rt.views.mu.Lock()
		n := len(x.svc.rt.views.flights)
		x.svc.rt.views.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	waitCtx, cancelWait := context.WithCancel(x.ctx)
	cancelWait()
	if _, _, err := x.svc.viewPortfolio(waitCtx, v); !errors.Is(err, context.Canceled) {
		t.Fatalf("a waiter that left = %v", err)
	}
	cancelFirst() // the first caller leaves too: the shared read still completes
	close(sb.release)
	if err := <-first; err != nil {
		t.Fatalf("the shared read = %v", err)
	}
	if _, _, ok := x.svc.rt.views.peek(x.a.ID); !ok || sb.count() != 1 {
		t.Fatalf("cached %v after %d reads", ok, sb.count())
	}
}

// TestEntriesNeverUseTheViewCache is guardrail §2.2 (PLAN 6.6a): an entry is sized from
// eToro's cash read fresh, never from the view cache, even when the cache holds 100× more.
func TestEntriesNeverUseTheViewCache(t *testing.T) {
	ae := newAgentEnv(t)
	const cash = 500_00
	// Most of the equity is the gain of a $1 position cactusTrade did not open, so the
	// budget left and the floors are unchanged while the cash is small.
	equity := ae.b.Actual().Equity()
	ae.b.Cash = cash
	ae.b.Positions = append(ae.b.Positions, domain.Position{
		ID: 4242, Instrument: 4242, Amount: 1_00, UnrealisedPnL: equity - cash - 1_00, RealAsset: true, Long: true, Leverage: 1, OpenedAt: ae.clock.Now(),
	})
	primed := ae.b.Actual()
	primed.AvailableCash = 100 * cash
	ae.svc.rt.views.put(ae.a.ID, primed, ae.clock.Now())

	reserved := domain.Cents(0)
	var fresh []domain.Cents
	ae.b.OnOpen = func(ctx context.Context, in domain.OrderIntent) error {
		fresh = append(fresh, cash-reserved) // the SimBroker's cash before this open
		return nil
	}
	tr := ae.tick(t)
	if ae.b.Opens != 1 || tr.Entries != 1 {
		t.Fatalf("opens %d, trace %+v", ae.b.Opens, tr)
	}
	for i, in := range ae.b.Intents {
		if in.Amount > fresh[i] || in.Amount <= 0 {
			t.Fatalf("open %d amount $%s, fresh cash − reserved $%s (cache said $%s)", i, in.Amount, fresh[i], primed.AvailableCash)
		}
	}
	if pf, _, _ := ae.svc.rt.views.peek(ae.a.ID); pf.AvailableCash != 100*cash {
		t.Fatalf("the tick wrote to the view cache: %+v", pf)
	}
}

// orderPathFiles may never read the view cache (CLAUDE.md §2.2); supervisor.go may only store
// its own read into it.
var orderPathFiles = []string{"agent.go", "executor.go", "risk.go", "supervisor.go"}

// viewCacheReaders are the identifiers that read the view cache, directly or through a view
// use case. (Declaring the runtime's views field and building it, in executor.go, read
// nothing.)
var viewCacheReaders = []string{
	"viewPortfolio", "peek", "entries", "flights", "viewEquity", "AgentStatusView", "LiveSnapshot", "WarmLiveView",
	"WatchlistEntries", "WatchlistOverview", "watchHoldings", // R27
}

// cacheReads lists the view-cache reads in one Go source file: any viewCacheReaders
// identifier, and any use of the runtime's views field except storeOK's `….views.put(…)`.
func cacheReads(t *testing.T, name string, src any, storeOK bool) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[*ast.SelectorExpr]bool{}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if inner, ok := n.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "views" && n.Sel.Name == "put" && storeOK {
				allowed[inner] = true
			}
			if n.Sel.Name == "views" && !allowed[n] {
				out = append(out, fmt.Sprintf("%s: .views", fset.Position(n.Pos())))
			}
		case *ast.Ident:
			if slices.Contains(viewCacheReaders, n.Name) {
				out = append(out, fmt.Sprintf("%s: %s", fset.Position(n.Pos()), n.Name))
			}
		}
		return true
	})
	return out
}

func TestViewCacheReadersAreViewsOnly(t *testing.T) {
	for _, name := range orderPathFiles {
		if reads := cacheReads(t, name, nil, name == "supervisor.go"); len(reads) > 0 {
			t.Errorf("%s reads the view cache (guardrail §2.2): %v", name, reads)
		}
	}
	// supervisor.go stores its read exactly once.
	if n := selectorCalls(t, "supervisor.go")["views.put"]; n != 1 {
		t.Fatalf("supervisor.go stores into the view cache %d times, want 1", n)
	}
	// Only the view files read the cache at all.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	views := []string{"viewcache.go", "agent_view.go", "live.go", "watchlist_view.go"} // R27: the watchlist page's holdings
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || slices.Contains(views, name) {
			continue
		}
		calls := selectorCalls(t, name)
		for _, id := range []string{"viewPortfolio", "views.peek", "viewEquity"} {
			if calls[id] > 0 {
				t.Errorf("%s calls %s: only %v may read the view cache", name, id, views)
			}
		}
	}
}

// selectorCalls counts the method calls of one Go file by their last one or two names
// ("viewPortfolio", "views.put").
func selectorCalls(t *testing.T, name string) map[string]int {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	ast.Inspect(f, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
			out[sel.Sel.Name]++
			if inner, ok := sel.X.(*ast.SelectorExpr); ok {
				out[inner.Sel.Name+"."+sel.Sel.Name]++
			}
		}
		return true
	})
	return out
}

func TestViewCacheSourceTestCatchesReads(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		storeOK bool
		want    int
	}{
		{"a planted read", "package app\nfunc f(s *Service) { s.rt.views.peek(\"a\") }", false, 2},
		{"a planted viewPortfolio", "package app\nfunc f(s *Service) { s.viewPortfolio(nil, v) }", false, 1},
		{"the store call where allowed", "package app\nfunc f(s *Service) { s.rt.views.put(\"a\", pf, at) }", true, 0},
		{"the store call elsewhere", "package app\nfunc f(s *Service) { s.rt.views.put(\"a\", pf, at) }", false, 1},
		{"a store call next to a read", "package app\nfunc f(s *Service) { s.rt.views.put(\"a\", pf, at); _ = s.rt.views }", true, 1},
		{"a view use case", "package app\nfunc f(s *Service) { s.AgentStatusView(ctx, p, \"\") }", true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cacheReads(t, "planted.go", tt.src, tt.storeOK); len(got) != tt.want {
				t.Fatalf("reads = %v, want %d", got, tt.want)
			}
		})
	}
}

var _ ports.Broker = (*slowBroker)(nil)
