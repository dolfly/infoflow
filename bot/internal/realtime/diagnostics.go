// Package realtime contains neutral, redacted observability primitives shared
// by the additive realtime runtime. The package has no output or network side
// effects of its own.
package realtime

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"
)

const (
	SDKLanguage     = "go"
	RedactionPolicy = "sensitive_fields_omitted"
)

var requestSequence atomic.Uint64

type requestIDKey struct{}

// Diagnostic is a closed structured record. It deliberately has no generic
// attributes map: every accepted field is reviewed to ensure it cannot carry
// payloads, credentials, URLs, headers, or underlying error strings.
type Diagnostic struct {
	Event         string    `json:"event"`
	Level         string    `json:"level"`
	Timestamp     time.Time `json:"timestamp"`
	SDKLanguage   string    `json:"sdk_language"`
	Component     string    `json:"component"`
	RequestID     string    `json:"request_id"`
	Redaction     string    `json:"redaction"`
	Operation     string    `json:"operation,omitempty"`
	Route         string    `json:"route_id,omitempty"`
	Result        string    `json:"result,omitempty"`
	ErrorType     string    `json:"error_type,omitempty"`
	PlatformLogID string    `json:"platform_log_id,omitempty"`
	Generation    uint64    `json:"generation,omitempty"`
	Attempt       int       `json:"attempt,omitempty"`
	SeqID         uint64    `json:"seq_id,omitempty"`
	FrameMethod   int32     `json:"frame_method,omitempty"`
	FrameBytes    int       `json:"frame_bytes,omitempty"`
	PayloadBytes  int       `json:"payload_bytes,omitempty"`
	ChatType      string    `json:"chat_type,omitempty"`
	MessageType   string    `json:"message_type,omitempty"`
	HandlerCount  int       `json:"handler_count,omitempty"`
	HTTPStatus    int       `json:"http_status,omitempty"`
	ElapsedMS     int64     `json:"elapsed_ms,omitempty"`
}

// Observer receives redacted structured lifecycle records. Calls may be
// concurrent; implementations must be concurrency safe and should return
// quickly. Panics are isolated from SDK runtime behavior.
type Observer interface {
	Observe(Diagnostic)
}

// DiagnosticHandler adapts a function to Observer. It remains convenient for
// small applications while the interface supports logger adapters.
type DiagnosticHandler func(Diagnostic)

func (handler DiagnosticHandler) Observe(diagnostic Diagnostic) {
	handler(diagnostic)
}

type observerGroup []Observer

func (group observerGroup) Observe(diagnostic Diagnostic) {
	for _, observer := range group {
		emitOne(observer, diagnostic)
	}
}

// Observers combines non-nil observers. A nil result is the no-op default.
func Observers(observers ...Observer) Observer {
	group := make(observerGroup, 0, len(observers))
	for _, observer := range observers {
		if observer != nil {
			group = append(group, observer)
		}
	}
	switch len(group) {
	case 0:
		return nil
	case 1:
		return group[0]
	default:
		return group
	}
}

// EnsureRequestID returns a context carrying an SDK-generated correlation ID.
// Existing SDK IDs are retained so nested Webhook/Event dispatch records can
// be correlated without accepting user-controlled values.
func EnsureRequestID(ctx context.Context) (context.Context, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	if requestID, ok := ctx.Value(requestIDKey{}).(string); ok && requestID != "" {
		return ctx, requestID
	}
	requestID := "go-rt-" + strconv.FormatUint(requestSequence.Add(1), 10)
	return context.WithValue(ctx, requestIDKey{}, requestID), requestID
}

// RequestID returns an SDK-generated correlation ID, if present.
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(requestIDKey{}).(string)
	return requestID
}

// DetachedContext preserves only the SDK correlation ID while removing the
// HTTP request lifetime and every user value. Accepted callback work can then
// finish after a framework returns its response without retaining secrets.
func DetachedContext(ctx context.Context) context.Context {
	requestID := RequestID(ctx)
	if requestID == "" {
		return context.Background()
	}
	return context.WithValue(context.Background(), requestIDKey{}, requestID)
}

// RuntimeError is the safe error passed to realtime error callbacks. The
// underlying error is intentionally not retained because handler and network
// errors can contain application data.
type RuntimeError struct {
	Component string
	Operation string
	Category  string
	Route     string
}

func (e RuntimeError) Error() string {
	if e.Category == "" {
		return fmt.Sprintf("InfoFlow realtime %s operation failed", e.Component)
	}
	return fmt.Sprintf("InfoFlow realtime %s operation failed (%s)", e.Component, e.Category)
}

// ErrorHandler receives safe runtime error categories.
type ErrorHandler func(RuntimeError)

// Emit normalizes required fields and calls an observer while isolating its
// panic from the runtime path.
func Emit(observer Observer, diagnostic Diagnostic) {
	if observer == nil {
		return
	}
	if diagnostic.Timestamp.IsZero() {
		diagnostic.Timestamp = time.Now()
	}
	if diagnostic.SDKLanguage == "" {
		diagnostic.SDKLanguage = SDKLanguage
	}
	if diagnostic.Redaction == "" {
		diagnostic.Redaction = RedactionPolicy
	}
	if diagnostic.RequestID == "" {
		_, diagnostic.RequestID = EnsureRequestID(context.Background())
	}
	emitOne(observer, diagnostic)
}

func emitOne(observer Observer, diagnostic Diagnostic) {
	defer func() { _ = recover() }()
	observer.Observe(diagnostic)
}

// Report calls an error hook while isolating user callback panics.
func Report(handler ErrorHandler, runtimeError RuntimeError) {
	if handler == nil {
		return
	}
	defer func() { _ = recover() }()
	handler(runtimeError)
}
