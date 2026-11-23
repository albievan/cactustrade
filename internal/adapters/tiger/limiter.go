package tiger

import (
	"sync"
	"time"
)

// limiter spaces the calls of one account per method and holds a method back after a rate
// limit answer until it may be tried again.
type limiter struct {
	mu   sync.Mutex
	now  func() time.Time
	next map[Method]time.Time
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, next: map[Method]time.Time{}}
}

// Reserve books the next slot for m and returns how long to wait for it. When that wait would
// exceed maxWait nothing is booked and ok is false.
func (l *limiter) Reserve(m Method, every, maxWait time.Duration) (wait time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	at := now
	if n := l.next[m]; n.After(at) {
		at = n
	}
	if at.Sub(now) > maxWait {
		return at.Sub(now), false
	}
	l.next[m] = at.Add(every)
	return at.Sub(now), true
}

// Block holds m back until at least until.
func (l *limiter) Block(m Method, until time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until.After(l.next[m]) {
		l.next[m] = until
	}
}
