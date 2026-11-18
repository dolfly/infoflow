package dynamiccard

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	dcinternal "github.com/dolfly/infoflow/internal/dynamiccard"
)

// AckMode is the card-event ACK processing mode.
type AckMode string

const (
	// AckModeSync acknowledges after the business logic already ran. It may
	// carry a toast and an optional card_update.
	AckModeSync AckMode = "sync"
	// AckModeAsync acknowledges that the event was accepted so the business
	// logic may run afterwards. It must use code 0 and must not carry a
	// card_update.
	AckModeAsync AckMode = "async"
)

// Toast levels accepted by the ACK payload. An empty level is omitted and the
// platform defaults it to info.
const (
	AckToastLevelSuccess = "success"
	AckToastLevelInfo    = "info"
	AckToastLevelWarning = "warning"
	AckToastLevelError   = "error"
)

const (
	// AckWindowMs is the 4 second ACK window measured from event receipt.
	AckWindowMs = 4000
	// AckEventTTL is the platform's event retention for ACK; a later ACK is
	// rejected locally with ack_expired.
	AckEventTTL = 10 * time.Minute

	// WarningAckWindowExceeded reports that a sync ACK was sent more than 4
	// seconds after event receipt, so the platform applies card_update without
	// showing the toast.
	WarningAckWindowExceeded = "ack_window_exceeded_toast_suppressed"

	ackAlreadyProcessed = "ack already processed"
)

// AckToast is the short user-facing acknowledgement text. Content is required
// when a toast is present and must stay short; it must not embed unescaped user
// input or sensitive data.
type AckToast struct {
	Level   string `json:"level,omitempty"`
	Content string `json:"content"`
}

// CardUpdate is the optional result-state update carried by a sync ACK. It has
// the shape of an update request without card_id or sequence: full requires a
// scheme "1" CardContent and no Updates; element requires complete components
// with type and id, unique ids and no ancestor/descendant pair.
//
// The fields intentionally carry no JSON tags: only MarshalJSON turns them into
// the wire object, so a card_update can never grow a card_id or sequence field.
type CardUpdate struct {
	UpdateType  UpdateType
	CardContent *DynamicCardContent
	Updates     ComponentList
}

type cardUpdatePayload struct {
	UpdateType  UpdateType          `json:"update_type"`
	CardContent *DynamicCardContent `json:"card_content,omitempty"`
	Updates     ComponentList       `json:"updates,omitempty"`
}

// MarshalJSON emits update_type plus exactly one of card_content or updates and
// can never emit card_id or sequence.
func (u CardUpdate) MarshalJSON() ([]byte, error) {
	return json.Marshal(cardUpdatePayload{
		UpdateType:  u.UpdateType,
		CardContent: u.CardContent,
		Updates:     u.Updates,
	})
}

// AckData is the ack_data payload. Code 0 means success; a non-zero code must be
// sync, must carry a toast and must not carry CardUpdate. Async likewise never
// accepts CardUpdate.
type AckData struct {
	Code       int64
	Mode       AckMode
	Toast      *AckToast
	CardUpdate *CardUpdate
}

// AckRequest is the explicit ACK input. ReceivedAt is the event receipt time
// used for the 4 second / 10 minute windows; zero means "just now". StrictWindow
// additionally rejects an async ACK older than 4 seconds locally instead of
// letting the platform answer 1011.
type AckRequest struct {
	CardID       string
	EventID      string
	AckData      AckData
	ReceivedAt   time.Time
	StrictWindow bool
}

// AckResult is the normalized ACK response. Accepted mirrors code 0;
// AlreadyProcessed marks the platform's idempotent "ack already processed"
// success; ProceedWithBusiness is true only for an accepted async ACK. On a
// failure the result is still populated (accepted=false, message, mode, ageMs,
// raw) and returned together with the error.
type AckResult struct {
	Accepted            bool
	AlreadyProcessed    bool
	CardID              string
	EventID             string
	Sequence            *int64
	Message             string
	Mode                AckMode
	AgeMs               int64
	ProceedWithBusiness bool
	Warnings            []string
	Raw                 json.RawMessage
}

// RawJSON returns the original response, defensively copied.
func (r AckResult) RawJSON() json.RawMessage {
	return json.RawMessage(append([]byte(nil), r.Raw...))
}

type ackPayload struct {
	CardID  string         `json:"card_id"`
	EventID string         `json:"event_id"`
	AckData ackDataPayload `json:"ack_data"`
}

type ackDataPayload struct {
	Code       int64       `json:"code"`
	Mode       AckMode     `json:"mode"`
	Toast      *AckToast   `json:"toast,omitempty"`
	CardUpdate *CardUpdate `json:"card_update,omitempty"`
}

// Ack acknowledges one interactive card event through
// POST /interactive/message/card_event_ack. It is an independent API call, not
// a webhook response, and never uses an event-center ACK token. The local
// validation matrix rejects invalid input without a request; a 1010 response is
// returned as stream_state without current_* fields, 1011 as ack_expired and an
// already-processed event as a successful result. ACKs are never retried.
func (s Service) Ack(ctx context.Context, request AckRequest) (AckResult, error) {
	result := AckResult{
		CardID:  request.CardID,
		EventID: request.EventID,
		Mode:    request.AckData.Mode,
	}
	if !request.ReceivedAt.IsZero() {
		if age := time.Since(request.ReceivedAt).Milliseconds(); age > 0 {
			result.AgeMs = age
		}
	}
	if ctx == nil {
		return result, newValidationError("context", "must not be nil")
	}
	payload, warnings, err := buildAckPayload(request, result.AgeMs)
	if err != nil {
		return result, err
	}
	result.Warnings = warnings
	response, err := s.post(ctx, dcinternal.RouteCardEventAck, payload, OperationAck)
	if err != nil {
		if failure, ok := err.(*DynamicCardError); ok {
			failure.CardID = request.CardID
			failure.EventID = request.EventID
			if failure.Operation == "" {
				failure.Operation = OperationAck
			}
		}
		return ackFailureResult(result, response, err), err
	}
	parsed, err := parseAckResult(response, result)
	if err != nil {
		return parsed, err
	}
	return parsed, nil
}

// AckEvent is the CardEvent form of Ack. It reads only CardID and EventID from
// the normalized event; it never derives a card_update from CurrentContent.
// Use Ack with AckRequest when the event receipt time or StrictWindow matters.
func (s Service) AckEvent(ctx context.Context, event CardEvent, ackData AckData) (AckResult, error) {
	return s.Ack(ctx, AckRequest{
		CardID:  event.CardID,
		EventID: event.EventID,
		AckData: ackData,
	})
}

func buildAckPayload(request AckRequest, ageMs int64) (ackPayload, []string, error) {
	if strings.TrimSpace(request.CardID) == "" {
		return ackPayload{}, nil, newValidationError("cardId", "must not be blank")
	}
	if strings.TrimSpace(request.EventID) == "" {
		return ackPayload{}, nil, newValidationError("eventId", "must not be blank")
	}
	data := request.AckData
	switch data.Mode {
	case AckModeSync, AckModeAsync:
	default:
		return ackPayload{}, nil, newValidationError("ackData.mode", `must be "sync" or "async"`)
	}
	if data.Code != 0 && data.Mode == AckModeAsync {
		return ackPayload{}, nil, newValidationError("ackData.code", "a non-zero code requires sync mode")
	}
	if data.Code != 0 && data.Toast == nil {
		return ackPayload{}, nil, newValidationError("ackData.toast", "is required when code is not 0")
	}
	if data.Code != 0 && data.CardUpdate != nil {
		return ackPayload{}, nil, newValidationError("ackData.cardUpdate", "is not allowed when code is not 0")
	}
	if data.Mode == AckModeAsync && data.CardUpdate != nil {
		return ackPayload{}, nil, newValidationError("ackData.cardUpdate", "is not allowed in async mode")
	}
	if data.Toast != nil {
		switch data.Toast.Level {
		case "", AckToastLevelSuccess, AckToastLevelInfo, AckToastLevelWarning, AckToastLevelError:
		default:
			return ackPayload{}, nil, newValidationError("ackData.toast.level",
				`must be "success", "info", "warning" or "error"`)
		}
		if strings.TrimSpace(data.Toast.Content) == "" {
			return ackPayload{}, nil, newValidationError("ackData.toast.content", "must not be blank")
		}
	}
	if data.CardUpdate != nil {
		if err := validateCardUpdate(*data.CardUpdate); err != nil {
			return ackPayload{}, nil, err
		}
	}
	if ageMs > AckEventTTL.Milliseconds() {
		return ackPayload{}, nil, &DynamicCardError{
			Category:  CategoryAckExpired,
			Message:   "card event ack window closed: the event is older than 10 minutes",
			Operation: OperationAck,
			CardID:    request.CardID,
			EventID:   request.EventID,
		}
	}
	if data.Mode == AckModeAsync && request.StrictWindow && ageMs > AckWindowMs {
		return ackPayload{}, nil, &DynamicCardError{
			Category:  CategoryAckExpired,
			Message:   "card event ack window closed: async ack is later than 4 seconds (strictWindow)",
			Operation: OperationAck,
			CardID:    request.CardID,
			EventID:   request.EventID,
		}
	}

	var warnings []string
	if data.Mode == AckModeSync && ageMs > AckWindowMs {
		warnings = append(warnings, WarningAckWindowExceeded)
	}
	payload := ackPayload{
		CardID:  request.CardID,
		EventID: request.EventID,
		AckData: ackDataPayload{
			Code:       data.Code,
			Mode:       data.Mode,
			Toast:      data.Toast,
			CardUpdate: data.CardUpdate,
		},
	}
	return payload, warnings, nil
}

func validateCardUpdate(update CardUpdate) error {
	switch update.UpdateType {
	case UpdateTypeFull:
		if update.CardContent == nil {
			return newValidationError("ackData.cardUpdate.cardContent",
				"is required for a full update")
		}
		if len(update.Updates) != 0 {
			return newValidationError("ackData.cardUpdate.updates",
				"must be empty for a full update")
		}
		if update.CardContent.Scheme != "" && update.CardContent.Scheme != SchemeV1 {
			return newValidationError("ackData.cardUpdate.cardContent.scheme", `must be "1"`)
		}
		if len(update.CardContent.Body.Children) == 0 {
			return newValidationError("ackData.cardUpdate.cardContent.body.children", "must not be empty")
		}
		content := DynamicCardContent{Scheme: SchemeV1, Body: update.CardContent.Body, Footer: update.CardContent.Footer}
		if _, err := ValidateContent(&content); err != nil {
			return err
		}
	case UpdateTypeElement:
		if update.CardContent != nil {
			return newValidationError("ackData.cardUpdate.cardContent",
				"must be empty for an element update")
		}
		if len(update.Updates) == 0 {
			return newValidationError("ackData.cardUpdate.updates",
				"must contain at least one component")
		}
		if _, err := elementUpdateObjectsAt("ackData.cardUpdate.updates", update.Updates); err != nil {
			return err
		}
	default:
		return newValidationError("ackData.cardUpdate.updateType", `must be "full" or "element"`)
	}

	raw, err := json.Marshal(update)
	if err != nil {
		return newValidationError("ackData.cardUpdate", "cannot be encoded: "+err.Error())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return newValidationError("ackData.cardUpdate", "cannot be encoded: "+err.Error())
	}
	if _, exists := fields["card_id"]; exists {
		return newValidationError("ackData.cardUpdate.cardId", "must not be set")
	}
	if _, exists := fields["sequence"]; exists {
		return newValidationError("ackData.cardUpdate.sequence", "must not be set")
	}
	return nil
}

func parseAckResult(response CardResponse, base AckResult) (AckResult, error) {
	result := base
	result.Raw = response.RawJSON()
	if len(response.Payload) == 0 {
		return result, missingResponseField("business payload", OperationAck, response)
	}
	var payload struct {
		CardID   json.RawMessage `json:"card_id"`
		EventID  json.RawMessage `json:"event_id"`
		Sequence json.RawMessage `json:"sequence"`
		Message  json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(response.Payload, &payload); err != nil {
		return result, missingResponseField("business data", OperationAck, response)
	}
	if value := scalarJSONValue(payload.CardID); value != "" {
		result.CardID = value
	}
	if value := scalarJSONValue(payload.EventID); value != "" {
		result.EventID = value
	}
	result.Message = response.Message
	if value := scalarJSONValue(payload.Message); value != "" {
		result.Message = value
	}
	sequence, present, err := parseSequence(payload.Sequence)
	if err != nil {
		return result, &DynamicCardError{
			Category:   CategoryServer,
			Message:    "response sequence is invalid",
			HTTPStatus: response.HTTPStatus,
			Operation:  OperationAck,
			CardID:     result.CardID,
			EventID:    result.EventID,
			LogID:      response.LogID,
			Raw:        response.RawJSON(),
		}
	}
	if present {
		result.Sequence = &sequence
	}
	result.Accepted = true
	result.AlreadyProcessed = result.Message == ackAlreadyProcessed
	if result.Mode == AckModeAsync {
		result.ProceedWithBusiness = true
	}
	return result, nil
}

func ackFailureResult(base AckResult, response CardResponse, err error) AckResult {
	result := base
	result.Accepted = false
	result.AlreadyProcessed = false
	result.ProceedWithBusiness = false
	result.Raw = response.RawJSON()
	if failure, ok := err.(*DynamicCardError); ok {
		if len(result.Raw) == 0 {
			result.Raw = failure.RawJSON()
		}
		if failure.Message != "" {
			result.Message = failure.Message
		}
	}
	return result
}
