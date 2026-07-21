package events

import (
	"context"
	"sync"
)

type Broker struct {
	mu          sync.Mutex
	journal     Journal
	subscribers map[chan Event]struct{}
}

func NewBroker(journal Journal) *Broker {
	return &Broker{
		journal:     journal,
		subscribers: make(map[chan Event]struct{}),
	}
}

func (b *Broker) Publish(ctx context.Context, input Input) (Event, error) {
	event, err := b.journal.Append(ctx, input)
	if err != nil {
		return Event{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	for subscriber := range b.subscribers {
		select {
		case subscriber <- event:
		default:
			// A slow client must reconnect and replay from its durable cursor.
			// Silently dropping one event while keeping the stream open would
			// violate the protocol's at-least-once delivery contract.
			delete(b.subscribers, subscriber)
			close(subscriber)
		}
	}

	return event, nil
}

func (b *Broker) Subscribe(ctx context.Context, after uint64) ([]Event, <-chan Event, error) {
	b.mu.Lock()
	replay, err := b.journal.After(ctx, after)
	if err != nil {
		b.mu.Unlock()
		return nil, nil, err
	}

	stream := make(chan Event, 32)
	b.subscribers[stream] = struct{}{}
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		if _, exists := b.subscribers[stream]; exists {
			delete(b.subscribers, stream)
			close(stream)
		}
		b.mu.Unlock()
	}()

	return replay, stream, nil
}

func (b *Broker) Acknowledge(ctx context.Context, deviceID string, through uint64) error {
	return b.journal.Acknowledge(ctx, deviceID, through)
}

func (b *Broker) Healthy(ctx context.Context) error {
	return b.journal.Ping(ctx)
}

func (b *Broker) Close() error {
	return b.journal.Close()
}
