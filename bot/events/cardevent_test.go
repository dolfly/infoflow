package events_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/dolfly/infoflow/bot/dynamiccard"
	"github.com/dolfly/infoflow/bot/events"
	"github.com/dolfly/infoflow/bot/internal/realtime"
)

func cardEventPayload(eventID string, groupID string) map[string]any {
	payload := map[string]any{
		"EventType":   "INTERACTIVE_MSG_CARD_EVENT",
		"DeliverType": "EVENT",
		"EventId":     eventID,
		"LogId":       "log_0001",
		"CreateTime":  "1792744774000",
		"EventData": map[string]any{
			"card_id":          "card_evt_001",
			"current_sequence": 3,
			"msg_sbasemsgid":   "1879549251012710400",
			"chat_id":          "7001",
			"from_id":          "9001",
			"element_id":       "ok",
			"action": map[string]any{
				"type":  "data_submit",
				"value": "ok",
			},
		},
	}
	if groupID != "" {
		payload["groupId"] = groupID
	}
	return payload
}

func TestDispatchPayloadRoutesCardEventBeforeGroupBranch(t *testing.T) {
	dispatcher := events.NewDispatcher(events.Options{})
	var cardCalls int32
	var groupCalls int32
	var wildcardCalls int32
	cleanup, err := dispatcher.OnCardEvent(func(_ context.Context, event dynamiccard.CardEvent) error {
		atomic.AddInt32(&cardCalls, 1)
		if event.CardID != "card_evt_001" || event.EventID != "evt_0001" {
			t.Errorf("event = %+v", event)
		}
		if event.MsgBaseMsgID != "1879549251012710400" {
			t.Errorf("msg base msg id = %q", event.MsgBaseMsgID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("OnCardEvent: %v", err)
	}
	defer cleanup()
	if err := dispatcher.On("group.*", func(context.Context, events.Event) error {
		atomic.AddInt32(&groupCalls, 1)
		return nil
	}); err != nil {
		t.Fatalf("On group: %v", err)
	}
	if err := dispatcher.On("*", func(context.Context, events.Event) error {
		atomic.AddInt32(&wildcardCalls, 1)
		return nil
	}); err != nil {
		t.Fatalf("On wildcard: %v", err)
	}

	payload := cardEventPayload("evt_0001", "group_1")
	if err := dispatcher.DispatchPayload(context.Background(), payload, nil); err != nil {
		t.Fatalf("DispatchPayload: %v", err)
	}
	if calls := atomic.LoadInt32(&cardCalls); calls != 1 {
		t.Fatalf("card calls = %d", calls)
	}
	if calls := atomic.LoadInt32(&groupCalls); calls != 0 {
		t.Fatalf("group calls = %d, card event must not be rerouted", calls)
	}
	if calls := atomic.LoadInt32(&wildcardCalls); calls != 1 {
		t.Fatalf("wildcard calls = %d", calls)
	}
}

func TestDispatchPayloadLowercaseCardTypeStaysOutOfCardRoute(t *testing.T) {
	dispatcher := events.NewDispatcher(events.Options{})
	var cardCalls int32
	cleanup, err := dispatcher.OnCardEvent(func(context.Context, dynamiccard.CardEvent) error {
		atomic.AddInt32(&cardCalls, 1)
		return nil
	})
	if err != nil {
		t.Fatalf("OnCardEvent: %v", err)
	}
	defer cleanup()

	payload := cardEventPayload("evt_0002", "group_1")
	payload["EventType"] = "interactive_msg_card_event"
	if err := dispatcher.DispatchPayload(context.Background(), payload, nil); err != nil {
		t.Fatalf("DispatchPayload: %v", err)
	}
	if calls := atomic.LoadInt32(&cardCalls); calls != 0 {
		t.Fatalf("card calls = %d, lowercase type must not match", calls)
	}
}

func TestOnCardEventReportsNormalizationFailure(t *testing.T) {
	var reported int32
	dispatcher := events.NewDispatcher(events.Options{
		OnError: func(realtime.RuntimeError) { atomic.AddInt32(&reported, 1) },
	})
	var cardCalls int32
	cleanup, err := dispatcher.OnCardEvent(func(context.Context, dynamiccard.CardEvent) error {
		atomic.AddInt32(&cardCalls, 1)
		return nil
	})
	if err != nil {
		t.Fatalf("OnCardEvent: %v", err)
	}
	defer cleanup()

	payload := cardEventPayload("evt_0003", "")
	delete(payload["EventData"].(map[string]any), "card_id")
	if err := dispatcher.DispatchPayload(context.Background(), payload, nil); err != nil {
		t.Fatalf("DispatchPayload: %v", err)
	}
	if calls := atomic.LoadInt32(&cardCalls); calls != 0 {
		t.Fatalf("card calls = %d", calls)
	}
	if calls := atomic.LoadInt32(&reported); calls != 1 {
		t.Fatalf("reported errors = %d", calls)
	}
}

func TestOnCardEventRejectsNilHandler(t *testing.T) {
	dispatcher := events.NewDispatcher(events.Options{})
	if _, err := dispatcher.OnCardEvent(nil); err == nil {
		t.Fatal("expected an error for a nil handler")
	}
}
