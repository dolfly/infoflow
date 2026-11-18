package dynamiccard

import (
	"context"
	"encoding/json"
	"fmt"

	dcinternal "github.com/dolfly/infoflow/internal/dynamiccard"
)

// postTransport is the new-route transport required by the capability. The
// internal dynamic-card transport implements it; colocated tests substitute a
// fake so no real network is ever used.
type postTransport interface {
	PostJSON(ctx context.Context, route string, payload any) (dcinternal.Decision, error)
}

// Service is the AI dynamic-card (scheme=1) capability exposed as
// client.Bot.IM.DynamicCard. It reuses the qyopenapi base profile, app token,
// new-route headers, envelope decision and unified error model from the
// foundation layer. It never retries a send, update or card-event ACK: those
// operations have no platform idempotency key. The streaming lifecycle retries
// only the single 6001 rate-limit case its contract defines.
type Service struct {
	transport postTransport
}

// NewService builds the dynamic-card service on the shared qyopenapi transport.
func NewService(transport *dcinternal.Transport) Service {
	return Service{transport: transport}
}

func (s Service) post(ctx context.Context, route string, payload any, operation Operation) (CardResponse, error) {
	if ctx == nil {
		return CardResponse{}, newValidationError("context", "must not be nil")
	}
	if s.transport == nil {
		return CardResponse{}, newValidationError("transport", "dynamic card service is not initialized")
	}
	decision, err := s.transport.PostJSON(ctx, route, payload)
	if err != nil {
		return CardResponse{}, err
	}
	response := responseFromDecision(decision)
	if failure := response.Error(operation); failure != nil {
		return response, failure
	}
	return response, nil
}

func responseFromDecision(decision dcinternal.Decision) CardResponse {
	return CardResponse{
		Shape:      ResponseShape(decision.Shape),
		Code:       decision.Code,
		Message:    decision.Message,
		Payload:    decision.Payload,
		Raw:        decision.RawJSON(),
		HTTPStatus: decision.HTTPStatus,
		LogID:      decision.LogID,
	}
}

func missingResponseField(field string, operation Operation, response CardResponse) *DynamicCardError {
	return &DynamicCardError{
		Category:   CategoryServer,
		Message:    fmt.Sprintf("response is missing %s", field),
		HTTPStatus: response.HTTPStatus,
		Operation:  operation,
		LogID:      response.LogID,
		Raw:        response.RawJSON(),
	}
}

// firstObjectField reads field from a JSON object, returning nil when the
// object is not decodable or the field is absent/null.
func firstObjectField(raw json.RawMessage, field string) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	value := fields[field]
	if len(value) == 0 {
		return nil
	}
	return value
}
