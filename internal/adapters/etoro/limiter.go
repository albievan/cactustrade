package etoro

import (
	"context"
	"sync"
	"time"
)

// window is eToro's rate-limit window: a rolling 60 seconds per user key.
const window = 60 * time.Second

// limiter keeps each quota group under its documented limit with a sliding window, and
// pauses a group entirely after a 429 until eToro's Retry-After has passed.
type limiter struct {
	mu      sync.Mutex
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error
	sent    map[QuotaGroup][]time.Time
	blocked map[QuotaGroup]time.Time
}

func newLimiter(now func() time.Time, sleep func(context.Context, time.Duration) error) *limiter {
	return &limiter{now: now, sleep: sleep, sent: map[QuotaGroup][]time.Time{}, blocked: map[QuotaGroup]time.Time{}}
}

// Wait blocks until group g may send one more request (or ctx ends), then records it.
func (l *limiter) Wait(ctx context.Context, g QuotaGroup) error {
	limit := QuotaLimits[g]
	if limit <= 0 {
		limit = 1 // unknown group: as slow as possible rather than unlimited
	}
	for {
		l.mu.Lock()
		now := l.now()
		ts := l.sent[g]
		for len(ts) > 0 && now.Sub(ts[0]) >= window {
			ts = ts[1:]
		}
		wait := time.Duration(0)
		if b := l.blocked[g]; now.Before(b) {
			wait = b.Sub(now)
		}
		if len(ts) >= limit {
			wait = max(wait, ts[0].Add(window).Sub(now))
		}
		if wait <= 0 {
			l.sent[g] = append(ts, now)
			l.mu.Unlock()
			return nil
		}
		l.sent[g] = ts
		l.mu.Unlock()
		if err := l.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// Block pauses group g until at least until (after a 429).
func (l *limiter) Block(g QuotaGroup, until time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until.After(l.blocked[g]) {
		l.blocked[g] = until
	}
}

// sleepCtx sleeps for d or until ctx ends.
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
