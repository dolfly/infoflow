package events_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dolfly/infoflow/bot/events"
)

const interactionSubmitSample = `{
  "EventType": "TRIGGER",
  "FromPlatform": "4",
  "agentId": "33743",
  "MsgType": "text",
  "MsgId": "task_xxx",
  "Time": 1780550400000,
  "taskContext": {
    "version": "0.0.1",
    "subscriptionType": "infoflow.interaction.submit",
    "cardInstanceId": "card_inst_01JZ8Q3Z7VQ4YQ6J4N8B2D9K1A",
    "groupId": "group_xxx",
    "userId": "submit_user",
    "cardId": "card_xxx",
    "caller": {"callerName": "bridge", "requestId": "bridge_req_001"},
    "receiver": {"id": "group_xxx", "type": "group"},
    "submit": {"fields": {"reason": "ok"}, "eventKey": "submit"}
  }
}`

func TestInteractionSubmitNormalization(t *testing.T) {
	var captured events.Event
	dispatcher := events.NewDispatcher(events.Options{})
	if err := dispatcher.On("event.interaction_submit", func(_ context.Context, event events.Event) error {
		captured = event
		return nil
	}); err != nil {
		t.Fatalf("On() error = %v", err)
	}
	if err := dispatcher.DispatchFrame(context.Background(), []byte(interactionSubmitSample)); err != nil {
		t.Fatalf("DispatchFrame() error = %v", err)
	}
	if captured.Type != "event.interaction_submit" {
		t.Fatalf("Type = %q", captured.Type)
	}

	normalized, err := events.NormalizeInteractionSubmitEvent(captured)
	if err != nil {
		t.Fatalf("NormalizeInteractionSubmitEvent() error = %v", err)
	}
	if normalized.EventID != "task_xxx" {
		t.Fatalf("EventID = %q", normalized.EventID)
	}
	if normalized.CardInstanceID != "card_inst_01JZ8Q3Z7VQ4YQ6J4N8B2D9K1A" {
		t.Fatalf("CardInstanceID = %q", normalized.CardInstanceID)
	}
	if normalized.UserID != "submit_user" {
		t.Fatalf("UserID = %q", normalized.UserID)
	}
	if normalized.GroupID != "group_xxx" {
		t.Fatalf("GroupID = %q", normalized.GroupID)
	}
	if normalized.CardID != "card_xxx" {
		t.Fatalf("CardID = %q", normalized.CardID)
	}
	if normalized.Version != "0.0.1" {
		t.Fatalf("Version = %q", normalized.Version)
	}
	if normalized.Caller.CallerName != "bridge" || normalized.Caller.RequestID != "bridge_req_001" {
		t.Fatalf("Caller = %#v", normalized.Caller)
	}
	if normalized.Receiver.ID != "group_xxx" || normalized.Receiver.Type != "group" {
		t.Fatalf("Receiver = %#v", normalized.Receiver)
	}
	if normalized.Timestamp.UnixMilli() != 1780550400000 {
		t.Fatalf("Timestamp = %v", normalized.Timestamp)
	}
	submit, ok := normalized.Submit.(map[string]any)
	if !ok || submit["eventKey"] != "submit" {
		t.Fatalf("Submit = %#v", normalized.Submit)
	}
	if normalized.Payload == nil {
		t.Fatal("Payload = nil")
	}
	if normalized.Payload["MsgId"] != "task_xxx" {
		t.Fatalf("Payload MsgId = %#v", normalized.Payload["MsgId"])
	}
}

func TestInteractionSubmitGroupOptional(t *testing.T) {
	payload := `{"EventType":"TRIGGER","MsgId":"m1","Time":100,"taskContext":{"subscriptionType":"infoflow.interaction.submit","cardInstanceId":"c1","userId":"","submit":{}}}`
	var captured events.Event
	dispatcher := events.NewDispatcher(events.Options{})
	if err := dispatcher.On("event.interaction_submit", func(_ context.Context, event events.Event) error {
		captured = event
		return nil
	}); err != nil {
		t.Fatalf("On() error = %v", err)
	}
	if err := dispatcher.DispatchFrame(context.Background(), []byte(payload)); err != nil {
		t.Fatalf("DispatchFrame() error = %v", err)
	}
	normalized, err := events.NormalizeInteractionSubmitEvent(captured)
	if err != nil {
		t.Fatalf("NormalizeInteractionSubmitEvent() error = %v", err)
	}
	if normalized.GroupID != "" || normalized.CardID != "" {
		t.Fatalf("expected optional fields empty, got groupId=%q cardId=%q", normalized.GroupID, normalized.CardID)
	}
	if normalized.UserID != "" {
		t.Fatalf("UserID = %q, want empty retained", normalized.UserID)
	}
}

func TestInteractionSubmitRejectsNonSubmit(t *testing.T) {
	cases := []string{
		`{"EventType":"TRIGGER","taskContext":{"subscriptionType":"infoflow.other"}}`,
		`{"EventType":"MESSAGE_RECEIVE","taskContext":{"subscriptionType":"infoflow.interaction.submit"}}`,
		`{"EventType":"TRIGGER"}`,
	}
	for _, raw := range cases {
		var captured events.Event
		dispatcher := events.NewDispatcher(events.Options{})
		_ = dispatcher.On("*", func(_ context.Context, event events.Event) error {
			captured = event
			return nil
		})
		if err := dispatcher.DispatchFrame(context.Background(), []byte(raw)); err != nil {
			t.Fatalf("DispatchFrame(%s) error = %v", raw, err)
		}
		_, err := events.NormalizeInteractionSubmitEvent(captured)
		if err == nil {
			t.Fatalf("NormalizeInteractionSubmitEvent(%s) = nil error, want rejection", raw)
		}
		var parseErr *events.ParseError
		if errors.As(err, &parseErr) {
			t.Fatalf("NormalizeInteractionSubmitEvent(%s) returned ParseError, want semantic error", raw)
		}
	}
}

func TestInteractionSubmitOnHandler(t *testing.T) {
	var got *events.InteractionSubmitEvent
	dispatcher := events.NewDispatcher(events.Options{})
	cleanup, err := dispatcher.OnInteractionSubmit(func(_ context.Context, event events.InteractionSubmitEvent) error {
		eventCopy := event
		got = &eventCopy
		return nil
	})
	if err != nil {
		t.Fatalf("OnInteractionSubmit() error = %v", err)
	}
	defer cleanup()
	if err := dispatcher.DispatchFrame(context.Background(), []byte(interactionSubmitSample)); err != nil {
		t.Fatalf("DispatchFrame() error = %v", err)
	}
	if got == nil || got.CardInstanceID != "card_inst_01JZ8Q3Z7VQ4YQ6J4N8B2D9K1A" {
		t.Fatalf("got = %#v", got)
	}
}

func TestInteractionSubmitDoesNotRouteAsText(t *testing.T) {
	called := make([]string, 0)
	dispatcher := events.NewDispatcher(events.Options{})
	for _, route := range []string{"private.text", "text", "group.text", "trigger.*", "event.interaction_submit"} {
		route := route
		if err := dispatcher.On(route, func(_ context.Context, _ events.Event) error {
			called = append(called, route)
			return nil
		}); err != nil {
			t.Fatalf("On(%q) error = %v", route, err)
		}
	}
	if err := dispatcher.DispatchFrame(context.Background(), []byte(interactionSubmitSample)); err != nil {
		t.Fatalf("DispatchFrame() error = %v", err)
	}
	if strings.Join(called, ",") != "event.interaction_submit" {
		t.Fatalf("called = %v, want only event.interaction_submit", called)
	}
}
