package eventbus

import (
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/ports"
)

func TestBus(t *testing.T) {
	b := New()
	a, cancelA := b.Subscribe(2)
	c, cancelC := b.Subscribe(0)
	b.Publish(ports.Event{Type: "one"})
	b.Publish(ports.Event{Type: "two"})
	if e := <-a; e.Type != "one" {
		t.Fatal(e)
	}
	if e := <-a; e.Type != "two" {
		t.Fatal(e)
	}
	if e := <-c; e.Type != "one" {
		t.Fatal(e)
	}
	if b.Dropped() != 1 {
		t.Fatalf("dropped = %d", b.Dropped())
	}
	cancelA()
	cancelA() // idempotent
	if _, ok := <-a; ok {
		t.Fatal("closed")
	}
	b.Publish(ports.Event{Type: "three"})
	cancelC()
}

// TestFullSubscriberNeverBlocks: a subscriber that never reads cannot hold up a publisher
// (PLAN 6.6b: the executor publishes every order transition).
func TestFullSubscriberNeverBlocks(t *testing.T) {
	b := New()
	_, cancel := b.Subscribe(1)
	defer cancel()
	done := make(chan struct{})
	go func() {
		for range 1000 {
			b.Publish(ports.Event{Type: "order.updated"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a full subscriber blocked the publisher")
	}
	if b.Dropped() != 999 {
		t.Fatalf("dropped = %d, want 999", b.Dropped())
	}
}
