package fakes

import (
	"sync"

	"github.com/albievan/cactustrade/internal/ports"
)

// Bus is a recording ports.EventBus: it keeps every published event in order and, like the
// real bus, never blocks a publisher (a full subscriber misses the event, counted in
// Dropped). Safe for concurrent use.
type Bus struct {
	mu      sync.Mutex
	events  []ports.Event
	subs    map[int]chan ports.Event
	next    int
	dropped int
}

var _ ports.EventBus = (*Bus)(nil)

// Publish records e and offers it to every subscriber that has room.
func (b *Bus) Publish(e ports.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, e)
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
			b.dropped++
		}
	}
}

// Subscribe registers a subscriber with a buffer (min 1); the cancel function closes it.
func (b *Bus) Subscribe(buffer int) (<-chan ports.Event, func()) {
	ch := make(chan ports.Event, max(1, buffer))
	b.mu.Lock()
	if b.subs == nil {
		b.subs = map[int]chan ports.Event{}
	}
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
			close(ch)
		})
	}
}

// Events returns the events published so far, in order.
func (b *Bus) Events() []ports.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]ports.Event(nil), b.events...)
}

// OfType returns the published events of one type, in order.
func (b *Bus) OfType(typ string) []ports.Event {
	var out []ports.Event
	for _, e := range b.Events() {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// Dropped counts events a full subscriber missed.
func (b *Bus) Dropped() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}
