package dynamiccard

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Shape is the normalized classification of one dynamic-card response body.
type Shape string

const (
	// ShapeSuccess is {"code":"ok","data":{"code":0,...}}: the payload is data.
	ShapeSuccess Shape = "success"
	// ShapeBusiness is a flat business failure such as {"code":1009,...}.
	ShapeBusiness Shape = "business"
	// ShapeGateway is a non-"ok" string status such as {"code":"request error"}.
	ShapeGateway Shape = "gateway"
	// ShapeUnknown is a body the contract cannot classify.
	ShapeUnknown Shape = "unknown"
)

// Decision is the three-layer envelope decision defined by the dynamic-card
// contract. Code is the decimal business code for success and business shapes,
// and the raw gateway string for the gateway shape.
type Decision struct {
	Shape      Shape
	Code       string
	Message    string
	Payload    json.RawMessage
	Raw        json.RawMessage
	HTTPStatus int
	LogID      string
}

// OK reports whether the decision is a business success.
func (d Decision) OK() bool {
	return d.Shape == ShapeSuccess
}

// WithHTTPStatus returns a copy of the decision carrying the HTTP status of the
// response that produced it.
func (d Decision) WithHTTPStatus(status int) Decision {
	d.HTTPStatus = status
	return d
}

// RawJSON returns the complete original response, defensively copied.
func (d Decision) RawJSON() json.RawMessage {
	return json.RawMessage(append([]byte(nil), d.Raw...))
}

// Decide applies the contract's envelope decision to one response body:
//
//	if body is not an object  -> gateway
//	if body.code == "ok"      -> payload = body.data, business code = payload.code
//	elif body.code is number  -> business failure (flat)
//	elif body.code is string  -> gateway error
//	else                      -> unknown
func Decide(raw []byte) Decision {
	decision := Decision{Raw: append(json.RawMessage(nil), raw...)}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		decision.Shape = ShapeGateway
		decision.Message = "response is not a JSON object"
		return decision
	}

	rawCode, ok := root["code"]
	if !ok {
		decision.Shape = ShapeUnknown
		decision.Message = "response code is missing"
		return decision
	}
	code := bytes.TrimSpace(rawCode)
	if len(code) > 0 && code[0] == '"' {
		var text string
		if err := json.Unmarshal(code, &text); err != nil {
			decision.Shape = ShapeUnknown
			decision.Message = "response code is invalid"
			return decision
		}
		if text == "ok" {
			return decideBusiness(decision, root)
		}
		decision.Shape = ShapeGateway
		decision.Code = text
		decision.Message = messageAt(root, "")
		return decision
	}
	if decimal, err := decimalCode(code); err == nil {
		decision.Shape = ShapeBusiness
		decision.Code = decimal
		decision.Message = messageAt(root, "")
		return decision
	}
	decision.Shape = ShapeUnknown
	decision.Message = "response code is invalid"
	return decision
}

func decideBusiness(decision Decision, root map[string]json.RawMessage) Decision {
	rawData, ok := root["data"]
	if !ok || len(bytes.TrimSpace(rawData)) == 0 || bytes.Equal(bytes.TrimSpace(rawData), []byte("null")) {
		decision.Shape = ShapeUnknown
		decision.Message = "business payload is missing"
		return decision
	}
	payload := append(json.RawMessage(nil), rawData...)
	decision.Payload = payload

	var body map[string]json.RawMessage
	if err := json.Unmarshal(payload, &body); err != nil || body == nil {
		decision.Shape = ShapeUnknown
		decision.Message = "business payload is not a JSON object"
		return decision
	}
	rawCode, ok := body["code"]
	if !ok {
		decision.Shape = ShapeUnknown
		decision.Message = "business code is missing"
		return decision
	}
	code, err := decimalCode(bytes.TrimSpace(rawCode))
	if err != nil {
		decision.Shape = ShapeUnknown
		decision.Message = "business code is invalid"
		return decision
	}
	decision.Code = code
	decision.Message = messageAt(body, messageAt(root, ""))
	if code == "0" {
		decision.Shape = ShapeSuccess
		return decision
	}
	decision.Shape = ShapeBusiness
	return decision
}

func decimalCode(raw []byte) (string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", errors.New("missing code")
	}
	if raw[0] == '"' {
		return "", errors.New("code must be numeric")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var number json.Number
	if err := decoder.Decode(&number); err != nil {
		return "", err
	}
	if _, err := number.Int64(); err != nil {
		return "", err
	}
	return number.String(), nil
}

func messageAt(value map[string]json.RawMessage, fallback string) string {
	for _, name := range []string{"message", "msg", "errmsg", "err_msg"} {
		raw, ok := value[name]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil && text != "" {
			return text
		}
	}
	return fallback
}
