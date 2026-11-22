package httpapi

import (
	"sync"
	"time"
)

// loginLimiter counts failed sign-ins (and setup attempts) per client address and per
// username in a sliding window. Over the limit, requests are refused with 429 before the
// password is even checked.
type loginLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	perIP   int
	perUser int
	fails   map[string][]time.Time
	now     func() time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		window: 15 * time.Minute, perIP: 20, perUser: 5,
		fails: map[string][]time.Time{}, now: time.Now,
	}
}

func (l *loginLimiter) recent(key string, now time.Time) []time.Time {
	ts := l.fails[key]
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= l.window {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = ts
	}
	return ts
}

// Allow reports whether a sign-in from ip for user may be tried, and if not, when.
func (l *loginLimiter) Allow(ip, user string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, c := range []struct {
		key   string
		limit int
	}{{"ip:" + ip, l.perIP}, {"user:" + user, l.perUser}} {
		if c.key == "user:" {
			continue
		}
		if ts := l.recent(c.key, now); len(ts) >= c.limit {
			return false, l.window - now.Sub(ts[0])
		}
	}
	return true, 0
}

// Fail records a failed attempt.
func (l *loginLimiter) Fail(ip, user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.fails["ip:"+ip] = append(l.recent("ip:"+ip, now), now)
	if user != "" {
		l.fails["user:"+user] = append(l.recent("user:"+user, now), now)
	}
}

// Success clears the username's counter (not the address's).
func (l *loginLimiter) Success(user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, "user:"+user)
}
