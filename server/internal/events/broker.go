package events

import (
	"context"
	"sync"
	"time"
)

const ReplayBatchSize = 64

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
	b.broadcast(event)
	return event, nil
}

func (b *Broker) broadcast(event Event) {
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
}

func (b *Broker) Subscribe(ctx context.Context, after uint64) ([]Event, <-chan Event, error) {
	b.mu.Lock()
	replay, err := b.journal.After(ctx, after, ReplayBatchSize)
	if err != nil {
		b.mu.Unlock()
		return nil, nil, err
	}

	// A full page may have more durable events behind it. Return it without a
	// live subscription; the handler will write the bounded page and ask again.
	// Once a short page is reached, registering under the same broker lock closes
	// the query-to-subscribe race without loading the complete history at once.
	if len(replay) == ReplayBatchSize {
		b.mu.Unlock()
		return replay, nil, nil
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

func (b *Broker) Acknowledged(ctx context.Context, deviceID string) (uint64, error) {
	return b.journal.Acknowledged(ctx, deviceID)
}

func (b *Broker) CreatePairingCode(ctx context.Context, secretHash []byte, expiresAt time.Time) error {
	return b.journal.CreatePairingCode(ctx, secretHash, expiresAt)
}

func (b *Broker) RedeemPairingCode(ctx context.Context, secretHash []byte, deviceID, name string, tokenHash []byte) error {
	return b.journal.RedeemPairingCode(ctx, secretHash, deviceID, name, tokenHash)
}

func (b *Broker) AuthenticateDevice(ctx context.Context, tokenHash []byte) (string, error) {
	return b.journal.AuthenticateDevice(ctx, tokenHash)
}

func (b *Broker) RevokeDevice(ctx context.Context, deviceID string) error {
	return b.journal.RevokeDevice(ctx, deviceID)
}

func (b *Broker) ListDevices(ctx context.Context) ([]DeviceInfo, error) {
	return b.journal.ListDevices(ctx)
}

func (b *Broker) CreateAgentToken(ctx context.Context, tokenID, name string, tokenHash []byte) error {
	return b.journal.CreateAgentToken(ctx, tokenID, name, tokenHash)
}

func (b *Broker) AuthenticateAgent(ctx context.Context, tokenID string, tokenHash []byte) (AgentTokenInfo, error) {
	return b.journal.AuthenticateAgent(ctx, tokenID, tokenHash)
}

func (b *Broker) RevokeAgentToken(ctx context.Context, tokenID string) error {
	return b.journal.RevokeAgentToken(ctx, tokenID)
}

func (b *Broker) ListAgentTokens(ctx context.Context) ([]AgentTokenInfo, error) {
	return b.journal.ListAgentTokens(ctx)
}

func (b *Broker) Stats(ctx context.Context) (ServerStats, error) {
	return b.journal.Stats(ctx)
}

func (b *Broker) SetPagerMessage(ctx context.Context, message string) (Event, error) {
	event, err := b.journal.SetPagerMessage(ctx, message)
	if err != nil {
		return Event{}, err
	}
	b.broadcast(event)
	return event, nil
}

func (b *Broker) PagerState(ctx context.Context) (PagerState, error) {
	return b.journal.PagerState(ctx)
}

func (b *Broker) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}

func (b *Broker) Healthy(ctx context.Context) error {
	return b.journal.Ping(ctx)
}

func (b *Broker) Close() error {
	return b.journal.Close()
}
