// Package events provides the shared, transport-neutral InfoFlow event
// dispatcher used by both Webhook and WebSocket receive modes.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/dolfly/infoflow/bot/dynamiccard"
	"github.com/dolfly/infoflow/bot/internal/realtime"
)

// ChatType identifies the normalized chat namespace.
type ChatType string

const (
	ChatPrivate ChatType = "private"
	ChatGroup   ChatType = "group"
)

// Event is the stable value passed to handlers. Protocol-open payload data is
// available only through copying accessors.
type Event struct {
	Type        string
	ChatType    ChatType
	MessageType string
	EventType   string
	TriggerType string
	Timestamp   time.Time

	payload map[string]any
	raw     []byte
}

// NewEvent constructs a transport lifecycle event with defensive copies. It
// is used by WebSocket connected/disconnected/heartbeat/response delivery.
func NewEvent(eventType string, payload map[string]any, raw []byte) Event {
	return Event{
		Type: eventType, Timestamp: time.Now(), payload: clonePayload(payload), raw: append([]byte(nil), raw...),
	}
}

// Payload returns a defensive deep copy while retaining json.Number values.
func (e Event) Payload() map[string]any {
	return clonePayload(e.payload)
}

// RawPayload returns a defensive copy of the original JSON or XML payload.
func (e Event) RawPayload() []byte {
	return append([]byte(nil), e.raw...)
}

// Handler handles one normalized event.
type Handler func(context.Context, Event) error

// Options configures safe diagnostics and handler-error reporting.
type Options struct {
	Observer    realtime.Observer
	Diagnostics realtime.DiagnosticHandler
	OnError     realtime.ErrorHandler
	Now         func() time.Time
}

// ParseError reports an invalid protocol-open payload without rendering it.
type ParseError struct {
	raw   []byte
	cause error
}

func (e *ParseError) Error() string { return "invalid InfoFlow event payload" }
func (e *ParseError) Unwrap() error { return e.cause }
func (e *ParseError) RawPayload() []byte {
	return append([]byte(nil), e.raw...)
}

type registration struct {
	handler Handler
	key     handlerKey
	once    bool
}

// A Go function value is a pointer to a runtime function value; its reflected
// Pointer reports only the shared code entry. Combining both values preserves
// the identity of distinct closures created from the same function literal,
// which Off must distinguish. The original Handler value remains retained by
// registration for the entire lifetime of this key.
type handlerKey struct {
	code  uintptr
	value uintptr
}

func identity(handler Handler) handlerKey {
	return handlerKey{
		code:  reflect.ValueOf(handler).Pointer(),
		value: *(*uintptr)(unsafe.Pointer(&handler)),
	}
}

// Dispatcher stores handlers in registration order and is safe for concurrent
// registration and dispatch. Handler work runs outside the registry lock.
type Dispatcher struct {
	mu       sync.RWMutex
	handlers map[string][]registration
	observer realtime.Observer
	onError  realtime.ErrorHandler
	now      func() time.Time
}

func NewDispatcher(options Options) *Dispatcher {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Dispatcher{
		handlers: make(map[string][]registration),
		observer: realtime.Observers(options.Observer, options.Diagnostics),
		onError:  options.OnError,
		now:      now,
	}
}

// On registers a handler in stable insertion order.
func (d *Dispatcher) On(route string, handler Handler) error {
	return d.register(route, handler, false)
}

// Once registers a handler that is removed before its first invocation.
func (d *Dispatcher) Once(route string, handler Handler) error {
	return d.register(route, handler, true)
}

func (d *Dispatcher) register(route string, handler Handler, once bool) error {
	if strings.TrimSpace(route) == "" {
		return errors.New("event route must not be blank")
	}
	if handler == nil {
		return errors.New("event handler must not be nil")
	}
	key := identity(handler)
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, existing := range d.handlers[route] {
		if existing.key == key {
			return nil
		}
	}
	d.handlers[route] = append(d.handlers[route], registration{handler: handler, key: key, once: once})
	return nil
}

// Off removes registrations that match the original handler passed to On or
// Once. It returns whether a registration was removed.
func (d *Dispatcher) Off(route string, handler Handler) bool {
	if handler == nil {
		return false
	}
	key := identity(handler)
	d.mu.Lock()
	defer d.mu.Unlock()
	registrations := d.handlers[route]
	kept := registrations[:0]
	removed := false
	for _, existing := range registrations {
		if existing.key == key {
			removed = true
			continue
		}
		kept = append(kept, existing)
	}
	if len(kept) == 0 {
		delete(d.handlers, route)
	} else {
		d.handlers[route] = kept
	}
	return removed
}

// Dispatch sends an already normalized event to the exact route followed by
// the global wildcard. Dispatching "*" invokes the wildcard only once.
func (d *Dispatcher) Dispatch(ctx context.Context, route string, event Event) error {
	if ctx == nil {
		return errors.New("event context must not be nil")
	}
	if strings.TrimSpace(route) == "" {
		return errors.New("event route must not be blank")
	}
	if event.Type == "" {
		event.Type = route
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = d.now()
	}
	routes := []string{route}
	if route != "*" {
		routes = append(routes, "*")
	}
	return d.invoke(ctx, routes, event)
}

// DispatchFrame decodes and classifies one protocol-open JSON payload.
func (d *Dispatcher) DispatchFrame(ctx context.Context, raw []byte) error {
	payload, err := decodeObject(raw)
	if err != nil {
		return &ParseError{raw: append([]byte(nil), raw...), cause: err}
	}
	return d.DispatchPayload(ctx, payload, raw)
}

// DispatchPayload is the advanced path for payloads that were decoded by a
// transport (for example Webhook XML fallback or WebSocket Encrypt expansion).
// Both payload and raw bytes are copied before handler invocation.
func (d *Dispatcher) DispatchPayload(ctx context.Context, payload map[string]any, raw []byte) error {
	if ctx == nil {
		return errors.New("event context must not be nil")
	}
	if payload == nil {
		return &ParseError{raw: append([]byte(nil), raw...), cause: errors.New("payload is nil")}
	}
	if _, challenge := payload["echostr"]; challenge {
		return nil
	}
	classification := classify(payload)
	if !classification.dispatch {
		_, requestID := realtime.EnsureRequestID(ctx)
		realtime.Emit(d.observer, realtime.Diagnostic{
			Level: "debug", Event: "event.dispatch.no_handler", Component: "events",
			Operation: "classify", Result: "skipped", RequestID: requestID, Timestamp: d.now(),
		})
		return nil
	}
	if raw == nil {
		raw, _ = json.Marshal(payload)
	}
	event := Event{
		Type:        classification.eventType,
		ChatType:    classification.chatType,
		MessageType: classification.messageType,
		EventType:   classification.rawEventType,
		TriggerType: classification.triggerType,
		Timestamp:   d.now(),
		payload:     clonePayload(payload),
		raw:         append([]byte(nil), raw...),
	}
	return d.invoke(ctx, classification.routes, event)
}

func (d *Dispatcher) invoke(ctx context.Context, routes []string, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, requestID := realtime.EnsureRequestID(ctx)
	type invocation struct {
		route        string
		registration registration
	}
	queue := make([]invocation, 0)
	for _, route := range routes {
		for _, current := range d.take(route) {
			queue = append(queue, invocation{route: route, registration: current})
		}
	}
	safeEventRoute := diagnosticRoute(event.Type)
	safeMessageType := diagnosticMessageType(event.MessageType)
	realtime.Emit(d.observer, realtime.Diagnostic{
		Level: "debug", Event: "event.dispatch.start", Component: "events",
		Operation: "dispatch", Route: safeEventRoute, Result: "started", RequestID: requestID,
		Timestamp: d.now(), ChatType: string(event.ChatType), MessageType: safeMessageType,
		HandlerCount: len(queue),
	})
	invoked := 0
	for _, current := range queue {
		if err := ctx.Err(); err != nil {
			return err
		}
		invoked++
		if err := callHandler(current.registration.handler, ctx, event); err != nil {
			safeHandlerRoute := diagnosticRoute(current.route)
			runtimeError := realtime.RuntimeError{
				Component: "events", Operation: "handler", Category: "handler_failure", Route: safeHandlerRoute,
			}
			realtime.Report(d.onError, runtimeError)
			realtime.Emit(d.observer, realtime.Diagnostic{
				Level: "warn", Event: "event.handler.failure", Component: "events",
				Operation: "handler", Route: safeHandlerRoute, Result: "failure", ErrorType: "handler_failure",
				RequestID: requestID, Timestamp: d.now(), ChatType: string(event.ChatType),
				MessageType: safeMessageType, HandlerCount: len(queue),
			})
		}
	}
	eventName := "event.dispatch.success"
	result := "success"
	if invoked == 0 {
		eventName = "event.dispatch.no_handler"
		result = "no_handler"
	}
	realtime.Emit(d.observer, realtime.Diagnostic{
		Level: "debug", Event: eventName, Component: "events",
		Operation: "dispatch", Route: safeEventRoute, Result: result, RequestID: requestID,
		Timestamp: d.now(), ChatType: string(event.ChatType), MessageType: safeMessageType,
		HandlerCount: invoked,
	})
	return nil
}

var knownMessageTypes = map[string]struct{}{
	"text": {}, "image": {}, "file": {}, "richtext": {},
	"markdown": {}, "voice": {}, "mixed": {},
}

var knownGroupEventRoutes = map[string]struct{}{
	"group.event.info_change": {}, "group.event.manager_add": {},
	"group.event.manager_del": {}, "group.event.member_add": {},
	"group.event.member_del": {}, "group.event.open_app": {},
	"group.event.close_app": {}, "group.event.menu_click": {},
	"group.event.emoji_reply_add": {}, "group.event.emoji_reply_delete": {},
}

var knownEventRoutes = map[string]struct{}{
	"event.auth_success": {}, "event.auth_rejected": {},
	"event.interaction_submit": {}, "event.message_summary_result": {},
	"event.important_msg_summary_result": {}, "event.interactive_msg_card_event": {},
}

func diagnosticMessageType(messageType string) string {
	if messageType == "" {
		return ""
	}
	if _, known := knownMessageTypes[messageType]; known {
		return messageType
	}
	return "other"
}

func diagnosticRoute(route string) string {
	switch route {
	case "*", "private.*", "group.*", "group.event.*", "event.*", "trigger.*",
		"connected", "disconnected", "heartbeat", "error", "response":
		return route
	}
	if _, known := knownMessageTypes[route]; known {
		return route
	}
	if strings.HasPrefix(route, "private.") {
		if _, known := knownMessageTypes[strings.TrimPrefix(route, "private.")]; known {
			return route
		}
		return "private.other"
	}
	if strings.HasPrefix(route, "group.") {
		if _, known := knownMessageTypes[strings.TrimPrefix(route, "group.")]; known {
			return route
		}
		if _, known := knownGroupEventRoutes[route]; known {
			return route
		}
		return "group.other"
	}
	if strings.HasPrefix(route, "event.") {
		if _, known := knownEventRoutes[route]; known {
			return route
		}
		return "event.other"
	}
	if strings.HasPrefix(route, "trigger.") {
		if route == "trigger.message" {
			return route
		}
		return "trigger.other"
	}
	return "other"
}

func callHandler(handler Handler, ctx context.Context, event Event) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("event handler panicked")
		}
	}()
	return handler(ctx, event)
}

func (d *Dispatcher) take(route string) []registration {
	d.mu.Lock()
	defer d.mu.Unlock()
	registrations := d.handlers[route]
	if len(registrations) == 0 {
		return nil
	}
	result := append([]registration(nil), registrations...)
	kept := registrations[:0]
	for _, current := range registrations {
		if !current.once {
			kept = append(kept, current)
		}
	}
	if len(kept) == 0 {
		delete(d.handlers, route)
	} else {
		d.handlers[route] = kept
	}
	return result
}

type frameClassification struct {
	dispatch     bool
	routes       []string
	eventType    string
	chatType     ChatType
	messageType  string
	rawEventType string
	triggerType  string
}

func classify(payload map[string]any) frameClassification {
	rawEventType, _ := firstString(payload, "eventtype", "eventType", "EventType")
	triggerType, _ := firstString(payload, "triggertype", "triggerType", "TriggerType")

	// Interactive card events use an exact, case-sensitive outer EventType. The
	// branch must precede the group/private message branches so a card event that
	// also carries a group id is never rerouted as a group event.
	if rawEventType == dynamiccard.CardEventType {
		return namespaceClassification("interactive_msg_card_event", rawEventType, triggerType)
	}

	if strings.EqualFold(rawEventType, "TRIGGER") {
		if taskContext, ok := payload["taskContext"].(map[string]any); ok {
			if subscription, _ := firstString(taskContext, "subscriptionType"); subscription == "infoflow.interaction.submit" {
				return namespaceClassification("interaction_submit", rawEventType, triggerType)
			}
		}
	}
	upperEventType := strings.ToUpper(rawEventType)
	if upperEventType == "AUTH_SUCCESS" || upperEventType == "AUTH_REJECTED" {
		return namespaceClassification(strings.ToLower(upperEventType), rawEventType, triggerType)
	}
	msgType, msgTypePresent := firstString(payload, "MsgType", "msgType")
	if msgTypePresent && strings.EqualFold(msgType, "event") && (rawEventType != "" || triggerType != "") {
		name := triggerType
		if strings.TrimSpace(name) == "" {
			name = rawEventType
		}
		return namespaceClassification(strings.ToLower(name), rawEventType, triggerType)
	}
	if strings.EqualFold(rawEventType, "TRIGGER") {
		normalized := strings.ToLower(triggerType)
		routes := []string{"trigger.*", "*"}
		eventType := "trigger."
		if normalized != "" {
			eventType += normalized
			routes = append([]string{eventType}, routes...)
		}
		return frameClassification{true, routes, eventType, "", "", rawEventType, normalized}
	}

	if _, groupID := firstValue(payload, "groupid", "groupId"); groupID {
		message, hasMessage := payload["message"].(map[string]any)
		isMessage := upperEventType == "MESSAGE_RECEIVE" || upperEventType == "ALL_MESSAGE_FORWARD" || (rawEventType == "" && hasMessage)
		if isMessage {
			messageType, present := groupMessageType(message)
			if !present {
				return frameClassification{}
			}
			normalized := strings.ToLower(messageType)
			if normalized == "" {
				return frameClassification{true, []string{"group.*", "*"}, "group.", ChatGroup, "", rawEventType, ""}
			}
			return frameClassification{
				true, []string{"group." + normalized, normalized, "group.*", "*"},
				"group." + normalized, ChatGroup, normalized, rawEventType, "",
			}
		}
		if rawEventType == "" {
			return frameClassification{}
		}
		normalized := strings.TrimPrefix(strings.ToLower(rawEventType), "group_")
		route := "group.event." + normalized
		return frameClassification{true, []string{route, "group.event.*", "*"}, route, ChatGroup, "", rawEventType, ""}
	}

	if !msgTypePresent {
		// A message-delivery envelope without MsgType is an incomplete private
		// message and must remain dropped. Every other named event stays
		// protocol-open under the event namespace instead of requiring a
		// hard-coded allowlist.
		if rawEventType != "" && upperEventType != "MESSAGE_RECEIVE" && upperEventType != "ALL_MESSAGE_FORWARD" {
			return namespaceClassification(strings.ToLower(rawEventType), rawEventType, triggerType)
		}
		return frameClassification{}
	}
	normalized := strings.ToLower(msgType)
	if normalized == "" {
		return frameClassification{true, []string{"private.*", "*"}, "private.", ChatPrivate, "", rawEventType, ""}
	}
	return frameClassification{
		true, []string{"private." + normalized, normalized, "private.*", "*"},
		"private." + normalized, ChatPrivate, normalized, rawEventType, "",
	}
}

func namespaceClassification(name, rawEventType, triggerType string) frameClassification {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		normalized = "unknown"
	}
	route := "event." + normalized
	return frameClassification{true, []string{route, "event.*", "*"}, route, "", "", rawEventType, strings.ToLower(triggerType)}
}

func groupMessageType(message map[string]any) (string, bool) {
	if message == nil {
		return "", false
	}
	if header, ok := message["header"].(map[string]any); ok {
		if value, present := firstString(header, "msgtype", "msgType"); present {
			return value, true
		}
	}
	return firstString(message, "MsgType", "msgType")
}

func firstString(payload map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		value, exists := payload[key]
		if !exists || value == nil {
			continue
		}
		if text, ok := value.(string); ok {
			return text, true
		}
	}
	for current, value := range payload {
		for _, key := range keys {
			if !strings.EqualFold(current, key) {
				continue
			}
			if text, ok := value.(string); ok {
				return text, true
			}
		}
	}
	return "", false
}

func firstValue(payload map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, exists := payload[key]; exists && value != nil {
			return value, true
		}
	}
	for current, value := range payload {
		for _, key := range keys {
			if strings.EqualFold(current, key) && value != nil {
				return value, true
			}
		}
	}
	return nil, false
}

func decodeObject(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, errors.New("payload must be an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("payload contains trailing JSON")
		}
		return nil, err
	}
	return payload, nil
}

func clonePayload(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	clone, err := decodeObject(raw)
	if err != nil {
		return nil
	}
	return clone
}
