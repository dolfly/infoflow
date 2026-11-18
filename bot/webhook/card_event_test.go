package webhook_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dolfly/infoflow/bot/dynamiccard"
	"github.com/dolfly/infoflow/bot/events"
	"github.com/dolfly/infoflow/bot/internal/realtime"
	"github.com/dolfly/infoflow/bot/webhook"
)

var cardEventKey = []byte("0123456789abcdef")

const cardEventPlaintext = `{
  "EventType": "INTERACTIVE_MSG_CARD_EVENT",
  "DeliverType": "EVENT",
  "EventId": "evt_0001",
  "LogId": "log_0001",
  "CreateTime": "1792744774000",
  "EventData": {
    "card_id": "card_evt_001",
    "current_sequence": 3,
    "current_msg_content": {"scheme": "1", "body": {"children": []}},
    "msg_sbasemsgid": 1879549251012710400,
    "msg_msgid2": 123456789,
    "session_id": "sess_001",
    "chat_id": 7001,
    "chat_type": 2,
    "from_id": 9001,
    "from_user_id": "user_1001",
    "from_user_name": "张三",
    "element_id": "ok",
    "action": {
      "type": "data_submit",
      "value": "ok",
      "extra": {"client_context": [{"remark": "补充说明"}]}
    }
  }
}`

func cardEventForm(t *testing.T, plaintext string) url.Values {
	t.Helper()
	encrypted := encryptECB(t, cardEventKey, []byte(plaintext))
	envelope, err := json.Marshal(map[string]string{"Encrypt": encrypted})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return url.Values{"messageJson": {string(envelope)}}
}

func newCardEventReceiver(t *testing.T, now func() time.Time, onError realtime.ErrorHandler) (*webhook.Receiver, *events.Dispatcher) {
	t.Helper()
	dispatcher := events.NewDispatcher(events.Options{OnError: onError})
	receiver, err := webhook.NewReceiver(webhook.Options{
		Dispatcher:     dispatcher,
		EncodingAESKey: encodeKey(cardEventKey),
		Now:            now,
		OnError:        onError,
	})
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	return receiver, dispatcher
}

func receiveCardEvent(t *testing.T, channel <-chan dynamiccard.CardEvent, timeout time.Duration) dynamiccard.CardEvent {
	t.Helper()
	select {
	case event := <-channel:
		return event
	case <-time.After(timeout):
		t.Fatalf("card event was not dispatched within %s", timeout)
		return dynamiccard.CardEvent{}
	}
}

func TestReceiverEncryptedCardEventIsNormalized(t *testing.T) {
	receiver, dispatcher := newCardEventReceiver(t, time.Now, nil)
	dispatched := make(chan dynamiccard.CardEvent, 4)
	cleanup, err := dispatcher.OnCardEvent(func(_ context.Context, event dynamiccard.CardEvent) error {
		dispatched <- event
		return nil
	})
	if err != nil {
		t.Fatalf("OnCardEvent: %v", err)
	}
	defer cleanup()

	response := receiver.Handle(context.Background(), formRequest(cardEventForm(t, cardEventPlaintext)))
	if response.Status != http.StatusOK || response.Body != "success" {
		t.Fatalf("response = %#v", response)
	}
	event := receiveCardEvent(t, dispatched, 2*time.Second)
	if event.CardID != "card_evt_001" || event.EventID != "evt_0001" {
		t.Fatalf("event = %+v", event)
	}
	if event.MsgBaseMsgID != "1879549251012710400" {
		t.Fatalf("msg base msg id = %q", event.MsgBaseMsgID)
	}
	if event.ChatID != "7001" || event.FromID != "9001" || event.ElementID != "ok" {
		t.Fatalf("event ids = %+v", event)
	}
	if event.ActionType != "data_submit" || event.ActionValue != "ok" {
		t.Fatalf("action = %+v", event)
	}
	if len(event.ClientContext) != 1 || event.ClientContext[0]["remark"].Text != "补充说明" {
		t.Fatalf("client context = %+v", event.ClientContext)
	}
}

func TestReceiverCardEventReturnsBeforeHandlerCompletes(t *testing.T) {
	receiver, dispatcher := newCardEventReceiver(t, time.Now, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	cleanup, err := dispatcher.OnCardEvent(func(context.Context, dynamiccard.CardEvent) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("OnCardEvent: %v", err)
	}
	defer cleanup()

	responseChannel := make(chan webhook.Response, 1)
	go func() {
		responseChannel <- receiver.Handle(context.Background(), formRequest(cardEventForm(t, cardEventPlaintext)))
	}()
	select {
	case response := <-responseChannel:
		if response.Status != http.StatusOK || response.Body != "success" {
			close(release)
			t.Fatalf("response = %#v", response)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Handle blocked on the business handler")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("handler did not start")
	}
	close(release)
}

func TestReceiverCardEventDedupeUsesTenMinuteWindow(t *testing.T) {
	var current int64 = 1_800_000_000_000
	now := func() time.Time { return time.UnixMilli(atomic.LoadInt64(&current)) }
	receiver, dispatcher := newCardEventReceiver(t, now, nil)
	dispatched := make(chan struct{}, 4)
	cleanup, err := dispatcher.OnCardEvent(func(context.Context, dynamiccard.CardEvent) error {
		dispatched <- struct{}{}
		return nil
	})
	if err != nil {
		t.Fatalf("OnCardEvent: %v", err)
	}
	defer cleanup()

	form := cardEventForm(t, cardEventPlaintext)
	if response := receiver.Handle(context.Background(), formRequest(form)); response.Status != http.StatusOK {
		t.Fatalf("first response = %#v", response)
	}
	select {
	case <-dispatched:
	case <-time.After(2 * time.Second):
		t.Fatal("first event was not dispatched")
	}

	if response := receiver.Handle(context.Background(), formRequest(form)); response.Status != http.StatusOK || response.Body != "success" {
		t.Fatalf("duplicate response = %#v", response)
	}
	select {
	case <-dispatched:
		t.Fatal("duplicate event was dispatched")
	case <-time.After(200 * time.Millisecond):
	}

	atomic.StoreInt64(&current, current+11*60*1000)
	if response := receiver.Handle(context.Background(), formRequest(form)); response.Status != http.StatusOK {
		t.Fatalf("expired response = %#v", response)
	}
	select {
	case <-dispatched:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not dispatched after the dedupe window")
	}
}

func TestReceiverCardEventXMLMessageFallback(t *testing.T) {
	receiver, dispatcher := newCardEventReceiver(t, time.Now, nil)
	dispatched := make(chan dynamiccard.CardEvent, 1)
	cleanup, err := dispatcher.OnCardEvent(func(_ context.Context, event dynamiccard.CardEvent) error {
		dispatched <- event
		return nil
	})
	if err != nil {
		t.Fatalf("OnCardEvent: %v", err)
	}
	defer cleanup()

	encrypted := encryptECB(t, cardEventKey, []byte(cardEventPlaintext))
	message := fmt.Sprintf("<xml><Encrypt><![CDATA[%s]]></Encrypt></xml>", encrypted)
	response := receiver.Handle(context.Background(), formRequest(url.Values{"message": {message}}))
	if response.Status != http.StatusOK || response.Body != "success" {
		t.Fatalf("response = %#v", response)
	}
	event := receiveCardEvent(t, dispatched, 2*time.Second)
	if event.CardID != "card_evt_001" || event.MsgBaseMsgID != "1879549251012710400" {
		t.Fatalf("event = %+v", event)
	}
}

func TestReceiverMessageJsonWinsOverXMLMessage(t *testing.T) {
	receiver, dispatcher := newCardEventReceiver(t, time.Now, nil)
	dispatched := make(chan dynamiccard.CardEvent, 1)
	cleanup, err := dispatcher.OnCardEvent(func(_ context.Context, event dynamiccard.CardEvent) error {
		dispatched <- event
		return nil
	})
	if err != nil {
		t.Fatalf("OnCardEvent: %v", err)
	}
	defer cleanup()

	plaintext := cardEventPlaintext
	encrypted := encryptECB(t, cardEventKey, []byte(plaintext))
	envelope, err := json.Marshal(map[string]string{"Encrypt": encrypted})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	badXML := "<xml><Encrypt><![CDATA[not-the-json-envelope]]></Encrypt></xml>"
	response := receiver.Handle(context.Background(), formRequest(url.Values{
		"messageJson": {string(envelope)},
		"message":     {badXML},
	}))
	if response.Status != http.StatusOK {
		t.Fatalf("response = %#v", response)
	}
	receiveCardEvent(t, dispatched, 2*time.Second)
}

func TestReceiverInvalidCardEventIsDiscarded(t *testing.T) {
	cases := []struct {
		name      string
		plaintext string
	}{
		{"missing card id", `{"EventType":"INTERACTIVE_MSG_CARD_EVENT","EventId":"evt_0001","EventData":{}}`},
		{"missing event data", `{"EventType":"INTERACTIVE_MSG_CARD_EVENT","EventId":"evt_0001"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var reported int32
			receiver, dispatcher := newCardEventReceiver(t, time.Now, func(realtime.RuntimeError) {
				atomic.AddInt32(&reported, 1)
			})
			dispatched := make(chan struct{}, 1)
			cleanup, err := dispatcher.OnCardEvent(func(context.Context, dynamiccard.CardEvent) error {
				dispatched <- struct{}{}
				return nil
			})
			if err != nil {
				t.Fatalf("OnCardEvent: %v", err)
			}
			defer cleanup()

			response := receiver.Handle(context.Background(), formRequest(cardEventForm(t, testCase.plaintext)))
			if response.Status != http.StatusOK || response.Body != "success" {
				t.Fatalf("response = %#v", response)
			}
			select {
			case <-dispatched:
				t.Fatal("invalid event was dispatched")
			case <-time.After(200 * time.Millisecond):
			}
			if calls := atomic.LoadInt32(&reported); calls == 0 {
				t.Fatal("invalid event was not reported")
			}
		})
	}
}

func TestReceiverCardEventHandlerPanicKeepsSuccess(t *testing.T) {
	receiver, dispatcher := newCardEventReceiver(t, time.Now, nil)
	cleanup, err := dispatcher.OnCardEvent(func(context.Context, dynamiccard.CardEvent) error {
		panic("boom")
	})
	if err != nil {
		t.Fatalf("OnCardEvent: %v", err)
	}
	defer cleanup()

	response := receiver.Handle(context.Background(), formRequest(cardEventForm(t, cardEventPlaintext)))
	if response.Status != http.StatusOK || response.Body != "success" {
		t.Fatalf("response = %#v", response)
	}
	time.Sleep(100 * time.Millisecond)
}
