package trading212

import (
	"sync"
	"time"
)

// limiter spaces the requests of one account per endpoint to the documented rate (one per
// Endpoint.Every) and holds an endpoint back after a 429 until Trading 212's reset time.
type limiter struct {
	mu   sync.Mutex
	now  func() time.Time
	next map[Op]time.Time
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, next: map[Op]time.Time{}}
}

// Reserve books the next slot for op and returns how long to wait for it. When that wait
// would exceed maxWait nothing is booked and ok is false.
func (l *limiter) Reserve(op Op, every, maxWait time.Duration) (wait time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	at := now
	if n := l.next[op]; n.After(at) {
		at = n
	}
	if at.Sub(now) > maxWait {
		return at.Sub(now), false
	}
	l.next[op] = at.Add(every)
	return at.Sub(now), true
}

// Block holds op back until at least until.
func (l *limiter) Block(op Op, until time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until.After(l.next[op]) {
		l.next[op] = until
	}
}
