package dynamiccard

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const cardEventFrame = `{
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
      "extra": {"client_context": [{"remark": "补充说明"}, {"tags": ["fast", "stable"]}]}
    }
  }
}`

func decodeCardEventFrame(t *testing.T) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(cardEventFrame))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	return payload
}

func TestNormalizeCardEvent(t *testing.T) {
	payload := decodeCardEventFrame(t)
	event, err := NormalizeCardEvent(payload, []byte(cardEventFrame))
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if event.EventType != CardEventType || event.EventID != "evt_0001" || event.LogID != "log_0001" {
		t.Fatalf("outer frame = %+v", event)
	}
	if event.DeliverType != "EVENT" || event.CreateTime != "1792744774000" {
		t.Fatalf("outer frame = %+v", event)
	}
	if event.CardID != "card_evt_001" || event.CurrentSequence != 3 {
		t.Fatalf("event data = %+v", event)
	}
	if event.CurrentContent == nil || event.CurrentContent.Scheme != SchemeV1 {
		t.Fatalf("current content = %+v", event.CurrentContent)
	}
	if event.MsgBaseMsgID != "1879549251012710400" {
		t.Fatalf("msg base msg id = %q", event.MsgBaseMsgID)
	}
	if event.MsgMsgID2 != "123456789" || event.SessionID != "sess_001" {
		t.Fatalf("event data = %+v", event)
	}
	if event.ChatID != "7001" || event.ChatType != 2 || event.FromID != "9001" {
		t.Fatalf("chat/from = %+v", event)
	}
	if event.FromUserID != "user_1001" || event.FromUserName != "张三" || event.ElementID != "ok" {
		t.Fatalf("user/element = %+v", event)
	}
	if event.ActionType != "data_submit" || event.ActionValue != "ok" {
		t.Fatalf("action = %+v", event)
	}
	if len(event.ClientContext) != 2 {
		t.Fatalf("client context = %+v", event.ClientContext)
	}
	if event.ClientContext[0]["remark"].Text != "补充说明" {
		t.Fatalf("string client context = %+v", event.ClientContext[0])
	}
	tags := event.ClientContext[1]["tags"]
	if tags.Text != "" || len(tags.Values) != 2 || tags.Values[0] != "fast" || tags.Values[1] != "stable" {
		t.Fatalf("array client context = %+v", tags)
	}
	if !bytes.Equal(event.RawJSON(), []byte(cardEventFrame)) {
		t.Fatalf("raw frame changed")
	}
}

func TestNormalizeCardEventDefaults(t *testing.T) {
	payload := map[string]any{
		"EventType": CardEventType,
		"EventId":   "evt_min",
		"EventData": map[string]any{"card_id": "card_min"},
	}
	event, err := NormalizeCardEvent(payload, nil)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if event.ClientContext == nil || len(event.ClientContext) != 0 {
		t.Fatalf("client context = %+v, want empty", event.ClientContext)
	}
	if event.CurrentContent != nil {
		t.Fatalf("current content = %+v, want nil", event.CurrentContent)
	}
	if event.MsgBaseMsgID != "" || event.ChatID != "" || event.FromID != "" || event.ChatType != 0 {
		t.Fatalf("defaults = %+v", event)
	}
	if len(event.RawJSON()) == 0 {
		t.Fatal("raw frame must be retained when raw is nil")
	}
}

func TestNormalizeCardEventRejectsInvalidFrames(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		field   string
	}{
		{"nil payload", nil, ""},
		{"wrong type", map[string]any{"EventType": "MESSAGE_RECEIVE", "EventData": map[string]any{"card_id": "c"}}, "EventType"},
		{"lowercase type", map[string]any{"EventType": "interactive_msg_card_event", "EventData": map[string]any{"card_id": "c"}}, "EventType"},
		{"missing event data", map[string]any{"EventType": CardEventType}, "EventData"},
		{"event data not object", map[string]any{"EventType": CardEventType, "EventData": "x"}, "EventData"},
		{"missing card id", map[string]any{"EventType": CardEventType, "EventData": map[string]any{}}, "EventData.card_id"},
		{"blank card id", map[string]any{"EventType": CardEventType, "EventData": map[string]any{"card_id": "  "}}, "EventData.card_id"},
		{"bad current content", map[string]any{"EventType": CardEventType,
			"EventData": map[string]any{"card_id": "c", "current_msg_content": "x"}}, "EventData.current_msg_content"},
		{"bad client context", map[string]any{"EventType": CardEventType,
			"EventData": map[string]any{"card_id": "c", "action": map[string]any{
				"extra": map[string]any{"client_context": []any{map[string]any{"a": map[string]any{"b": 1}}}},
			}}}, "EventData.action.extra.client_context"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NormalizeCardEvent(testCase.payload, nil)
			var eventErr *CardEventError
			if !errors.As(err, &eventErr) {
				t.Fatalf("error = %v, want CardEventError", err)
			}
			if testCase.field != "" && eventErr.Field != testCase.field {
				t.Fatalf("field = %q, want %q", eventErr.Field, testCase.field)
			}
			if strings.Contains(err.Error(), "card_evt_001") {
				t.Fatalf("error leaked payload data: %v", err)
			}
		})
	}
}

// AB-05: a footer_operation vote keeps element_id, action.value and the original
// client_context object array, including the numeric state.
func TestNormalizeCardEventFooterOperation(t *testing.T) {
	frame := `{
	  "EventType": "INTERACTIVE_MSG_CARD_EVENT",
	  "EventId": "evt_footer_1",
	  "EventData": {
	    "card_id": "card_ops_1",
	    "element_id": "ai_ops",
	    "action": {
	      "type": "footer_operation",
	      "value": "vote_up",
	      "extra": {
	        "client_context": [
	          {"source_platform": "ios"},
	          {"state": 1},
	          {"biz_extra": "{\"biz_msg_id\":\"abc-123\"}"}
	        ]
	      }
	    }
	  }
	}`
	decoder := json.NewDecoder(strings.NewReader(frame))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	event, err := NormalizeCardEvent(payload, []byte(frame))
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if event.ActionType != CardEventActionFooterOperation {
		t.Fatalf("action type = %q", event.ActionType)
	}
	if event.ActionValue != "vote_up" {
		t.Fatalf("action value = %q", event.ActionValue)
	}
	if event.ElementID != "ai_ops" {
		t.Fatalf("element_id = %q", event.ElementID)
	}
	if event.EventID != "evt_footer_1" {
		t.Fatalf("event id = %q", event.EventID)
	}
	if len(event.ClientContext) != 3 {
		t.Fatalf("client context = %v", event.ClientContext)
	}
	if got := event.ClientContext[0]["source_platform"].Text; got != "ios" {
		t.Fatalf("source_platform = %q", got)
	}
	if got := event.ClientContext[1]["state"].Text; got != "1" {
		t.Fatalf("state = %q", got)
	}
	if got := string(event.ClientContext[1]["state"].Raw); got != "1" {
		t.Fatalf("state raw = %q", got)
	}
	if got := event.ClientContext[2]["biz_extra"].Text; got != `{"biz_msg_id":"abc-123"}` {
		t.Fatalf("biz_extra = %q", got)
	}
}

// A numeric client_context value round-trips through Raw without becoming a
// quoted string.
func TestClientContextValueKeepsNumericRaw(t *testing.T) {
	var value ClientContextValue
	if err := json.Unmarshal([]byte("1"), &value); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if value.Text != "1" || string(value.Raw) != "1" {
		t.Fatalf("value = %+v", value)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != "1" {
		t.Fatalf("encoded = %s, want number 1", encoded)
	}
}

func TestIsCardEventIsCaseSensitive(t *testing.T) {
	if !IsCardEvent(map[string]any{"EventType": CardEventType}) {
		t.Fatal("exact type must be a card event")
	}
	if IsCardEvent(map[string]any{"EventType": "interactive_msg_card_event"}) {
		t.Fatal("lowercase type must not match")
	}
	if IsCardEvent(map[string]any{"eventType": CardEventType}) {
		t.Fatal("only the exact EventType key is recognized by the helper")
	}
}

func TestClientContextValueRoundTrip(t *testing.T) {
	raw, err := json.Marshal(map[string]ClientContextValue{
		"text":  {Text: "x"},
		"array": {Values: []string{"a", "b"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"text":"x"`)) || !bytes.Contains(raw, []byte(`"array":["a","b"]`)) {
		t.Fatalf("encoded = %s", raw)
	}
}
