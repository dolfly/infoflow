package sdkerrors

import (
	"encoding/json"
	"fmt"
)

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	return append([]byte(nil), value...)
}

// ConfigError reports invalid SDK configuration or public method input.
type ConfigError struct {
	Field   string
	Message string
}

func NewConfig(field, message string) *ConfigError {
	return &ConfigError{Field: field, Message: message}
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("invalid InfoFlow configuration for %s: %s", e.Field, e.Message)
}

// AuthError reports a platform authentication-envelope failure.
type AuthError struct {
	Code    string
	Message string
	raw     []byte
}

func NewAuth(code, message string, raw []byte) *AuthError {
	return &AuthError{Code: code, Message: message, raw: cloneBytes(raw)}
}

func (e *AuthError) Error() string {
	if e.Code == "" {
		return "InfoFlow authentication failed"
	}
	return fmt.Sprintf("InfoFlow authentication failed (code %s)", e.Code)
}

func (e *AuthError) RawJSON() json.RawMessage {
	return json.RawMessage(cloneBytes(e.raw))
}

// HTTPError reports a non-2xx response or an HTTP transport failure.
// StatusCode is zero when no response was received.
type HTTPError struct {
	StatusCode int
	body       []byte
	cause      error
}

func NewHTTP(statusCode int, body []byte, cause error) *HTTPError {
	return &HTTPError{StatusCode: statusCode, body: cloneBytes(body), cause: cause}
}

func (e *HTTPError) Error() string {
	if e.StatusCode == 0 {
		return "InfoFlow HTTP request failed"
	}
	return fmt.Sprintf("InfoFlow HTTP request failed with status %d", e.StatusCode)
}

func (e *HTTPError) Unwrap() error {
	return e.cause
}

func (e *HTTPError) Body() []byte {
	return cloneBytes(e.body)
}

// BusinessError reports a non-success platform business status.
type BusinessError struct {
	Code      string
	Message   string
	RequestID string
	raw       []byte
}

func NewBusiness(code, message string, raw []byte, requestID ...string) *BusinessError {
	return &BusinessError{Code: code, Message: message, RequestID: firstRequestID(requestID), raw: cloneBytes(raw)}
}

func (e *BusinessError) Error() string {
	if e.Code == "" {
		return "InfoFlow business request failed"
	}
	return fmt.Sprintf("InfoFlow business request failed (code %s)", e.Code)
}

func (e *BusinessError) RawJSON() json.RawMessage {
	return json.RawMessage(cloneBytes(e.raw))
}

// MalformedResponseError reports invalid JSON or an incomplete success
// envelope. RawResponse returns the received bytes defensively.
type MalformedResponseError struct {
	Message   string
	RequestID string
	raw       []byte
	cause     error
}

func NewMalformed(message string, raw []byte, cause error, requestID ...string) *MalformedResponseError {
	return &MalformedResponseError{Message: message, RequestID: firstRequestID(requestID), raw: cloneBytes(raw), cause: cause}
}

func (e *MalformedResponseError) Error() string {
	return "malformed InfoFlow response: " + e.Message
}

func (e *MalformedResponseError) Unwrap() error {
	return e.cause
}

func (e *MalformedResponseError) RawResponse() []byte {
	return cloneBytes(e.raw)
}

func firstRequestID(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
