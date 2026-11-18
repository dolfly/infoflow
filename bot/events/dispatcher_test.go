package events_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dolfly/infoflow/bot/events"
	"github.com/dolfly/infoflow/bot/internal/realtime"
)

func registerRoutes(t *testing.T, dispatcher *events.Dispatcher, routes []string, called *[]string) {
	t.Helper()
	for _, route := range routes {
		route := route
		if err := dispatcher.On(route, func(context.Context, events.Event) error {
			*called = append(*called, route)
			return nil
		}); err != nil {
			t.Fatalf("On(%q) error = %v", route, err)
		}
	}
}

func assertFrameRoutes(t *testing.T, raw string, routes, want []string) events.Event {
	t.Helper()
	dispatcher := events.NewDispatcher(events.Options{})
	called := make([]string, 0, len(routes))
	var captured events.Event
	for _, route := range routes {
		route := route
		if err := dispatcher.On(route, func(_ context.Context, event events.Event) error {
			called = append(called, route)
			captured = event
			return nil
		}); err != nil {
			t.Fatalf("On(%q) error = %v", route, err)
		}
	}
	if err := dispatcher.DispatchFrame(context.Background(), []byte(raw)); err != nil {
		t.Fatalf("DispatchFrame() error = %v", err)
	}
	if !slices.Equal(called, want) {
		t.Fatalf("routes = %v, want %v", called, want)
	}
	return captured
}

func TestDispatcherPrivateMessageMatrixUsesStableRouteOrder(t *testing.T) {
	types := []string{"text", "image", "file", "richtext", "markdown", "voice"}
	for _, messageType := range types {
		t.Run(messageType, func(t *testing.T) {
			routes := []string{"private." + messageType, messageType, "private.*", "*"}
			event := assertFrameRoutes(t, `{"MsgType":"`+messageType+`","future":{"kept":true}}`, routes, routes)
			if event.Type != "private."+messageType || event.ChatType != events.ChatPrivate || event.MessageType != messageType {
				t.Fatalf("event = %#v", event)
			}
		})
	}
}

func TestDispatcherGroupMessageMatrixUsesStableRouteOrder(t *testing.T) {
	types := []string{"text", "image", "file", "richtext", "markdown", "mixed", "voice"}
	for _, messageType := range types {
		t.Run(messageType, func(t *testing.T) {
			routes := []string{"group." + messageType, messageType, "group.*", "*"}
			raw := `{"groupid":123,"eventtype":"MESSAGE_RECEIVE","message":{"header":{"msgtype":"` + messageType + `"}}}`
			event := assertFrameRoutes(t, raw, routes, routes)
			if event.Type != "group."+messageType || event.ChatType != events.ChatGroup || event.MessageType != messageType {
				t.Fatalf("event = %#v", event)
			}
		})
	}
}

func TestDispatcherEmptyAndMissingMessageType(t *testing.T) {
	t.Run("empty private reaches chat and global wildcard only", func(t *testing.T) {
		assertFrameRoutes(t, `{"MsgType":""}`, []string{"private.*", "*", "text"}, []string{"private.*", "*"})
	})
	t.Run("empty group reaches chat and global wildcard only", func(t *testing.T) {
		assertFrameRoutes(t, `{"groupId":123,"message":{"header":{"msgType":""}}}`, []string{"group.*", "*", "text"}, []string{"group.*", "*"})
	})
	t.Run("missing private message type is not dispatched", func(t *testing.T) {
		assertFrameRoutes(t, `{"future":true}`, []string{"private.*", "*"}, nil)
	})
}

func TestDispatcherGroupSystemMatrix(t *testing.T) {
	tests := map[string]string{
		"GROUP_INFO_CHANGE":  "info_change",
		"GROUP_MANAGER_ADD":  "manager_add",
		"GROUP_MANAGER_DEL":  "manager_del",
		"GROUP_MEMBER_ADD":   "member_add",
		"GROUP_MEMBER_DEL":   "member_del",
		"GROUP_OPEN_APP":     "open_app",
		"GROUP_CLOSE_APP":    "close_app",
		"MENU_CLICK":         "menu_click",
		"EMOJI_REPLY_ADD":    "emoji_reply_add",
		"EMOJI_REPLY_DELETE": "emoji_reply_delete",
	}
	for rawType, normalized := range tests {
		t.Run(rawType, func(t *testing.T) {
			routes := []string{"group.event." + normalized, "group.event.*", "*"}
			raw := `{"groupid":123,"eventType":"` + rawType + `","future":true}`
			event := assertFrameRoutes(t, raw, routes, routes)
			if event.EventType != rawType || event.ChatType != events.ChatGroup {
				t.Fatalf("event = %#v", event)
			}
		})
	}
}

func TestDispatcherTriggerCasingCompatibility(t *testing.T) {
	tests := []string{
		`{"eventtype":"TRIGGER","triggertype":"MESSAGE"}`,
		`{"eventType":"TRIGGER","triggerType":"Message"}`,
		`{"EventType":"TRIGGER","TriggerType":"message"}`,
	}
	for i, raw := range tests {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			routes := []string{"trigger.message", "trigger.*", "*"}
			event := assertFrameRoutes(t, raw, routes, routes)
			if event.TriggerType != "message" {
				t.Fatalf("TriggerType = %q", event.TriggerType)
			}
		})
	}
}

func TestDispatcherEventNamespaceMatrix(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		route string
	}{
		{"auth success", `{"eventtype":"AUTH_SUCCESS","data":{"future":true}}`, "event.auth_success"},
		{"auth rejected", `{"eventType":"AUTH_REJECTED","data":{"future":true}}`, "event.auth_rejected"},
		{"interaction submit", `{"EventType":"TRIGGER","taskContext":{"subscriptionType":"infoflow.interaction.submit"}}`, "event.interaction_submit"},
		{"message summary", `{"MsgType":"event","EventType":"MESSAGE_SUMMARY_RESULT","RequestId":"r1"}`, "event.message_summary_result"},
		{"important summary", `{"msgType":"event","eventType":"important_msg_summary_result","taskId":"t1"}`, "event.important_msg_summary_result"},
		{"future namespace", `{"eventType":"FUTURE_PLATFORM_EVENT","future":{"kept":true}}`, "event.future_platform_event"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes := []string{tt.route, "event.*", "*"}
			event := assertFrameRoutes(t, tt.raw, routes, routes)
			if event.Type != tt.route {
				t.Fatalf("Type = %q, want %q", event.Type, tt.route)
			}
		})
	}
	t.Run("message envelope without MsgType remains dropped", func(t *testing.T) {
		assertFrameRoutes(t, `{"eventtype":"MESSAGE_RECEIVE","future":true}`, []string{"event.*", "*"}, nil)
	})
}

func TestDispatcherLifecycleOnceOffAndFailureIsolation(t *testing.T) {
	var diagnostics []realtime.Diagnostic
	var reported []realtime.RuntimeError
	dispatcher := events.NewDispatcher(events.Options{
		Diagnostics: func(diagnostic realtime.Diagnostic) { diagnostics = append(diagnostics, diagnostic) },
		OnError:     func(runtimeError realtime.RuntimeError) { reported = append(reported, runtimeError) },
	})

	var called []string
	failing := events.Handler(func(context.Context, events.Event) error {
		called = append(called, "failing")
		return errors.New("private-secret-value")
	})
	panicking := events.Handler(func(context.Context, events.Event) error {
		called = append(called, "panicking")
		panic("private-panic-value")
	})
	later := events.Handler(func(context.Context, events.Event) error {
		called = append(called, "later")
		return nil
	})
	once := events.Handler(func(context.Context, events.Event) error {
		called = append(called, "once")
		return nil
	})
	for _, registration := range []struct {
		method string
		fn     events.Handler
	}{{"on", failing}, {"on", panicking}, {"on", later}, {"once", once}} {
		var err error
		if registration.method == "once" {
			err = dispatcher.Once("connected", registration.fn)
		} else {
			err = dispatcher.On("connected", registration.fn)
		}
		if err != nil {
			t.Fatalf("%s error = %v", registration.method, err)
		}
	}

	if err := dispatcher.Dispatch(context.Background(), "connected", events.Event{}); err != nil {
		t.Fatalf("first Dispatch() error = %v", err)
	}
	if err := dispatcher.Dispatch(context.Background(), "connected", events.Event{}); err != nil {
		t.Fatalf("second Dispatch() error = %v", err)
	}
	want := []string{"failing", "panicking", "later", "once", "failing", "panicking", "later"}
	if !reflect.DeepEqual(called, want) {
		t.Fatalf("called = %v, want %v", called, want)
	}
	if len(reported) != 4 || reported[0].Category != "handler_failure" {
		t.Fatalf("reported = %#v", reported)
	}
	if strings.Contains(reported[0].Error(), "private-secret-value") {
		t.Fatalf("safe error leaked handler error: %v", reported[0])
	}
	if len(diagnostics) == 0 {
		t.Fatal("expected structured diagnostics")
	}

	if !dispatcher.Off("connected", failing) || !dispatcher.Off("connected", panicking) || dispatcher.Off("connected", once) {
		t.Fatal("Off() did not preserve original handler identity")
	}
}

func TestDispatcherDirectAndLifecycleRoutesIncludeGlobalWildcardOnce(t *testing.T) {
	for _, route := range []string{"connected", "disconnected", "heartbeat", "error"} {
		t.Run(route, func(t *testing.T) {
			dispatcher := events.NewDispatcher(events.Options{})
			called := make([]string, 0, 2)
			registerRoutes(t, dispatcher, []string{route, "*"}, &called)

			if err := dispatcher.Dispatch(context.Background(), route, events.NewEvent(route, nil, nil)); err != nil {
				t.Fatalf("Dispatch(%q) error = %v", route, err)
			}
			if want := []string{route, "*"}; !slices.Equal(called, want) {
				t.Fatalf("routes = %v, want exact then global %v", called, want)
			}
		})
	}

	t.Run("global route is not invoked twice", func(t *testing.T) {
		dispatcher := events.NewDispatcher(events.Options{})
		called := make([]string, 0, 1)
		registerRoutes(t, dispatcher, []string{"*"}, &called)

		if err := dispatcher.Dispatch(context.Background(), "*", events.NewEvent("*", nil, nil)); err != nil {
			t.Fatalf("Dispatch(*) error = %v", err)
		}
		if want := []string{"*"}; !slices.Equal(called, want) {
			t.Fatalf("routes = %v, want %v", called, want)
		}
	})
}

func TestDispatcherObserverCorrelationExactRedactionAndFailureIsolation(t *testing.T) {
	const (
		payloadSecret = "seed-private-user-message"
		handlerSecret = "seed-private-handler-error"
		tokenSecret   = "seed-private-token"
	)
	var records []realtime.Diagnostic
	observer := realtime.Observers(
		realtime.DiagnosticHandler(func(realtime.Diagnostic) { panic("observer failure") }),
		realtime.DiagnosticHandler(func(record realtime.Diagnostic) { records = append(records, record) }),
	)
	dispatcher := events.NewDispatcher(events.Options{Observer: observer})
	if err := dispatcher.On("private.text", func(context.Context, events.Event) error {
		return errors.New(handlerSecret)
	}); err != nil {
		t.Fatalf("On() error = %v", err)
	}
	raw := []byte(`{"MsgType":"text","content":"` + payloadSecret + `","token":"` + tokenSecret + `"}`)
	if err := dispatcher.DispatchFrame(context.Background(), raw); err != nil {
		t.Fatalf("DispatchFrame() error = %v", err)
	}
	wantEvents := []string{"event.dispatch.start", "event.handler.failure", "event.dispatch.success"}
	if len(records) != len(wantEvents) {
		t.Fatalf("records = %#v, want %d", records, len(wantEvents))
	}
	requestID := records[0].RequestID
	for i, record := range records {
		if record.Event != wantEvents[i] {
			t.Fatalf("record[%d].Event = %q, want %q", i, record.Event, wantEvents[i])
		}
		if record.Level == "" || record.Timestamp.IsZero() || record.SDKLanguage != "go" ||
			record.Component != "events" || record.RequestID != requestID || requestID == "" ||
			record.Redaction != realtime.RedactionPolicy {
			t.Fatalf("record[%d] missing required/correlation fields: %#v", i, record)
		}
	}
	serialized, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, sensitive := range []string{payloadSecret, handlerSecret, tokenSecret, string(raw)} {
		if strings.Contains(string(serialized), sensitive) {
			t.Fatalf("observer records leaked exact sensitive value %q: %s", sensitive, serialized)
		}
	}
}

func TestDispatcherObservabilityBucketsUnknownProtocolOpenRoutesWithoutMutatingEvents(t *testing.T) {
	tests := []struct {
		name            string
		raw             string
		directRoute     string
		exactRoute      string
		wantEventType   string
		wantMessageType string
		wantSafeRoute   string
		wantSafeMessage string
		sensitive       string
	}{
		{
			name: "private message", raw: `{"MsgType":"seed-private-msgtype"}`,
			exactRoute: "private.seed-private-msgtype", wantEventType: "private.seed-private-msgtype",
			wantMessageType: "seed-private-msgtype", wantSafeRoute: "private.other",
			wantSafeMessage: "other", sensitive: "seed-private-msgtype",
		},
		{
			name:       "group message",
			raw:        `{"groupid":1,"eventtype":"MESSAGE_RECEIVE","message":{"header":{"msgtype":"seed-group-msgtype"}}}`,
			exactRoute: "group.seed-group-msgtype", wantEventType: "group.seed-group-msgtype",
			wantMessageType: "seed-group-msgtype", wantSafeRoute: "group.other",
			wantSafeMessage: "other", sensitive: "seed-group-msgtype",
		},
		{
			name: "event namespace", raw: `{"eventtype":"seed-private-eventtype"}`,
			exactRoute: "event.seed-private-eventtype", wantEventType: "event.seed-private-eventtype",
			wantSafeRoute: "event.other", sensitive: "seed-private-eventtype",
		},
		{
			name: "trigger namespace", raw: `{"eventtype":"TRIGGER","triggertype":"seed-private-trigger"}`,
			exactRoute: "trigger.seed-private-trigger", wantEventType: "trigger.seed-private-trigger",
			wantSafeRoute: "trigger.other", sensitive: "seed-private-trigger",
		},
		{
			name: "direct route", directRoute: "seed-private-direct-route",
			exactRoute: "seed-private-direct-route", wantEventType: "seed-private-direct-route",
			wantSafeRoute: "other", sensitive: "seed-private-direct-route",
		},
		{
			name: "known route remains exact", raw: `{"MsgType":"text"}`,
			exactRoute: "private.text", wantEventType: "private.text", wantMessageType: "text",
			wantSafeRoute: "private.text", wantSafeMessage: "text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var records []realtime.Diagnostic
			var reported []realtime.RuntimeError
			var captured events.Event
			dispatcher := events.NewDispatcher(events.Options{
				Diagnostics: func(record realtime.Diagnostic) { records = append(records, record) },
				OnError:     func(runtimeError realtime.RuntimeError) { reported = append(reported, runtimeError) },
			})
			if err := dispatcher.On(tt.exactRoute, func(_ context.Context, event events.Event) error {
				captured = event
				return errors.New("seed-private-handler-error")
			}); err != nil {
				t.Fatalf("On(%q) error = %v", tt.exactRoute, err)
			}

			var err error
			if tt.directRoute != "" {
				err = dispatcher.Dispatch(
					context.Background(), tt.directRoute, events.NewEvent(tt.directRoute, nil, nil),
				)
			} else {
				err = dispatcher.DispatchFrame(context.Background(), []byte(tt.raw))
			}
			if err != nil {
				t.Fatalf("dispatch error = %v", err)
			}
			if captured.Type != tt.wantEventType || captured.MessageType != tt.wantMessageType {
				t.Fatalf("handler event type/message = %q/%q, want %q/%q",
					captured.Type, captured.MessageType, tt.wantEventType, tt.wantMessageType)
			}
			if len(records) != 3 || len(reported) != 1 {
				t.Fatalf("records=%#v reported=%#v", records, reported)
			}
			for index, record := range records {
				if record.Route != tt.wantSafeRoute || record.MessageType != tt.wantSafeMessage {
					t.Fatalf("record[%d] route/message = %q/%q, want %q/%q",
						index, record.Route, record.MessageType, tt.wantSafeRoute, tt.wantSafeMessage)
				}
			}
			if reported[0].Route != tt.wantSafeRoute {
				t.Fatalf("runtime error route = %q, want %q", reported[0].Route, tt.wantSafeRoute)
			}
			serialized, err := json.Marshal(struct {
				Records  []realtime.Diagnostic
				Reported []realtime.RuntimeError
			}{records, reported})
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if tt.sensitive != "" && strings.Contains(string(serialized), tt.sensitive) {
				t.Fatalf("observability leaked sensitive protocol value %q: %s", tt.sensitive, serialized)
			}
		})
	}
}

func TestDispatcherOffOriginalOnceHandlerBeforeDispatch(t *testing.T) {
	dispatcher := events.NewDispatcher(events.Options{})
	var calls atomic.Int32
	handler := events.Handler(func(context.Context, events.Event) error {
		calls.Add(1)
		return nil
	})
	if err := dispatcher.Once("connected", handler); err != nil {
		t.Fatalf("Once() error = %v", err)
	}
	if !dispatcher.Off("connected", handler) {
		t.Fatal("Off() = false, want true")
	}
	if err := dispatcher.Dispatch(context.Background(), "connected", events.Event{}); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("calls = %d, want 0", calls.Load())
	}
}

func TestDispatcherDistinguishesClosuresFromTheSameFunctionLiteral(t *testing.T) {
	dispatcher := events.NewDispatcher(events.Options{})
	var called []string
	newHandler := func(label string) events.Handler {
		return func(context.Context, events.Event) error {
			called = append(called, label)
			return nil
		}
	}
	first := newHandler("first")
	second := newHandler("second")
	if err := dispatcher.On("connected", first); err != nil {
		t.Fatalf("On(first) error = %v", err)
	}
	if err := dispatcher.On("connected", second); err != nil {
		t.Fatalf("On(second) error = %v", err)
	}
	if !dispatcher.Off("connected", first) {
		t.Fatal("Off(first) = false")
	}
	if err := dispatcher.Dispatch(context.Background(), "connected", events.Event{}); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if !reflect.DeepEqual(called, []string{"second"}) {
		t.Fatalf("called = %v, want second closure only", called)
	}
}

func TestDispatcherChallengeRawCopyLargeIntegerAndMalformed(t *testing.T) {
	dispatcher := events.NewDispatcher(events.Options{})
	var calls atomic.Int32
	var captured events.Event
	if err := dispatcher.On("private.text", func(_ context.Context, event events.Event) error {
		calls.Add(1)
		captured = event
		return nil
	}); err != nil {
		t.Fatalf("On() error = %v", err)
	}
	if err := dispatcher.On("*", func(context.Context, events.Event) error {
		calls.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("On(*) error = %v", err)
	}

	if err := dispatcher.DispatchFrame(context.Background(), []byte(`{"echostr":"challenge"}`)); err != nil {
		t.Fatalf("challenge DispatchFrame() error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("challenge dispatched %d handlers", calls.Load())
	}

	raw := []byte(`{"MsgType":"text","messageid":9007199254740993123,"future":{"kept":true}}`)
	if err := dispatcher.DispatchFrame(context.Background(), raw); err != nil {
		t.Fatalf("DispatchFrame() error = %v", err)
	}
	payload := captured.Payload()
	if got := payload["messageid"]; got != json.Number("9007199254740993123") {
		t.Fatalf("large messageid = %#v", got)
	}
	first := captured.RawPayload()
	first[0] = 'x'
	if second := captured.RawPayload(); len(second) == 0 || second[0] != '{' {
		t.Fatalf("RawPayload() not defensive: %q", second)
	}
	payload["messageid"] = "changed"
	if captured.Payload()["messageid"] != json.Number("9007199254740993123") {
		t.Fatal("Payload() shares caller mutation")
	}

	var parseError *events.ParseError
	if err := dispatcher.DispatchFrame(context.Background(), []byte(`{bad-json`)); !errors.As(err, &parseError) {
		t.Fatalf("malformed error = %T %v, want *events.ParseError", err, err)
	}
}

func TestDispatcherRegistrationAndDispatchAreRaceSafe(t *testing.T) {
	dispatcher := events.NewDispatcher(events.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var calls atomic.Int64
	base := events.Handler(func(context.Context, events.Event) error {
		calls.Add(1)
		return nil
	})
	if err := dispatcher.On("private.text", base); err != nil {
		t.Fatalf("On(base) error = %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			handler := events.Handler(func(context.Context, events.Event) error { return nil })
			_ = dispatcher.On("private.text", handler)
			dispatcher.Off("private.text", handler)
		}()
		go func() {
			defer wg.Done()
			_ = dispatcher.DispatchFrame(ctx, []byte(`{"MsgType":"text"}`))
		}()
	}
	wg.Wait()
	if calls.Load() == 0 {
		t.Fatal("base handler was never called")
	}
}
