package dynamiccard

import (
	"encoding/json"
	"strconv"
	"strings"

	dcinternal "github.com/dolfly/infoflow/internal/dynamiccard"
)

// ResponseShape is the normalized classification of a dynamic-card response.
type ResponseShape string

// Response shapes defined by the three-layer envelope contract.
const (
	// ResponseSuccess is {"code":"ok","data":{"code":0,...}}.
	ResponseSuccess ResponseShape = "success"
	// ResponseBusiness is a flat business failure such as {"code":1009,...}.
	ResponseBusiness ResponseShape = "business"
	// ResponseGateway is a non-"ok" gateway status such as {"code":"request error"}.
	ResponseGateway ResponseShape = "gateway"
	// ResponseUnknown is a body the contract cannot classify.
	ResponseUnknown ResponseShape = "unknown"
)

// CardResponse is the normalized decision for one dynamic-card response body
// plus, when known, the HTTP status that carried it.
type CardResponse struct {
	Shape      ResponseShape
	Code       string
	Message    string
	Payload    json.RawMessage
	Raw        json.RawMessage
	HTTPStatus int
	LogID      string
}

// DecodeResponse applies the contract's envelope decision to a response body:
// a not-object body is a gateway error, {"code":"ok"} takes data and its
// business code, a numeric code is a flat business failure, and any other
// string code is a gateway error.
func DecodeResponse(raw []byte) CardResponse {
	decision := dcinternal.Decide(raw)
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

// WithHTTPStatus returns a copy carrying the HTTP status of the response.
func (r CardResponse) WithHTTPStatus(status int) CardResponse {
	r.HTTPStatus = status
	return r
}

// WithLogID returns a copy carrying the request LOGID.
func (r CardResponse) WithLogID(logID string) CardResponse {
	r.LogID = logID
	return r
}

// OK reports whether the response is a business success.
func (r CardResponse) OK() bool {
	return r.Shape == ResponseSuccess
}

// RawJSON returns the original response, defensively copied.
func (r CardResponse) RawJSON() json.RawMessage {
	return json.RawMessage(append([]byte(nil), r.Raw...))
}

// Error converts a non-success response into a *DynamicCardError. It returns
// nil for a success. The category comes from the business code first and falls
// back to the HTTP status; an unrecognized response is CategoryUnknown and is
// never treated as success.
func (r CardResponse) Error(operation Operation) *DynamicCardError {
	if r.OK() {
		return nil
	}
	failure := &DynamicCardError{
		Category:   CategoryUnknown,
		Message:    r.Message,
		HTTPStatus: r.HTTPStatus,
		Operation:  operation,
		LogID:      r.LogID,
		Raw:        r.RawJSON(),
	}
	switch r.Shape {
	case ResponseGateway:
		failure.Category = CategoryGateway
		failure.Code = r.Code
	case ResponseBusiness:
		classification := ErrorClassification{Category: CategoryUnknown}
		if code, err := strconv.ParseInt(strings.TrimSpace(r.Code), 10, 64); err == nil {
			failure.Code = code
			classification = ClassifyCode(code)
		} else {
			failure.Code = r.Code
		}
		if !classification.Known {
			classification = ClassifyHTTPStatus(r.HTTPStatus)
		}
		failure.Category = classification.Category
		failure.Retryable = classification.Retryable
	default:
		if r.Code != "" {
			failure.Code = r.Code
		}
		if classification := ClassifyHTTPStatus(r.HTTPStatus); classification.Known {
			failure.Category = classification.Category
			failure.Retryable = classification.Retryable
		}
	}
	return failure
}
