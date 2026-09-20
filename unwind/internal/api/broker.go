package api

import "sync"

// Event is one line on the live ticker -- shape is deliberately loose (a
// plain map) since it's a presentation-layer concern, not a ledger record.
// Nothing here is durable; a subscriber that isn't connected when an event
// fires simply never sees it.
type Event map[string]any

// Broker is a minimal in-process pub/sub for Server-Sent Events: every
// POST /v1/act, /v1/demo/run, /v1/swarm/run and /v1/sessions/{id}/rollback
// call publishes here, and GET /v1/stream fans it out to every connected
// browser tab. One process, one broker, no external queue -- this is a
// UI convenience, not a system of record.
type Broker struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBroker() *Broker {
	return &Broker{subs: map[chan Event]struct{}{}}
}

// Subscribe returns a channel of events and an unsubscribe func the caller
// must defer. The channel is buffered so one slow reader can't block
// Publish; if it ever fills, new events are dropped for that subscriber
// rather than stalling the publisher.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
	}
}

func (b *Broker) Publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default: // subscriber's buffer is full -- drop rather than block
		}
	}
}
