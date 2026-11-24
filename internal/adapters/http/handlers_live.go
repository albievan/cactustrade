package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// The live stream (PLAN 6.9): GET /api/v1/live sends one account's live view as server-sent
// events. It is a view only (CLAUDE.md §2.6): nothing is decided on it, it never calls eToro
// except through WarmLiveView's shared, rate-bounded cache read on connect, and the kill
// switch and every exit work without it. Who may view what is decided by the use cases
// (LiveSnapshot, LiveFilter); the stream only forwards what they allow.

// liveRoute is the stream's route pattern (the request histogram skips it).
const liveRoute = "GET /api/v1/live"

// liveSnapshotGap is the shortest time between two snapshots sent because the agent's state
// or the portfolio changed (the periodic snapshot is separate).
const liveSnapshotGap = time.Second

// LiveLimits tunes the live stream. Zero fields take PLAN 6.9's values.
type LiveLimits struct {
	Snapshot     time.Duration // a snapshot this often (5 s)
	Ping         time.Duration // a ": ping" comment this often (15 s)
	Recheck      time.Duration // the session or API key and the access are checked again this often (30 s)
	Lifetime     time.Duration // the stream ends with bye "lifetime" after this; the client reconnects (30 min)
	WriteTimeout time.Duration // one write may take at most this long (10 s)
	Warm         time.Duration // waiting for the portfolio read on connect at most this long (5 s)
	PerUser      int           // open streams per user (4)
	Total        int           // open streams on this server (200)
	Buffer       int           // bus events held for a stream that is behind (256)
}

func (l LiveLimits) withDefaults() LiveLimits {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&l.Snapshot, 5*time.Second)
	def(&l.Ping, 15*time.Second)
	def(&l.Recheck, 30*time.Second)
	def(&l.Lifetime, 30*time.Minute)
	def(&l.WriteTimeout, 10*time.Second)
	def(&l.Warm, 5*time.Second)
	if l.PerUser <= 0 {
		l.PerUser = 4
	}
	if l.Total <= 0 {
		l.Total = 200
	}
	if l.Buffer <= 1 {
		l.Buffer = 256
	}
	return l
}

// liveStreams counts the open streams per user and in total and ends them all on shutdown.
type liveStreams struct {
	limits  LiveLimits
	gauge   prometheus.Gauge
	mu      sync.Mutex
	perUser map[string]int
	total   int
	closed  bool
	done    chan struct{} // closed by close: every stream says bye and ends
}

func newLiveStreams(l LiveLimits, g prometheus.Gauge) *liveStreams {
	return &liveStreams{limits: l, gauge: g, perUser: map[string]int{}, done: make(chan struct{})}
}

// acquire takes a stream slot for user. status is 0 when it got one, 429 at a cap and 503
// after shutdown began; release gives the slot back (idempotent).
func (ls *liveStreams) acquire(user string) (release func(), status int) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	switch {
	case ls.closed:
		return nil, http.StatusServiceUnavailable
	case ls.total >= ls.limits.Total || ls.perUser[user] >= ls.limits.PerUser:
		return nil, http.StatusTooManyRequests
	}
	ls.total++
	ls.perUser[user]++
	ls.gauge.Inc()
	var once sync.Once
	return func() {
		once.Do(func() {
			ls.mu.Lock()
			ls.total--
			if ls.perUser[user]--; ls.perUser[user] <= 0 {
				delete(ls.perUser, user)
			}
			ls.mu.Unlock()
			ls.gauge.Dec()
		})
	}, 0
}

func (ls *liveStreams) close() {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if !ls.closed {
		ls.closed = true
		close(ls.done)
	}
}

// CloseStreams ends every open live stream (each says bye "shutdown") and refuses new ones.
// Register it with http.Server.RegisterOnShutdown: Shutdown waits for active requests, and a
// stream would otherwise keep it waiting until its lifetime ends.
func (s *Server) CloseStreams() { s.streams.close() }

// Reasons sent with resync and bye (LiveNotice).
const (
	liveBehind      = "behind"
	liveSignedOut   = "signed_out"
	liveNoAccess    = "no_access"
	liveUnavailable = "unavailable"
	liveLifetime    = "lifetime"
	liveShutdown    = "shutdown"
)

// errStreamEnd ends a stream after its bye was sent.
var errStreamEnd = errors.New("live stream ended")

// GetLiveStream streams an account's live view: a snapshot on connect and every few seconds,
// the account's events and order changes from the bus, pings, and bye when it ends.
func (s *Server) GetLiveStream(w http.ResponseWriter, r *http.Request, params GetLiveStreamParams) {
	ctx := r.Context()
	lim := s.cfg.Live
	p := state(r).principal
	if p.UserID == "" {
		s.fail(w, r, domain.ErrUnauthenticated)
		return
	}
	release, code := s.streams.acquire(p.UserID)
	switch code {
	case http.StatusTooManyRequests:
		w.Header().Set("Retry-After", "30")
		writeProblem(w, r, code, "Too many live streams", fmt.Sprintf(
			"at most %d live streams per user and %d per server: close another tab and try again", lim.PerUser, lim.Total), "")
		return
	case http.StatusServiceUnavailable:
		writeProblem(w, r, code, "Unavailable", "the server is shutting down", "")
		return
	}
	defer release()

	// Subscribed before the first snapshot: nothing that happens after it is missed.
	events, unsubscribe, _ := s.svc.SubscribeLive(lim.Buffer)
	defer unsubscribe()
	// A recent portfolio for accounts the supervisor does not read (shared, at most one
	// eToro read per account every 10 s). Without one the snapshot is marked stale.
	wctx, cancel := context.WithTimeout(ctx, lim.Warm)
	if err := s.svc.WarmLiveView(wctx, p, accountParam(params.AccountId)); err != nil {
		s.log.DebugContext(ctx, "live stream: no fresh portfolio on connect", "err", err)
	}
	cancel()
	snap, err := s.svc.LiveSnapshot(ctx, p, accountParam(params.AccountId))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	filter, err := s.svc.LiveFilter(ctx, p)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	st := &liveStream{
		s: s, w: w, r: r, rc: http.NewResponseController(w), p: p, account: snap.Account.ID,
		filter: filter, symbols: map[int64]string{},
	}
	// The server's ReadTimeout would end the stream (HTTP/1.1 cancels the request when its
	// background read times out): no read deadline from here on.
	if err := st.rc.SetReadDeadline(time.Time{}); err != nil {
		s.log.WarnContext(ctx, "live stream: cannot clear the read deadline; the server's timeout will end it", "err", err)
	}
	if err := st.sendSnapshot(snap); err != nil {
		return
	}
	st.loop(ctx, events)
}

// liveStream is one open stream.
type liveStream struct {
	s       *Server
	w       http.ResponseWriter
	r       *http.Request
	rc      *http.ResponseController
	p       app.Principal
	account string
	filter  map[string]bool
	// symbols are the instrument symbols of the latest snapshot, for order messages.
	symbols  map[int64]string
	lastSnap time.Time
}

func (st *liveStream) loop(ctx context.Context, events <-chan ports.Event) {
	lim := st.s.cfg.Live
	snapT, pingT, recheckT := time.NewTicker(lim.Snapshot), time.NewTicker(lim.Ping), time.NewTicker(lim.Recheck)
	defer snapT.Stop()
	defer pingT.Stop()
	defer recheckT.Stop()
	life := time.NewTimer(lim.Lifetime)
	defer life.Stop()
	var soon <-chan time.Time // a snapshot asked for by a change, after liveSnapshotGap
	for {
		var err error
		select {
		case <-ctx.Done():
			return
		case <-st.s.streams.done:
			err = st.bye(liveShutdown)
		case <-life.C:
			err = st.bye(liveLifetime)
		case <-recheckT.C:
			err = st.recheck(ctx)
		case <-pingT.C:
			err = st.write([]byte(": ping\n\n"))
		case <-snapT.C:
			err = st.snapshot(ctx)
		case <-soon:
			soon = nil
			err = st.snapshot(ctx)
		case e, open := <-events: // nil without a bus: never ready
			if !open {
				return
			}
			// Full when it was received: a publish may have been dropped for this stream.
			behind := len(events) >= cap(events)-1
			var change bool
			if change, err = st.forward(e); err == nil && behind {
				err = st.resync(ctx, events)
				change = false
			}
			if err == nil && change && soon == nil {
				if wait := liveSnapshotGap - time.Since(st.lastSnap); wait > 0 {
					soon = time.After(wait)
				} else {
					err = st.snapshot(ctx)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// forward sends a bus event the caller may see. change: the agent's state or the portfolio
// of the stream's account changed (a snapshot is due).
func (st *liveStream) forward(e ports.Event) (change bool, err error) {
	acct := app.BusAccount(e)
	mine := acct != "" && acct == st.account && st.filter[acct]
	switch d := e.Data.(type) {
	case domain.Event:
		if acct != "" && !mine { // system-wide events are in everyone's feed
			return false, nil
		}
		return false, st.send("event", LiveEvent{AccountId: acct, Event: toEvent(d)})
	case domain.Order:
		if !mine {
			return false, nil
		}
		return false, st.send("order", LiveOrder{AccountId: acct, Order: toOrder(app.OrderView{Order: d, Symbol: st.symbols[int64(d.Instrument)]})})
	case app.AgentStatus, app.PortfolioRead:
		return mine, nil
	}
	return false, nil
}

// resync drops what is buffered (the client fetches again anyway), says so, and sends a
// fresh snapshot.
func (st *liveStream) resync(ctx context.Context, events <-chan ports.Event) error {
	for n := len(events); n > 0; n-- {
		if _, open := <-events; !open {
			return errStreamEnd
		}
	}
	if err := st.send("resync", LiveNotice{Reason: liveBehind}); err != nil {
		return err
	}
	return st.snapshot(ctx)
}

// snapshot sends the account's live view (cached and stored data only). Lost access ends
// the stream; any other failure skips this snapshot (the client shows it as stale).
func (st *liveStream) snapshot(ctx context.Context) error {
	snap, err := st.s.svc.LiveSnapshot(ctx, st.p, st.account)
	switch {
	case err == nil:
		return st.sendSnapshot(snap)
	case accessLost(err):
		return st.bye(liveNoAccess)
	case ctx.Err() != nil:
		return ctx.Err()
	}
	st.s.log.WarnContext(ctx, "live stream: snapshot failed", "account", st.account, "err", err)
	return nil
}

func (st *liveStream) sendSnapshot(snap app.LiveSnapshot) error {
	st.lastSnap = time.Now()
	out := toLiveSnapshot(snap)
	clear(st.symbols)
	for _, o := range snap.OpenOrders {
		st.symbols[int64(o.Instrument)] = o.Symbol
	}
	for _, o := range snap.RecentOrders {
		st.symbols[int64(o.Instrument)] = o.Symbol
	}
	for _, p := range snap.Positions {
		st.symbols[int64(p.Instrument)] = p.Symbol
	}
	for _, rg := range snap.Regimes {
		st.symbols[int64(rg.Instrument)] = rg.Symbol
	}
	return st.send("snapshot", out)
}

// recheck checks the caller's session or API key and the access to the account again:
// signed out, revoked, password changed or the share removed ends the stream.
func (st *liveStream) recheck(ctx context.Context) error {
	p, err := st.s.reauthenticate(ctx, st.r)
	switch {
	case errors.Is(err, domain.ErrUnauthenticated):
		return st.bye(liveSignedOut)
	case err != nil:
		st.s.log.WarnContext(ctx, "live stream: the session check failed", "err", err)
		return st.bye(liveUnavailable)
	}
	filter, err := st.s.svc.LiveFilter(ctx, p)
	switch {
	case err != nil && accessLost(err):
		return st.bye(liveNoAccess)
	case err != nil:
		st.s.log.WarnContext(ctx, "live stream: the access check failed", "err", err)
		return st.bye(liveUnavailable)
	case !filter[st.account]:
		return st.bye(liveNoAccess)
	}
	st.p, st.filter = p, filter
	return nil
}

// reauthenticate resolves the request's API key or session cookie again, as the
// authenticate middleware did when the stream opened.
func (s *Server) reauthenticate(ctx context.Context, r *http.Request) (app.Principal, error) {
	ip := state(r).ip
	if authz := r.Header.Get("Authorization"); authz != "" {
		bearer, ok := strings.CutPrefix(authz, "Bearer ")
		if !ok {
			return app.Principal{}, domain.ErrUnauthenticated
		}
		return s.svc.APIKeyPrincipal(ctx, strings.TrimSpace(bearer), ip)
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return app.Principal{}, domain.ErrUnauthenticated
	}
	p, _, err := s.svc.SessionPrincipal(ctx, c.Value, ip)
	return p, err
}

// accessLost: the error says the caller may no longer view the account.
func accessLost(err error) bool {
	for _, e := range []error{
		domain.ErrUnauthenticated, domain.ErrForbidden, domain.ErrNotFound, domain.ErrViewOnly,
		domain.ErrPasswordChangeRequired, domain.ErrNoAccount,
	} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func (st *liveStream) bye(reason string) error {
	if err := st.send("bye", LiveNotice{Reason: reason}); err != nil {
		return err
	}
	return errStreamEnd
}

func (st *liveStream) send(event string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	msg := make([]byte, 0, len(b)+len(event)+16)
	msg = append(msg, "event: "...)
	msg = append(msg, event...)
	msg = append(msg, "\ndata: "...)
	msg = append(msg, b...)
	msg = append(msg, "\n\n"...)
	return st.write(msg)
}

// write sends b at once. The write deadline is moved before it (the server's WriteTimeout
// would otherwise end the stream) and cleared after it: no write is pending until the next
// one, and on HTTP/2 a deadline that passes resets even an idle stream.
func (st *liveStream) write(b []byte) error {
	if err := st.rc.SetWriteDeadline(time.Now().Add(st.s.cfg.Live.WriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if _, err := st.w.Write(b); err != nil {
		return err
	}
	if err := st.rc.Flush(); err != nil {
		return err
	}
	if err := st.rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

func toLiveSnapshot(v app.LiveSnapshot) LiveSnapshot {
	out := LiveSnapshot{
		Account: toAccount(v.Account), At: v.At, PortfolioKnown: v.PortfolioKnown, Stale: v.Stale,
		Positions: make([]LivePosition, 0, len(v.Positions)), OpenOrders: make([]Order, 0, len(v.OpenOrders)),
		RecentOrders: make([]Order, 0, len(v.RecentOrders)), Regimes: make([]LiveRegime, 0, len(v.Regimes)),
		State: AgentState(v.State), Reconciliation: toReconciliation(v.Reconciliation), Killed: v.Killed,
		DayStopped: v.DayStopped, Lease: AgentLease{Mine: v.Lease.Mine, Other: v.Lease.Other},
	}
	if v.PortfolioKnown {
		out.PortfolioAt = ptr(v.PortfolioAt)
		out.Figures = ptr(toLiveFigures(v.Figures))
	}
	if v.KillReason != "" {
		out.KillReason = ptr(v.KillReason)
	}
	if !v.Lease.ExpiresAt.IsZero() {
		out.Lease.ExpiresAt = ptr(v.Lease.ExpiresAt)
	}
	for _, p := range v.Positions {
		x := LivePosition{
			PositionId: p.ID, InstrumentId: int64(p.Instrument), Symbol: p.Symbol, Market: Market(p.Market),
			OpenedAt: p.OpenedAt, OpenRate: dec(p.OpenRate), Units: dec(p.Units), AmountCents: Cents(p.Amount),
			UnrealisedPnlCents: Cents(p.UnrealisedPnL), StopLossRate: optDec(p.StopLossRate), TakeProfitRate: optDec(p.TakeProfitRate),
			RealAsset: p.RealAsset, Long: p.Long, Leverage: p.Leverage, Ours: p.Ours,
		}
		if p.OpenRequestID != "" {
			x.OpenRequestId = ptr(p.OpenRequestID)
		}
		if !p.FlattenAt.IsZero() {
			x.FlattenAt = ptr(p.FlattenAt)
		}
		out.Positions = append(out.Positions, x)
	}
	for _, o := range v.OpenOrders {
		out.OpenOrders = append(out.OpenOrders, toOrder(o))
	}
	for _, o := range v.RecentOrders {
		out.RecentOrders = append(out.RecentOrders, toOrder(o))
	}
	for _, rg := range v.Regimes {
		x := LiveRegime{InstrumentId: int64(rg.Instrument), Symbol: rg.Symbol, Known: rg.Known}
		if rg.Known {
			probs := make(map[string]float64, len(rg.Probs))
			for k, p := range rg.Probs {
				probs[string(k)] = p
			}
			x.Regime, x.Probabilities, x.At = ptr(string(rg.Regime)), &probs, ptr(rg.At)
		}
		out.Regimes = append(out.Regimes, x)
	}
	return out
}

func toLiveFigures(f domain.LiveFigures) LiveFigures {
	out := LiveFigures{
		AsOf: f.AsOf, EquityCents: Cents(f.Equity), CashCents: Cents(f.Cash), ReservedCents: Cents(f.Reserved),
		CashLeftCents: Cents(f.CashLeft), BudgetCents: Cents(f.Budget), BudgetUsedCents: Cents(f.BudgetUsed),
		BudgetLeftCents: Cents(f.BudgetLeft), DayKnown: f.DayKnown, Day: string(f.Day), StartOfDayCents: Cents(f.StartOfDay),
		DayPnlCents: Cents(f.DayPnL), DailyFloorCents: Cents(f.DailyFloor), ToDailyFloorCents: Cents(f.ToDailyFloor),
		DayStopped: f.DayStopped, HighWaterMarkCents: Cents(f.HighWaterMark), DrawdownFloorCents: Cents(f.DrawdownFloor),
		ToDrawdownFloorCents: Cents(f.ToDrawdownFloor), Killed: f.Killed, Open: f.Open, Cap: f.Cap, SlotsLeft: f.SlotsLeft,
		ShortUsedCents: Cents(f.ShortUsed), ShortCapCents: Cents(f.ShortCap), ShortLeftCents: Cents(f.ShortLeft),
		Markets: make([]LiveMarketClock, 0, len(f.Markets)),
	}
	for _, m := range f.Markets {
		x := LiveMarketClock{
			Market: Market(m.Market), Trading: m.Trading,
			ToNoEntrySeconds: int64(m.ToNoEntry / time.Second), ToFlattenSeconds: int64(m.ToFlatten / time.Second),
		}
		if m.Trading {
			x.Opens, x.Closes, x.NoEntryAt, x.FlattenAt = ptr(m.Open), ptr(m.Close), ptr(m.NoEntryAt), ptr(m.FlattenAt)
		}
		out.Markets = append(out.Markets, x)
	}
	return out
}
