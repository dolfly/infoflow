package events

import (
	"context"
	"errors"
	"sync"

	"github.com/dolfly/infoflow/bot/dynamiccard"
)

// CardEventHandler receives a normalized interactive card event.
type CardEventHandler func(context.Context, dynamiccard.CardEvent) error

// OnCardEvent installs a typed handler for the
// event.interactive_msg_card_event route and returns an idempotent cleanup
// function. Normalization errors are reported through the dispatcher's error
// handler; the raw payload is never logged.
func (d *Dispatcher) OnCardEvent(handler CardEventHandler) (func(), error) {
	if handler == nil {
		return func() {}, errors.New("card event handler must not be nil")
	}
	wrapper := func(ctx context.Context, event Event) error {
		normalized, err := dynamiccard.NormalizeCardEvent(event.Payload(), event.RawPayload())
		if err != nil {
			return err
		}
		return handler(ctx, normalized)
	}
	if err := d.On(dynamiccard.CardEventRoute, wrapper); err != nil {
		return func() {}, err
	}
	var once sync.Once
	return func() { once.Do(func() { d.Off(dynamiccard.CardEventRoute, wrapper) }) }, nil
}
