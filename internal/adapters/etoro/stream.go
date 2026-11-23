package etoro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/albievan/cactustrade/internal/domain"
)

// Stream keeps one WebSocket to eToro (WebSocketURL), authenticated with the current mode's
// keys and subscribed to instrument:<id> for every watched instrument, and caches the latest
// bid/ask. It reconnects with backoff, pings to detect a dead connection, and reconnects
// when the trading mode changes. It only ever subscribes to market-data topics.
//
// Message formats (verified 2026-11-23, api-portal.etoro.com/core/websocket/*):
// requests {id, operation: Authenticate|Subscribe|Unsubscribe, data}; Authenticate data
// {userKey, apiKey}; Subscribe data {topics, snapshot}; replies {id, operation, success,
// errorCode, errorMessage}; pushes {messages:[{topic, content, id, type}]} where content is
// a JSON string with Ask, Bid, LastExecution, Date (numbers sent as strings).
type Stream struct {
	c    *Client
	url  string
	dial func(ctx context.Context, url string) (wsConn, error)
	// PingEvery and ReconnectMax tune the connection (tests shorten them).
	PingEvery    time.Duration
	ReconnectMax time.Duration

	mu        sync.Mutex
	want      map[domain.InstrumentID]bool
	quotes    map[domain.InstrumentID]streamQuote
	connected bool
	lastErr   string
	changed   chan struct{}
}

type streamQuote struct {
	q        domain.Quote
	received time.Time
}

// wsConn is the part of a WebSocket the stream uses.
type wsConn interface {
	Read(ctx context.Context) (text bool, data []byte, err error)
	Write(ctx context.Context, data []byte) error
	Ping(ctx context.Context) error
	Close() error
}

// NewStream builds a stream on the client's keys, mode, clock and log.
func NewStream(c *Client) *Stream {
	return &Stream{
		c: c, url: WebSocketURL, dial: dialWS, PingEvery: 30 * time.Second, ReconnectMax: time.Minute,
		want: map[domain.InstrumentID]bool{}, quotes: map[domain.InstrumentID]streamQuote{},
		changed: make(chan struct{}, 1),
	}
}

// Watch adds instruments to the subscription set (they stay until restart).
func (s *Stream) Watch(ids ...domain.InstrumentID) {
	s.mu.Lock()
	added := false
	for _, id := range ids {
		if id > 0 && !s.want[id] {
			s.want[id], added = true, true
		}
	}
	s.mu.Unlock()
	if added {
		select {
		case s.changed <- struct{}{}:
		default:
		}
	}
}

// Latest returns the cached quote for id and when it arrived.
func (s *Stream) Latest(id domain.InstrumentID) (domain.Quote, time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sq, ok := s.quotes[id]
	return sq.q, sq.received, ok
}

// Status reports whether the stream is connected and the last connection error.
func (s *Stream) Status() (connected bool, lastErr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected, s.lastErr
}

func (s *Stream) setConnected(ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = ok
	if err != nil {
		s.lastErr = err.Error()
	} else if ok {
		s.lastErr = ""
	}
}

// Run keeps the stream up until ctx ends.
func (s *Stream) Run(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		start := s.c.cfg.Now()
		err := s.session(ctx)
		s.setConnected(false, err)
		if ctx.Err() != nil {
			return
		}
		if s.c.cfg.Now().Sub(start) > 5*time.Minute {
			failures = 0 // a long healthy session resets the backoff
		}
		failures++
		wait := min(time.Duration(1<<min(failures-1, 6))*time.Second, s.ReconnectMax)
		if errors.Is(err, domain.ErrCredentialsMissing) {
			wait = s.ReconnectMax // nothing to do until the keys are set
		} else {
			s.c.cfg.Log.WarnContext(ctx, "eToro stream disconnected", "err", err, "retry_in_s", wait.Seconds())
		}
		if s.c.cfg.Sleep(ctx, wait) != nil {
			return
		}
	}
}

type wsRequest struct {
	ID        string `json:"id"`
	Operation string `json:"operation"`
	Data      any    `json:"data"`
}

type wsReply struct {
	ID           string          `json:"id"`
	Operation    string          `json:"operation"`
	Success      *bool           `json:"success"`
	ErrorCode    string          `json:"errorCode"`
	ErrorMessage string          `json:"errorMessage"`
	Messages     []wsPushMessage `json:"messages"`
}

type wsPushMessage struct {
	Topic   string `json:"topic"`
	Content string `json:"content"`
	Type    string `json:"type"`
}

// session runs one connection: dial, authenticate, subscribe, then read until it fails.
func (s *Stream) session(ctx context.Context) error {
	mode := s.c.cfg.Mode(ctx)
	apiKey, userKey, err := s.c.keys(ctx, mode)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, err := s.dial(ctx, s.url)
	if err != nil {
		return fmt.Errorf("eToro stream: dial: %w", err)
	}
	defer conn.Close()

	if err := s.send(ctx, conn, "Authenticate", map[string]string{"userKey": userKey, "apiKey": apiKey}); err != nil {
		return err
	}
	for {
		r, err := readReply(ctx, conn)
		if err != nil {
			return err
		}
		if r.Operation == "Authenticate" && r.Success != nil {
			if !*r.Success {
				return fmt.Errorf("eToro stream: authentication refused: %s %s: %w", r.ErrorCode, r.ErrorMessage, domain.ErrUnavailable)
			}
			break
		}
	}
	s.setConnected(true, nil)
	s.c.cfg.Log.InfoContext(ctx, "eToro stream connected", "mode", string(mode))

	errc := make(chan error, 2)
	go func() { errc <- s.writer(ctx, conn, mode) }()
	go func() { errc <- s.reader(ctx, conn) }()
	err = <-errc
	cancel()
	return err
}

// writer subscribes new instruments, pings, and ends the session when the mode changes.
func (s *Stream) writer(ctx context.Context, conn wsConn, mode domain.TradingMode) error {
	subscribed := map[domain.InstrumentID]bool{}
	ping := time.NewTicker(s.PingEvery)
	defer ping.Stop()
	subscribeNew := func() error {
		s.mu.Lock()
		var add []domain.InstrumentID
		for id := range s.want {
			if !subscribed[id] {
				add = append(add, id)
			}
		}
		s.mu.Unlock()
		if len(add) == 0 {
			return nil
		}
		slices.Sort(add)
		topics := make([]string, len(add))
		for i, id := range add {
			topics[i] = "instrument:" + strconv.FormatInt(int64(id), 10)
		}
		if err := s.send(ctx, conn, "Subscribe", map[string]any{"topics": topics, "snapshot": true}); err != nil {
			return err
		}
		for _, id := range add {
			subscribed[id] = true
		}
		return nil
	}
	if err := subscribeNew(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.changed:
			if err := subscribeNew(); err != nil {
				return err
			}
		case <-ping.C:
			if m := s.c.cfg.Mode(ctx); m != mode {
				return fmt.Errorf("eToro stream: trading mode changed to %s; reconnecting", m)
			}
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return fmt.Errorf("eToro stream: ping: %w", err)
			}
		}
	}
}

// reader applies pushed rates and logs refused requests.
func (s *Stream) reader(ctx context.Context, conn wsConn) error {
	for {
		r, err := readReply(ctx, conn)
		if err != nil {
			return err
		}
		if r.Success != nil && !*r.Success {
			s.c.cfg.Log.WarnContext(ctx, "eToro stream refused a request", "operation", r.Operation, "code", r.ErrorCode, "message", r.ErrorMessage)
		}
		for _, m := range r.Messages {
			if q, ok := parseRate(m); ok {
				s.mu.Lock()
				s.quotes[q.Instrument] = streamQuote{q: q, received: s.c.cfg.Now()}
				s.mu.Unlock()
			}
		}
	}
}

func (s *Stream) send(ctx context.Context, conn wsConn, op string, data any) error {
	b, err := json.Marshal(wsRequest{ID: s.c.cfg.Random.ID(), Operation: op, Data: data})
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := conn.Write(wctx, b); err != nil {
		return fmt.Errorf("eToro stream: %s: %w", op, err)
	}
	return nil
}

// readReply reads the next text frame (binary heartbeat frames are skipped).
func readReply(ctx context.Context, conn wsConn) (wsReply, error) {
	for {
		text, data, err := conn.Read(ctx)
		if err != nil {
			return wsReply{}, fmt.Errorf("eToro stream: read: %w", err)
		}
		if !text {
			continue
		}
		var r wsReply
		if err := json.Unmarshal(data, &r); err != nil {
			continue // not ours to understand; never fatal
		}
		return r, nil
	}
}

// flexNum accepts a JSON number or a number in a string (eToro sends rates as strings).
type flexNum float64

func (f *flexNum) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseFloat(strings.Trim(string(b), `"`), 64)
	if err != nil {
		return err
	}
	*f = flexNum(v)
	return nil
}

// parseRate turns an instrument:<id> push into a quote.
func parseRate(m wsPushMessage) (domain.Quote, bool) {
	idText, ok := strings.CutPrefix(m.Topic, "instrument:")
	if !ok {
		return domain.Quote{}, false
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		return domain.Quote{}, false
	}
	var c struct {
		Ask  *flexNum   `json:"Ask"`
		Bid  *flexNum   `json:"Bid"`
		Date *time.Time `json:"Date"`
	}
	if json.Unmarshal([]byte(m.Content), &c) != nil || c.Ask == nil || c.Bid == nil || c.Date == nil || *c.Ask <= 0 || *c.Bid <= 0 {
		return domain.Quote{}, false
	}
	return domain.Quote{
		Instrument: domain.InstrumentID(id), Bid: toMicros(float64(*c.Bid)), Ask: toMicros(float64(*c.Ask)), At: c.Date.UTC(),
	}, true
}

// --- the real WebSocket (coder/websocket) ------------------------------------------------

type coderConn struct{ c *websocket.Conn }

func dialWS(ctx context.Context, url string) (wsConn, error) {
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(dctx, url, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(1 << 20)
	return coderConn{c}, nil
}

func (w coderConn) Read(ctx context.Context) (bool, []byte, error) {
	t, b, err := w.c.Read(ctx)
	return t == websocket.MessageText, b, err
}

func (w coderConn) Write(ctx context.Context, b []byte) error {
	return w.c.Write(ctx, websocket.MessageText, b)
}

func (w coderConn) Ping(ctx context.Context) error { return w.c.Ping(ctx) }
func (w coderConn) Close() error                   { return w.c.Close(websocket.StatusNormalClosure, "") }

// --- Feed: streamed quotes with a REST fallback -----------------------------------------

// Feed is the MarketData port: quotes come from the stream when fresh (received within
// MaxAge), otherwise from one REST rates call for the rest (within the market-data quota).
// Every instrument asked for is added to the stream.
type Feed struct {
	*Client
	Stream *Stream
	MaxAge time.Duration
}

// NewFeed combines the client and its stream; quotes older than maxAge go to REST.
func NewFeed(c *Client, s *Stream, maxAge time.Duration) *Feed {
	return &Feed{Client: c, Stream: s, MaxAge: maxAge}
}

// Quotes returns fresh streamed quotes and fetches the others over REST.
func (f *Feed) Quotes(ctx context.Context, ids []domain.InstrumentID) ([]domain.Quote, error) {
	f.Stream.Watch(ids...)
	now := f.cfg.Now()
	got := map[domain.InstrumentID]domain.Quote{}
	var missing []domain.InstrumentID
	for _, id := range ids {
		if q, at, ok := f.Stream.Latest(id); ok && now.Sub(at) <= f.MaxAge {
			got[id] = q
		} else {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		qs, err := f.Client.Quotes(ctx, missing)
		if err != nil {
			if len(got) == 0 {
				return nil, err
			}
			f.cfg.Log.WarnContext(ctx, "REST quote fallback failed; returning streamed quotes only", "err", err)
		}
		for _, q := range qs {
			if slices.Contains(missing, q.Instrument) { // never overwrite a fresher streamed quote
				got[q.Instrument] = q
			}
		}
	}
	out := make([]domain.Quote, 0, len(got))
	for _, id := range slices.Sorted(maps.Keys(got)) {
		out = append(out, got[id])
	}
	return out, nil
}
