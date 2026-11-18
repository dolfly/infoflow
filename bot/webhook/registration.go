package webhook

import "encoding/json"

// Registration switches the platform callback mode to Webhook.
type Registration struct {
	CallbackURL    string
	Token          string
	EncodingAESKey string
}

// RegistrationResult is the typed callback registration result.
type RegistrationResult struct {
	Code    int64
	Message string
	LogID   string
	raw     json.RawMessage
}

// NewRegistrationResult constructs a parsed result while defensively copying
// the protocol-open response. It is primarily used by the Client service.
func NewRegistrationResult(code int64, message, logID string, raw []byte) RegistrationResult {
	return RegistrationResult{
		Code: code, Message: message, LogID: logID,
		raw: append(json.RawMessage(nil), raw...),
	}
}

func (r RegistrationResult) RawJSON() json.RawMessage {
	return append(json.RawMessage(nil), r.raw...)
}
