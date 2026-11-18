package dynamiccard

import (
	"context"
	"strings"
	"sync"

	"github.com/dolfly/infoflow/internal/sdkerrors"
)

// GateState is the sequence state of one card.
type GateState string

const (
	// GateIdle means no request is in flight and the last result is conclusive.
	GateIdle GateState = "idle"
	// GateInflight means one request for the card currently holds the lock.
	GateInflight GateState = "inflight"
	// GatePaused means the last failure left the sequence uncertain; business
	// code decides the next step and the gate never replays by itself.
	GatePaused GateState = "paused"
	// GateExpired means the server reported a state conflict (1010); the card
	// must not be resumed automatically.
	GateExpired GateState = "expired"
)

// GateSnapshot is a point-in-time view of one card's gate.
type GateSnapshot struct {
	State       GateState
	Sequence    int64
	HasSequence bool
}

// Gate serializes dynamic-card mutations per card_id while letting different
// cards proceed in parallel. It stores the last confirmed server sequence and
// never replays a request on its own.
type Gate struct {
	mu      sync.Mutex
	entries map[string]*gateEntry
}

type gateEntry struct {
	sem chan struct{}

	mu          sync.Mutex
	state       GateState
	sequence    int64
	hasSequence bool
}

// NewGate creates an empty sequence gate.
func NewGate() *Gate {
	return &Gate{entries: make(map[string]*gateEntry)}
}

// Lock blocks until the card is idle on this gate and marks it inflight. The
// returned release function must be called exactly once; calling it more than
// once is a no-op. Lock honours context cancellation while waiting.
func (g *Gate) Lock(ctx context.Context, cardID string) (func(), error) {
	if ctx == nil {
		return nil, sdkerrors.NewConfig("context", "must not be nil")
	}
	if strings.TrimSpace(cardID) == "" {
		return nil, sdkerrors.NewConfig("cardId", "must not be blank")
	}
	entry := g.entryFor(cardID)
	select {
	case entry.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	entry.mu.Lock()
	entry.state = GateInflight
	entry.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			entry.mu.Lock()
			if entry.state == GateInflight {
				entry.state = GateIdle
			}
			entry.mu.Unlock()
			<-entry.sem
		})
	}, nil
}

// Snapshot returns the current state and sequence of the card without creating
// an entry.
func (g *Gate) Snapshot(cardID string) GateSnapshot {
	g.mu.Lock()
	entry, ok := g.entries[cardID]
	g.mu.Unlock()
	if !ok {
		return GateSnapshot{State: GateIdle}
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return GateSnapshot{State: entry.state, Sequence: entry.sequence, HasSequence: entry.hasSequence}
}

// Succeed records the sequence returned by a successful request and releases
// the paused/expired state.
func (g *Gate) Succeed(cardID string, sequence int64) {
	entry := g.entryFor(cardID)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.sequence = sequence
	entry.hasSequence = true
	entry.state = GateIdle
}

// Fail records a failed request. 1009 overwrites the local sequence from
// current_sequence and pauses the card; 6001 never rolls the sequence back;
// 1010 marks the card expired; any other failure pauses the card so an
// uncertain result is never followed by an automatic write.
func (g *Gate) Fail(cardID string, code int64, currentSequence *int64) {
	entry := g.entryFor(cardID)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	switch code {
	case 1009:
		if currentSequence != nil {
			entry.sequence = *currentSequence
			entry.hasSequence = true
		}
		entry.state = GatePaused
	case 6001:
		entry.state = GateIdle
	case 1010:
		entry.state = GateExpired
	default:
		entry.state = GatePaused
	}
}

func (g *Gate) entryFor(cardID string) *gateEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	entry, ok := g.entries[cardID]
	if !ok {
		entry = &gateEntry{sem: make(chan struct{}, 1), state: GateIdle}
		g.entries[cardID] = entry
	}
	return entry
}
