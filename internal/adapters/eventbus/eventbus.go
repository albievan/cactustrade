// Package eventbus is an in-process ports.EventBus. A slow subscriber never blocks a
// publisher: when its buffer is full the event is dropped for it and counted.
package eventbus

import (
	"sync"
	"sync/atomic"

	"github.com/albievan/cactustrade/internal/ports"
)

// Bus fans events out to subscribers.
type Bus struct {
	mu      sync.Mutex
	subs    map[int]chan ports.Event
	next    int
	dropped atomic.Int64
}

var _ ports.EventBus = (*Bus)(nil)

// New returns an empty bus.
func New() *Bus { return &Bus{subs: map[int]chan ports.Event{}} }

// Publish delivers e to every subscriber that has room.
func (b *Bus) Publish(e ports.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
			b.dropped.Add(1)
		}
	}
}

// Subscribe registers a subscriber with a buffer (min 1).
func (b *Bus) Subscribe(buffer int) (<-chan ports.Event, func()) {
	ch := make(chan ports.Event, max(1, buffer))
	b.mu.Lock()
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

// Dropped counts events not delivered to a full subscriber.
func (b *Bus) Dropped() int64 { return b.dropped.Load() }
