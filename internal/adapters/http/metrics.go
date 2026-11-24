package httpapi

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// Trading metrics (PLAN 7.5). The app knows nothing of Prometheus (depguard): the collector
// below reads app.TradingHealth at scrape time, and Observers holds the counters that cmd
// feeds from the eToro client's hooks and the live bus. Every label is an account id or a
// value from a fixed set (states, kinds, operations, quota groups, purposes, outcomes): never
// a name, key, token, path or amount.

// healthTimeout bounds one scrape's reads of the trading health.
const healthTimeout = 5 * time.Second

func desc(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc(name, help, labels, nil)
}

// tradingCollector exports app.TradingHealth on every scrape.
type tradingCollector struct {
	svc *app.Service

	healthy, accountHealthy, info, supervised, agentState, killed, reconciled, lastPass, lastClean,
	leaseHeld, orders, realArmed, lastTick, agentLoop, llmCalls *prometheus.Desc
}

func newTradingCollector(svc *app.Service) *tradingCollector {
	return &tradingCollector{
		svc:            svc,
		healthy:        desc("cactustrade_trading_healthy", "1 when every trading loop is alive (GET /api/v1/system/health answers 200), else 0."),
		accountHealthy: desc("cactustrade_account_healthy", "1 when the account's supervisor pass is recent and its state could be read.", "account"),
		info:           desc("cactustrade_account_info", "Always 1; the account's trading mode.", "account", "mode"),
		supervised:     desc("cactustrade_account_supervised", "1 when the supervisor is expected to pass over the account (eToro demo account with keys).", "account"),
		agentState:     desc("cactustrade_agent_state", "1 for the account's current agent state, 0 for the others.", "account", "state"),
		killed:         desc("cactustrade_agent_killed", "1 when the account's kill switch is on (a read error counts as on).", "account"),
		reconciled:     desc("cactustrade_account_reconciled", "1 when the account's latest reconciliation in this process was OK.", "account"),
		lastPass:       desc("cactustrade_supervisor_last_pass_timestamp_seconds", "End of the account's latest supervisor pass that ran (0 = none in this process).", "account"),
		lastClean:      desc("cactustrade_supervisor_last_clean_pass_timestamp_seconds", "End of the account's latest supervisor pass without errors (0 = none in this process).", "account"),
		leaseHeld:      desc("cactustrade_lease_held", "1 when this process holds the account's single-writer lease.", "account"),
		orders:         desc("cactustrade_orders_non_final", "Our orders that are not final, by state.", "account", "state"),
		realArmed:      desc("cactustrade_real_gate_armed", "1 when real trading is armed for the account (always 0 before PLAN 7.18).", "account"),
		lastTick:       desc("cactustrade_supervisor_last_tick_timestamp_seconds", "The supervisor loop's latest tick (0 = none yet)."),
		agentLoop:      desc("cactustrade_agent_loop_running", "0 when the start-up resume failed and the agent loop does not run."),
		llmCalls:       desc("cactustrade_llm_calls_total", "LLM reviewer calls in this process by purpose and outcome.", "purpose", "outcome"),
	}
}

// Describe sends every descriptor.
func (c *tradingCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		c.healthy, c.accountHealthy, c.info, c.supervised, c.agentState, c.killed, c.reconciled, c.lastPass,
		c.lastClean, c.leaseHeld, c.orders, c.realArmed, c.lastTick, c.agentLoop, c.llmCalls,
	} {
		ch <- d
	}
}

// agentStates are the values of cactustrade_agent_state's state label.
var agentStates = []domain.AgentState{domain.AgentStopped, domain.AgentRunning, domain.AgentPaused, domain.AgentKilled}

// Collect reads the trading health and sends the series.
func (c *tradingCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()
	h := c.svc.TradingHealth(ctx)
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}
	gauge(c.healthy, b2f(h.OK()))
	gauge(c.lastTick, ts(h.LastTick))
	gauge(c.agentLoop, b2f(h.AgentLoop))
	for _, n := range h.LLMCalls {
		ch <- prometheus.MustNewConstMetric(c.llmCalls, prometheus.CounterValue, float64(n.Count), string(n.Purpose), n.Outcome)
	}
	for _, a := range h.Accounts {
		id := a.AccountID
		gauge(c.accountHealthy, b2f(a.Problem == ""), id)
		gauge(c.info, 1, id, string(a.Mode))
		gauge(c.supervised, b2f(a.Supervised), id)
		for _, st := range agentStates {
			gauge(c.agentState, b2f(a.AgentState == st), id, string(st))
		}
		gauge(c.killed, b2f(a.Killed), id)
		gauge(c.reconciled, b2f(a.Reconciled), id)
		gauge(c.lastPass, ts(a.LastPass), id)
		gauge(c.lastClean, ts(a.LastCleanPass), id)
		gauge(c.leaseHeld, b2f(a.LeaseHeld), id)
		gauge(c.realArmed, b2f(a.RealGateArmed), id)
		for _, st := range app.NonFinalOrderStates {
			gauge(c.orders, float64(a.Orders[st]), id, string(st))
		}
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// ts is a time as Unix seconds (0 for the zero time).
func ts(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(t.UnixMilli()) / 1000
}

// Observers are the trading counters fed from outside the HTTP adapter: cmd passes
// EtoroRequest and LimiterWait to the eToro client's hooks and runs CountOrders on the live
// bus. Build them once per registry with NewObservers.
type Observers struct {
	etoroRequests *prometheus.CounterVec
	limiterWaits  *prometheus.CounterVec
	limiterWaited *prometheus.CounterVec
	orderMoves    *prometheus.CounterVec
}

// NewObservers registers the trading counters on reg.
func NewObservers(reg prometheus.Registerer) *Observers {
	o := &Observers{
		etoroRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cactustrade_etoro_requests_total",
			Help: "Requests sent to eToro by allowlisted operation, outcome class (2xx, 3xx, 4xx, 429, 5xx, error) and mode.",
		}, []string{"op", "class", "mode"}),
		limiterWaits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cactustrade_etoro_limiter_waits_total", Help: "Times the client-side eToro rate limiter held a request back, by quota group.",
		}, []string{"quota"}),
		limiterWaited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cactustrade_etoro_limiter_wait_seconds_total", Help: "Time requests were held back by the client-side eToro rate limiter, by quota group.",
		}, []string{"quota"}),
		orderMoves: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cactustrade_order_transitions_total",
			Help: "Stored order transitions (creation included) seen on the live bus, by account, order kind and new state.",
		}, []string{"account", "kind", "state"}),
	}
	reg.MustRegister(o.etoroRequests, o.limiterWaits, o.limiterWaited, o.orderMoves)
	return o
}

// EtoroRequest counts one request to eToro (etoro.Config.Observe). The values come from
// fixed sets: the allowlist's operations, the two modes and the status classes.
func (o *Observers) EtoroRequest(op string, mode domain.TradingMode, class string) {
	if mode != domain.ModeReal {
		mode = domain.ModeDemo
	}
	o.etoroRequests.WithLabelValues(op, class, string(mode)).Inc()
}

// LimiterWait counts one wait of the eToro client's rate limiter (etoro.Config.ObserveWait).
func (o *Observers) LimiterWait(quota string, wait time.Duration) {
	o.limiterWaits.WithLabelValues(quota).Inc()
	o.limiterWaited.WithLabelValues(quota).Add(wait.Seconds())
}

// orderBuffer is CountOrders' subscription buffer: the counter keeps up with any burst the
// executor can produce (8 positions, each a few transitions); a miss is in
// cactustrade_live_bus_dropped_total.
const orderBuffer = 4096

// CountOrders subscribes to the live bus now and, until ctx ends, counts every order
// transition published on it in its own goroutine (so nothing published after the call is
// missed). It only reads the bus: an order never waits for it (a full buffer drops,
// ports.EventBus). done is closed when the counting goroutine has ended.
func (o *Observers) CountOrders(ctx context.Context, svc *app.Service) (done <-chan struct{}) {
	ch := make(chan struct{})
	events, cancel, ok := svc.SubscribeLive(orderBuffer)
	if !ok {
		close(ch)
		return ch
	}
	go func() {
		defer close(ch)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case e, open := <-events:
				if !open {
					return
				}
				o.countOrder(e.Type, e.Data)
			}
		}
	}()
	return ch
}

func (o *Observers) countOrder(typ string, data any) {
	if typ != app.BusOrder {
		return
	}
	ord, ok := data.(domain.Order)
	if !ok || !ord.Kind.Valid() || !ord.State.Valid() {
		return
	}
	o.orderMoves.WithLabelValues(ord.AccountID, string(ord.Kind), string(ord.State)).Inc()
}
