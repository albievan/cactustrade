package main

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type countingSender struct {
	calls atomic.Int32
	err   error
}

func (c *countingSender) SendDueStatements(context.Context) (int, error) {
	c.calls.Add(1)
	return 1, c.err
}

// TestStatementsLoop: the loop runs the use case first after the start-up wait, then every
// interval, survives an error and stops with its context.
func TestStatementsLoop(t *testing.T) {
	for _, err := range []error{nil, errors.New("store down")} {
		s := &countingSender{err: err}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			statementsLoop(ctx, s, slog.New(slog.DiscardHandler), time.Millisecond, time.Millisecond)
			close(done)
		}()
		deadline := time.Now().Add(5 * time.Second)
		for s.calls.Load() < 3 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		cancel()
		<-done
		if s.calls.Load() < 3 {
			t.Fatalf("calls = %d", s.calls.Load())
		}
	}
}
