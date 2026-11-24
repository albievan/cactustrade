package httpapi

import (
	"context"
	"sync"
	"time"
)

// bucket is one sliding-window limit: at most limit hits per window.
type bucket struct {
	prefix string
	window time.Duration
	limit  int
}

// The limiter's buckets (PLAN 7.1.5). Nothing here locks a user out everywhere: the hard
// limits are per address, or per (user, address); across addresses a user's failures only
// slow further attempts down.
var (
	// signInIP: sign-in attempts (and current-password checks) per address.
	signInIP = bucket{"ip:", 15 * time.Minute, 20}
	// signInPair: attempts for one user from one address.
	signInPair = bucket{"pair:", 15 * time.Minute, 5}
	// signInUser: attempts for one user from anywhere; never refuses, only slows down from
	// slowAfter on (slowDelay).
	signInUser = bucket{"user:", 15 * time.Minute, 0}
	// resetIP and resetUser: password-reset requests per address and per user name (the
	// app also sends at most domain.PasswordResetPerHour mails per user and hour).
	resetIP   = bucket{"reset-ip:", 15 * time.Minute, 20}
	resetUser = bucket{"reset-user:", time.Hour, 5}
	// confirmIP: bad password-reset tokens per address.
	confirmIP = bucket{"confirm-ip:", 15 * time.Minute, 10}
	// oauthIP: forged or replayed OAuth states (Saxo sign-in callbacks) per address.
	oauthIP = bucket{"oauth-ip:", 15 * time.Minute, 5}
)

// slowAfter is the number of recent attempts for a user (from any address) after which each
// further attempt waits slowDelay first.
const slowAfter = 5

// slowDelay is the wait before a sign-in for a user with n recent attempts: none below
// slowAfter, then 1 s, 2 s, 4 s, capped at 8 s.
func slowDelay(n int) time.Duration {
	if n < slowAfter {
		return 0
	}
	return min(time.Second<<min(n-slowAfter, 3), 8*time.Second)
}

// maxLimiterKeys bounds the limiter's memory: when a new key would exceed it, stale keys are
// swept and, if that is not enough, the key idle the longest is dropped.
const maxLimiterKeys = 10_000

type hits struct {
	window time.Duration
	ts     []time.Time
}

// loginLimiter limits sign-ins, current-password checks and password resets. Attempts are
// counted before the password is hashed (refunded when it was right), so concurrent requests
// cannot all slip past the limit.
type loginLimiter struct {
	mu    sync.Mutex
	keys  map[string]*hits
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	max   int
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{keys: map[string]*hits{}, now: time.Now, sleep: sleepCtx, max: maxLimiterKeys}
}

// sleepCtx waits for d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// recent returns the hits of key inside the window, dropping older ones (and the key when
// none is left). Callers hold mu.
func (l *loginLimiter) recent(key string, now time.Time) []time.Time {
	h := l.keys[key]
	if h == nil {
		return nil
	}
	i := 0
	for i < len(h.ts) && now.Sub(h.ts[i]) >= h.window {
		i++
	}
	h.ts = h.ts[i:]
	if len(h.ts) == 0 {
		delete(l.keys, key)
		return nil
	}
	return h.ts
}

// over reports whether b's limit for id is reached, and when it frees up.
func (l *loginLimiter) over(b bucket, id string, now time.Time) (bool, time.Duration) {
	ts := l.recent(b.prefix+id, now)
	if b.limit > 0 && len(ts) >= b.limit {
		return true, b.window - now.Sub(ts[0])
	}
	return false, 0
}

// add records a hit for id in b. Callers hold mu.
func (l *loginLimiter) add(b bucket, id string, now time.Time) {
	key := b.prefix + id
	h := l.keys[key]
	if h == nil {
		if len(l.keys) >= l.max {
			l.sweep(now)
		}
		if len(l.keys) >= l.max {
			l.evict()
		}
		h = &hits{window: b.window}
		l.keys[key] = h
	}
	h.ts = append(h.ts, now)
}

// refund removes the latest hit for id in b. Callers hold mu.
func (l *loginLimiter) refund(b bucket, id string) {
	key := b.prefix + id
	if h := l.keys[key]; h != nil && len(h.ts) > 0 {
		h.ts = h.ts[:len(h.ts)-1]
		if len(h.ts) == 0 {
			delete(l.keys, key)
		}
	}
}

func pair(ip, user string) string { return user + "\x00" + ip }

// SignIn records a sign-in attempt (or current-password check) for user from ip before the
// password is checked. ok=false: refused, retry after wait. Otherwise delay is how long to
// wait before checking (the per-user slow-down).
func (l *loginLimiter) SignIn(ip, user string) (ok bool, wait, delay time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, c := range []struct {
		b  bucket
		id string
	}{{signInIP, ip}, {signInPair, pair(ip, user)}} {
		if over, w := l.over(c.b, c.id, now); over {
			return false, w, 0
		}
	}
	delay = slowDelay(len(l.recent(signInUser.prefix+user, now)))
	l.add(signInIP, ip, now)
	l.add(signInPair, pair(ip, user), now)
	l.add(signInUser, user, now)
	return true, 0, delay
}

// Refund takes back an attempt that was not a wrong password (the server was busy, or the
// store failed).
func (l *loginLimiter) Refund(ip, user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refund(signInIP, ip)
	l.refund(signInPair, pair(ip, user))
	l.refund(signInUser, user)
}

// Success takes back a right attempt and clears the user's failures from that address.
func (l *loginLimiter) Success(ip, user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refund(signInIP, ip)
	l.refund(signInUser, user)
	delete(l.keys, signInPair.prefix+pair(ip, user))
}

// ResetRequest records a password-reset request for user from ip; ok=false: refused, retry
// after wait. Reset requests have their own buckets: they never count as sign-in failures.
func (l *loginLimiter) ResetRequest(ip, user string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if over, w := l.over(resetIP, ip, now); over {
		return false, w
	}
	if over, w := l.over(resetUser, user, now); over {
		return false, w
	}
	l.add(resetIP, ip, now)
	l.add(resetUser, user, now)
	return true, 0
}

// Allowed reports whether b's limit for id leaves room for one more try, and if not, when.
func (l *loginLimiter) Allowed(b bucket, id string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	over, w := l.over(b, id, l.now())
	return !over, w
}

// Failed records a failed try for id in b (a bad reset token, a forged OAuth state).
func (l *loginLimiter) Failed(b bucket, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.add(b, id, l.now())
}

// Sweep drops every key without a hit inside its window (PurgeLoop calls it).
func (l *loginLimiter) Sweep() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sweep(l.now())
}

func (l *loginLimiter) sweep(now time.Time) int {
	n := 0
	for k := range l.keys {
		if l.recent(k, now) == nil {
			n++
		}
	}
	return n
}

// evict drops the key whose latest hit is the oldest (only when the map is full of live
// keys). Callers hold mu.
func (l *loginLimiter) evict() {
	var oldest string
	var at time.Time
	for k, h := range l.keys {
		if last := h.ts[len(h.ts)-1]; oldest == "" || last.Before(at) {
			oldest, at = k, last
		}
	}
	delete(l.keys, oldest)
}

// Len is the number of keys held.
func (l *loginLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.keys)
}
