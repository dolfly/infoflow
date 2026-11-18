package runtime

import (
	"sync"
	"time"
)

type MessageIDGenerator struct {
	mu   sync.Mutex
	last int64
	now  func() time.Time
}

func NewMessageIDGenerator(now func() time.Time) *MessageIDGenerator {
	if now == nil {
		now = time.Now
	}
	return &MessageIDGenerator{now: now}
}

func (g *MessageIDGenerator) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	value := g.now().UnixMilli()
	if value <= g.last {
		value = g.last + 1
	}
	if value <= 0 {
		value = 1
	}
	g.last = value
	return value
}
