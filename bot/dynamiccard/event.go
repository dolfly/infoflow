package dynamiccard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Wire values of the interactive card event route.
const (
	// CardEventType is the exact outer EventType of an interactive card event.
	CardEventType = "INTERACTIVE_MSG_CARD_EVENT"
	// CardEventRoute is the dispatcher route of a normalized card event.
	CardEventRoute = "event.interactive_msg_card_event"
	// CardEventActionFooterOperation is the action.type of an action_bar vote
	// click. Its action.value is the clicked operation key (vote_up / vote_down)
	// and element_id is the action_bar id.
	CardEventActionFooterOperation = "footer_operation"
)

// ClientContextValue is one `client_context` value: a single JSON scalar, an
// array of strings, or any other scalar the platform may send. Text and Values
// expose the documented string/array forms; Raw always keeps the exact original
// JSON, so numeric fields such as a footer_operation `state` (1 selected / 0
// cancelled) are not lost.
type ClientContextValue struct {
	Text   string
	Values []string
	Raw    json.RawMessage
}

// UnmarshalJSON accepts a JSON string, an array of strings, or any other JSON
// scalar (number, boolean, null) and stores the exact original JSON in Raw.
// Objects and nested arrays are rejected because client_context values are
// scalars or string lists.
func (v *ClientContextValue) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("client context value must not be empty")
	}
	switch trimmed[0] {
	case '[':
		var values []string
		if err := json.Unmarshal(trimmed, &values); err != nil {
			return fmt.Errorf("client context value must be an array of strings")
		}
		v.Text = ""
		v.Values = values
	case '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return fmt.Errorf("client context value must be a string")
		}
		v.Text = text
		v.Values = nil
	case '{':
		return fmt.Errorf("client context value must be a string or a scalar")
	default:
		if !json.Valid(trimmed) {
			return fmt.Errorf("client context value must be valid JSON")
		}
		if bytes.Equal(trimmed, []byte("null")) {
			v.Text = ""
		} else {
			v.Text = string(trimmed)
		}
		v.Values = nil
	}
	v.Raw = append(json.RawMessage(nil), trimmed...)
	return nil
}

// MarshalJSON emits the original JSON when present, otherwise the array or
// string form. An empty value emits the empty string.
func (v ClientContextValue) MarshalJSON() ([]byte, error) {
	if len(v.Raw) > 0 {
		return append([]byte(nil), v.Raw...), nil
	}
	if v.Values != nil {
		return json.Marshal(v.Values)
	}
	return json.Marshal(v.Text)
}

// CardEvent is the normalized INTERACTIVE_MSG_CARD_EVENT payload. Outer frame
// fields are camelCase and EventData fields are snake_case on the wire; all
// identifiers are strings so a 19-digit id keeps every digit. Raw is the
// original frame and must never be logged in full because EventData may carry
// a large current_msg_content.
type CardEvent struct {
	EventType   string
	DeliverType string
	EventID     string
	LogID       string
	CreateTime  string

	CardID          string
	CurrentSequence int64
	CurrentContent  *DynamicCardContent
	MsgBaseMsgID    string
	MsgMsgID2       string
	SessionID       string
	ChatID          string
	ChatType        int64
	FromID          string
	FromUserID      string
	FromUserName    string
	ElementID       string
	// ActionType is the original action.type. Besides the documented submit
	// values it can be CardEventActionFooterOperation for an action_bar vote.
	ActionType  string
	ActionValue string
	// ClientContext preserves the original client_context object array. For a
	// footer_operation it carries source_platform, state and biz_extra; it is
	// never trimmed by the input/radio_group view rules.
	ClientContext []map[string]ClientContextValue

	Raw json.RawMessage
}

// RawJSON returns the original event frame, defensively copied. It can contain
// large content and must not be logged in full.
func (e CardEvent) RawJSON() json.RawMessage {
	return json.RawMessage(append([]byte(nil), e.Raw...))
}

// CardEventError reports an event frame that is not a valid interactive card
// event. It never carries the payload or any secret.
type CardEventError struct {
	Field  string
	Reason string
}

func (e *CardEventError) Error() string {
	if e.Field == "" {
		return "invalid card event: " + e.Reason
	}
	return "invalid card event: " + e.Field + ": " + e.Reason
}

// IsCardEvent reports whether payload has the exact outer card-event type.
// The comparison is case-sensitive by contract so other event kinds are never
// swallowed.
func IsCardEvent(payload map[string]any) bool {
	value, _ := payload["EventType"].(string)
	return value == CardEventType
}

// NormalizeCardEvent validates and normalizes one interactive card event
// frame. EventType must equal INTERACTIVE_MSG_CARD_EVENT exactly, EventData
// must be an object and card_id must be non-empty; every other field is
// optional. Numbers are converted to strings without losing digits, and
// client_context keeps its array shape. raw is stored defensively; when nil the
// frame is re-encoded.
func NormalizeCardEvent(payload map[string]any, raw []byte) (CardEvent, error) {
	if payload == nil {
		return CardEvent{}, &CardEventError{Reason: "payload is empty"}
	}
	eventType, _ := payload["EventType"].(string)
	if eventType != CardEventType {
		return CardEvent{}, &CardEventError{Field: "EventType", Reason: "must be " + CardEventType}
	}
	rawData, ok := payload["EventData"].(map[string]any)
	if !ok || rawData == nil {
		return CardEvent{}, &CardEventError{Field: "EventData", Reason: "must be an object"}
	}
	cardID, _ := rawData["card_id"].(string)
	if strings.TrimSpace(cardID) == "" {
		return CardEvent{}, &CardEventError{Field: "EventData.card_id", Reason: "must not be empty"}
	}

	event := CardEvent{
		EventType:       eventType,
		DeliverType:     eventString(payload, "DeliverType"),
		EventID:         eventString(payload, "EventId"),
		LogID:           eventString(payload, "LogId"),
		CreateTime:      eventString(payload, "CreateTime"),
		CardID:          cardID,
		CurrentSequence: eventInt64(rawData, "current_sequence"),
		MsgBaseMsgID:    eventScalarString(rawData, "msg_sbasemsgid"),
		MsgMsgID2:       eventScalarString(rawData, "msg_msgid2"),
		SessionID:       eventString(rawData, "session_id"),
		ChatID:          eventScalarString(rawData, "chat_id"),
		ChatType:        eventInt64(rawData, "chat_type"),
		FromID:          eventScalarString(rawData, "from_id"),
		FromUserID:      eventString(rawData, "from_user_id"),
		FromUserName:    eventString(rawData, "from_user_name"),
		ElementID:       eventString(rawData, "element_id"),
		ClientContext:   make([]map[string]ClientContextValue, 0),
	}
	if value, ok := rawData["current_msg_content"]; ok && value != nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			return CardEvent{}, &CardEventError{Field: "EventData.current_msg_content", Reason: "cannot be encoded"}
		}
		var content DynamicCardContent
		if err := json.Unmarshal(encoded, &content); err != nil {
			return CardEvent{}, &CardEventError{Field: "EventData.current_msg_content", Reason: "must be a card content object"}
		}
		event.CurrentContent = &content
	}
	if action, ok := rawData["action"].(map[string]any); ok && action != nil {
		event.ActionType = eventString(action, "type")
		event.ActionValue = eventScalarString(action, "value")
		if extra, ok := action["extra"].(map[string]any); ok && extra != nil {
			if context, ok := extra["client_context"]; ok && context != nil {
				encoded, err := json.Marshal(context)
				if err != nil {
					return CardEvent{}, &CardEventError{Field: "EventData.action.extra.client_context", Reason: "cannot be encoded"}
				}
				var clientContext []map[string]ClientContextValue
				if err := json.Unmarshal(encoded, &clientContext); err != nil {
					return CardEvent{}, &CardEventError{
						Field:  "EventData.action.extra.client_context",
						Reason: "must be an array of objects",
					}
				}
				if clientContext != nil {
					event.ClientContext = clientContext
				}
			}
		}
	}
	event.Raw = append(json.RawMessage(nil), raw...)
	if len(event.Raw) == 0 {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return CardEvent{}, &CardEventError{Reason: "payload cannot be encoded"}
		}
		event.Raw = encoded
	}
	return event, nil
}

func eventString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

// eventScalarString renders a scalar id as text. json.Number keeps the exact
// decimal digits, so a 19-digit id is never rounded.
func eventScalarString(payload map[string]any, key string) string {
	value, exists := payload[key]
	if !exists || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(typed, 10)
	case int:
		return strconv.Itoa(typed)
	default:
		return ""
	}
}

func eventInt64(payload map[string]any, key string) int64 {
	value, exists := payload[key]
	if !exists || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Int64()
		if err != nil {
			return 0
		}
		return number
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	case string:
		number, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0
		}
		return number
	default:
		return 0
	}
}
