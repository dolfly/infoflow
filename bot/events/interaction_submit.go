package events

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

// InteractionSubmitHandler receives a normalized interaction-card user submit
// event.
type InteractionSubmitHandler func(context.Context, InteractionSubmitEvent) error

// InteractionSubmitCaller is the caller context echoed by the rendering
// service in a submit event.
type InteractionSubmitCaller struct {
	CallerName string
	RequestID  string
}

// InteractionSubmitReceiver is the receiver context echoed by the rendering
// service in a submit event.
type InteractionSubmitReceiver struct {
	ID   string
	Type string
}

// InteractionSubmitEvent is the cross-language normalized interaction-card
// user submit event. Submit carries the opaque user content unchanged; the SDK
// never interprets its internal structure.
type InteractionSubmitEvent struct {
	EventID        string
	CardInstanceID string
	UserID         string
	Timestamp      time.Time
	GroupID        string
	CardID         string
	Version        string
	Caller         InteractionSubmitCaller
	Receiver       InteractionSubmitReceiver
	Submit         any
	Payload        map[string]any
}

// InteractionSubmitError reports a payload that is not an interaction-card
// user submit event.
type InteractionSubmitError struct{ Reason string }

func (e *InteractionSubmitError) Error() string {
	if e.Reason == "" {
		return "not an interaction submit event"
	}
	return "not an interaction submit event: " + e.Reason
}

// OnInteractionSubmit installs a typed handler for the event.interaction_submit
// route and returns an idempotent cleanup function. The dispatcher remains
// responsible for concurrent registration and dispatch; cleanup only removes
// this handler instance.
func (d *Dispatcher) OnInteractionSubmit(handler InteractionSubmitHandler) (func(), error) {
	if handler == nil {
		return func() {}, errors.New("interaction submit handler must not be nil")
	}
	wrapper := func(ctx context.Context, event Event) error {
		normalized, err := NormalizeInteractionSubmitEvent(event)
		if err != nil {
			return err
		}
		return handler(ctx, normalized)
	}
	if err := d.On("event.interaction_submit", wrapper); err != nil {
		return func() {}, err
	}
	var once sync.Once
	return func() { once.Do(func() { d.Off("event.interaction_submit", wrapper) }) }, nil
}

// NormalizeInteractionSubmitEvent classifies and normalizes one protocol-open
// event into the stable interaction-card submit shape. It returns
// InteractionSubmitError when the payload is not an interaction submit event.
// The returned Payload is a defensive copy of the original protocol payload.
func NormalizeInteractionSubmitEvent(event Event) (InteractionSubmitEvent, error) {
	payload := event.Payload()
	if payload == nil {
		return InteractionSubmitEvent{}, &InteractionSubmitError{Reason: "payload is empty"}
	}
	eventType, _ := firstString(payload, "EventType", "eventType")
	if !strings.EqualFold(eventType, "TRIGGER") {
		return InteractionSubmitEvent{}, &InteractionSubmitError{Reason: "EventType is not TRIGGER"}
	}
	taskContext, ok := firstValue(payload, "taskContext")
	if !ok {
		return InteractionSubmitEvent{}, &InteractionSubmitError{Reason: "taskContext is missing"}
	}
	task, ok := taskContext.(map[string]any)
	if !ok {
		return InteractionSubmitEvent{}, &InteractionSubmitError{Reason: "taskContext is not an object"}
	}
	subscriptionType, _ := firstString(task, "subscriptionType")
	if subscriptionType != "infoflow.interaction.submit" {
		return InteractionSubmitEvent{}, &InteractionSubmitError{Reason: "subscriptionType is not infoflow.interaction.submit"}
	}

	timestamp := event.Timestamp
	if millis, present := firstMillis(payload, "Time", "time"); present && millis > 0 {
		timestamp = time.UnixMilli(millis)
	}

	normalized := InteractionSubmitEvent{
		EventID:        stringOrNumber(firstValue(payload, "MsgId", "msgId", "MsgID")),
		CardInstanceID: firstStringValue(task, "cardInstanceId"),
		UserID:         firstStringValue(task, "userId"),
		Timestamp:      timestamp,
		GroupID:        stringOrNumber(firstValue(task, "groupId")),
		CardID:         stringOrNumber(firstValue(task, "cardId")),
		Version:        firstStringValue(task, "version"),
		Submit:         firstValueRaw(task, "submit"),
		Payload:        payload,
	}
	if caller, ok := firstValueOnly(task, "caller").(map[string]any); ok && caller != nil {
		normalized.Caller = InteractionSubmitCaller{
			CallerName: firstStringValue(caller, "callerName"),
			RequestID:  stringOrNumber(firstValue(caller, "requestId")),
		}
	}
	if receiver, ok := firstValueOnly(task, "receiver").(map[string]any); ok && receiver != nil {
		normalized.Receiver = InteractionSubmitReceiver{
			ID:   stringOrNumber(firstValue(receiver, "id")),
			Type: firstStringValue(receiver, "type"),
		}
	}
	return normalized, nil
}

// firstStringValue returns the first string value found among the keys, or "".
func firstStringValue(m map[string]any, keys ...string) string {
	value, ok := firstValue(m, keys...)
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// stringOrNumber converts a scalar payload value (string or json.Number) to
// its string form, or returns "" when absent or not scalar.
func stringOrNumber(value any, present bool) string {
	if !present || value == nil {
		return ""
	}
	switch current := value.(type) {
	case string:
		return current
	case json.Number:
		return current.String()
	case float64:
		return strconv.FormatFloat(current, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(current, 10)
	case int:
		return strconv.Itoa(current)
	default:
		return ""
	}
}

// firstMillis reads a millisecond epoch value (number or numeric string) from
// the payload among the given keys.
func firstMillis(m map[string]any, keys ...string) (int64, bool) {
	value, ok := firstValue(m, keys...)
	if !ok || value == nil {
		return 0, false
	}
	switch current := value.(type) {
	case json.Number:
		millis, err := current.Int64()
		return millis, err == nil
	case float64:
		return int64(current), true
	case int64:
		return current, true
	case int:
		return int64(current), true
	case string:
		millis, err := strconv.ParseInt(strings.TrimSpace(current), 10, 64)
		return millis, err == nil
	default:
		return 0, false
	}
}

// firstValueRaw returns the raw value for a key without case-sensitive
// fallback text conversion; it is used for opaque passthrough fields.
func firstValueRaw(m map[string]any, keys ...string) any {
	value, ok := firstValue(m, keys...)
	if !ok {
		return nil
	}
	return value
}

// firstValueOnly returns the first value among the keys, or nil.
func firstValueOnly(m map[string]any, keys ...string) any {
	value, _ := firstValue(m, keys...)
	return value
}
