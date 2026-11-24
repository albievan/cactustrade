package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// rateClass is what a request costs against the API rate limits (PLAN 7.3, OD33).
type rateClass string

// The request classes. classNever is never throttled.
const (
	classNever    rateClass = ""
	classRead     rateClass = "read"
	classMutation rateClass = "mutation"
	classExport   rateClass = "export"
	classStream   rateClass = "stream"
)

// RateLimits are the API rate limits per minute, per principal (user or API key) and per
// address (PLAN 7.3). Zero fields take OD33's defaults.
type RateLimits struct {
	Read, Mutation, Export, Stream int
}

// DefaultRateLimits are OD33's numbers: reads 600/min, mutations 60/min, exports 10/min,
// stream connects 30/min.
var DefaultRateLimits = RateLimits{Read: 600, Mutation: 60, Export: 10, Stream: 30}

func (l RateLimits) withDefaults() RateLimits {
	d := DefaultRateLimits
	if l.Read > 0 {
		d.Read = l.Read
	}
	if l.Mutation > 0 {
		d.Mutation = l.Mutation
	}
	if l.Export > 0 {
		d.Export = l.Export
	}
	if l.Stream > 0 {
		d.Stream = l.Stream
	}
	return d
}

func (l RateLimits) perMinute(c rateClass) int {
	switch c {
	case classRead:
		return l.Read
	case classMutation:
		return l.Mutation
	case classExport:
		return l.Export
	case classStream:
		return l.Stream
	}
	return 0
}

// neverThrottled are the routes that stop trading: the kill switch, pause, stop and (PLAN
// 7.14) disarming real trading. They are never rate limited, whatever every bucket holds
// (CLAUDE.md §2.6: stopping must always work). Matched on the mux's route pattern.
var neverThrottled = map[string]bool{
	"POST /api/v1/agent/kill":                            true,
	"POST /api/v1/agent/pause":                           true,
	"POST /api/v1/agent/stop":                            true,
	"POST /api/v1/agent/force-close":                     true,
	"POST /api/v1/agent/force-close-all":                 true,
	"POST /api/v1/accounts/{accountId}/real-gate/disarm": true,
}

// classify is the class of a request matched to pattern (r.Pattern). Only /api/ routes are
// limited.
func classify(method, path, pattern string) rateClass {
	switch {
	case !strings.HasPrefix(path, "/api/"), neverThrottled[pattern]:
		return classNever
	case pattern == liveRoute:
		return classStream
	case pattern == "GET /api/v1/history/export/{file}":
		return classExport
	case method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions:
		return classRead
	}
	return classMutation
}

// rateLimiter is a set of token buckets (GCRA: one "theoretical arrival time" per key, so a
// key costs one map entry). A bucket of n per minute refills one token every minute/n and
// holds at most n. At most max keys are held: when full, refilled keys are swept and then
// the key closest to full is dropped (it loses at most its debt).
type rateLimiter struct {
	mu     sync.Mutex
	tat    map[string]time.Time
	now    func() time.Time
	max    int
	limits RateLimits
}

func newRateLimiter(l RateLimits) *rateLimiter {
	return &rateLimiter{tat: map[string]time.Time{}, now: time.Now, max: maxLimiterKeys, limits: l.withDefaults()}
}

// Allow takes one token of class c from every one of ids' buckets, or none: ok=false names
// how long until all of them have one.
func (l *rateLimiter) Allow(c rateClass, ids ...string) (ok bool, wait time.Duration) {
	n := l.limits.perMinute(c)
	if n <= 0 {
		return true, 0
	}
	step := time.Minute / time.Duration(n)
	tolerance := time.Minute - step
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	next := make([]time.Time, len(ids))
	for i, id := range ids {
		t := l.tat[string(c)+":"+id]
		if t.Before(now) {
			t = now
		}
		if d := t.Sub(now) - tolerance; d > 0 {
			wait = max(wait, d)
		}
		next[i] = t.Add(step)
	}
	if wait > 0 {
		return false, wait
	}
	for i, id := range ids {
		key := string(c) + ":" + id
		if _, held := l.tat[key]; !held && len(l.tat) >= l.max {
			l.sweep(now)
			if len(l.tat) >= l.max {
				l.evict()
			}
		}
		l.tat[key] = next[i]
	}
	return true, 0
}

// Sweep drops every key whose bucket is full again (PurgeLoop calls it).
func (l *rateLimiter) Sweep() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sweep(l.now())
}

func (l *rateLimiter) sweep(now time.Time) int {
	n := 0
	for k, t := range l.tat {
		if !t.After(now) {
			delete(l.tat, k)
			n++
		}
	}
	return n
}

// evict drops the key with the earliest arrival time (the fullest bucket). Callers hold mu.
func (l *rateLimiter) evict() {
	var oldest string
	var at time.Time
	for k, t := range l.tat {
		if oldest == "" || t.Before(at) {
			oldest, at = k, t
		}
	}
	delete(l.tat, oldest)
}

// Len is the number of keys held.
func (l *rateLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.tat)
}

// rateLimit applies the API rate limits to a routed request (it runs inside the mux, where
// r.Pattern is known, after authentication): one bucket per principal (user id, or API key
// id) and one per address, for the request's class. Kill, pause, stop and disarm are never
// limited. Refused: 429 problem+json with Retry-After, counted in a metric.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := classify(r.Method, r.URL.Path, r.Pattern)
		if c == classNever {
			next.ServeHTTP(w, r)
			return
		}
		st := state(r)
		ids := []string{"ip:" + st.ip}
		switch {
		case st.principal.APIKeyID != "":
			ids = append(ids, "key:"+st.principal.APIKeyID)
		case st.principal.UserID != "":
			ids = append(ids, "user:"+st.principal.UserID)
		}
		if ok, wait := s.rates.Allow(c, ids...); !ok {
			s.metrics.rateLimited.WithLabelValues(string(c)).Inc()
			w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
			writeProblem(w, r, http.StatusTooManyRequests, "Too many requests", "slow down: try again after Retry-After seconds", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}
