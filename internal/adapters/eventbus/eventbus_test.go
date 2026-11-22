package eventbus

import (
	"testing"

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
