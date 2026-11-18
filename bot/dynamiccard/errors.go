package dynamiccard

import (
	"encoding/json"
	"fmt"
	"strconv"

	dcinternal "github.com/dolfly/infoflow/internal/dynamiccard"
)

// Category classifies a dynamic-card failure. Business logic must branch on the
// category (or code) and never on the human-readable message.
type Category string

// Error categories defined by the dynamic-card contract.
const (
	CategoryValidation       Category = Category(dcinternal.CategoryValidation)
	CategorySequenceConflict Category = Category(dcinternal.CategorySequenceConflict)
	CategoryStreamState      Category = Category(dcinternal.CategoryStreamState)
	CategoryAckExpired       Category = Category(dcinternal.CategoryAckExpired)
	CategoryOwnership        Category = Category(dcinternal.CategoryOwnership)
	CategoryRateLimited      Category = Category(dcinternal.CategoryRateLimited)
	CategoryAuth             Category = Category(dcinternal.CategoryAuth)
	CategoryNotFound         Category = Category(dcinternal.CategoryNotFound)
	CategoryDuplicate        Category = Category(dcinternal.CategoryDuplicate)
	CategoryServer           Category = Category(dcinternal.CategoryServer)
	CategoryGateway          Category = Category(dcinternal.CategoryGateway)
	CategoryUnknown          Category = Category(dcinternal.CategoryUnknown)
)

// Operation identifies the dynamic-card operation that failed.
type Operation string

// Operations that can appear on a DynamicCardError.
const (
	OperationSend         Operation = "send"
	OperationStreamEnable Operation = "stream.enable"
	OperationStreamAppend Operation = "stream.append"
	OperationStreamFinish Operation = "stream.finish"
	OperationUpdate       Operation = "update"
	OperationAck          Operation = "ack"
)

// DynamicCardError is the unified dynamic-card error. Code holds an int64
// business code or the raw gateway string; the remaining fields are populated
// only when the response carried them.
type DynamicCardError struct {
	Code            any
	Category        Category
	Message         string
	HTTPStatus      int
	Operation       Operation
	Retryable       bool
	CardID          string
	EventID         string
	RequestSequence *int64
	CurrentSequence *int64
	CurrentContent  *DynamicCardContent
	LogID           string
	Cause           error
	Raw             json.RawMessage
}

// Error implements error. It never includes credentials, tokens or content.
func (e *DynamicCardError) Error() string {
	operation := string(e.Operation)
	if operation == "" {
		operation = "request"
	}
	category := string(e.Category)
	if category == "" {
		category = string(CategoryUnknown)
	}
	message := fmt.Sprintf("dynamic card %s failed (%s)", operation, category)
	if code := e.CodeString(); code != "" {
		message += ", code " + code
	}
	if e.Message != "" {
		message += ": " + e.Message
	}
	return message
}

// Unwrap returns the transport or encoding error that caused this failure.
func (e *DynamicCardError) Unwrap() error {
	return e.Cause
}

// CodeString renders Code as a decimal or raw string without losing precision.
func (e *DynamicCardError) CodeString() string {
	switch value := e.Code.(type) {
	case nil:
		return ""
	case int64:
		return strconv.FormatInt(value, 10)
	case int:
		return strconv.Itoa(value)
	case string:
		return value
	default:
		return fmt.Sprint(value)
	}
}

// RawJSON returns the original response, defensively copied.
func (e *DynamicCardError) RawJSON() json.RawMessage {
	return json.RawMessage(append([]byte(nil), e.Raw...))
}

// IsValidation reports CategoryValidation: invalid input or content, missing
// fields, or a platform limit.
func (e *DynamicCardError) IsValidation() bool { return e.hasCategory(CategoryValidation) }

// IsSequenceConflict reports CategorySequenceConflict: the sequence is stale
// and current_sequence must be adopted rather than blindly replayed.
func (e *DynamicCardError) IsSequenceConflict() bool {
	return e.hasCategory(CategorySequenceConflict)
}

// IsStreamState reports CategoryStreamState: the streaming state conflicts with
// the operation.
func (e *DynamicCardError) IsStreamState() bool { return e.hasCategory(CategoryStreamState) }

// IsAckExpired reports CategoryAckExpired: the ACK window is closed.
func (e *DynamicCardError) IsAckExpired() bool { return e.hasCategory(CategoryAckExpired) }

// IsOwnership reports CategoryOwnership: the message or operator is not owned by
// this robot.
func (e *DynamicCardError) IsOwnership() bool { return e.hasCategory(CategoryOwnership) }

// IsRateLimited reports CategoryRateLimited: back off and resend only the latest
// snapshot.
func (e *DynamicCardError) IsRateLimited() bool { return e.hasCategory(CategoryRateLimited) }

// IsAuth reports CategoryAuth: authentication failed and must not be retried
// indefinitely.
func (e *DynamicCardError) IsAuth() bool { return e.hasCategory(CategoryAuth) }

// IsNotFound reports CategoryNotFound: the message does not exist or expired.
func (e *DynamicCardError) IsNotFound() bool { return e.hasCategory(CategoryNotFound) }

// IsDuplicate reports CategoryDuplicate: the event was already delivered; it is
// safe to treat the operation as an idempotent success.
func (e *DynamicCardError) IsDuplicate() bool { return e.hasCategory(CategoryDuplicate) }

// IsServer reports CategoryServer: an internal platform error.
func (e *DynamicCardError) IsServer() bool { return e.hasCategory(CategoryServer) }

// IsGateway reports CategoryGateway: a gateway-level failure that is never a
// business success.
func (e *DynamicCardError) IsGateway() bool { return e.hasCategory(CategoryGateway) }

// IsUnknown reports CategoryUnknown: an unrecognized code that must not be
// treated as success.
func (e *DynamicCardError) IsUnknown() bool { return e.hasCategory(CategoryUnknown) }

func (e *DynamicCardError) hasCategory(category Category) bool {
	return e != nil && e.Category == category
}

// ErrorClassification is the category and retryability of a platform code or
// HTTP status. Known is false when the value is not part of the contract.
type ErrorClassification struct {
	Category  Category
	Retryable bool
	Known     bool
}

// ClassifyCode maps a business code to its contract category and retryability.
func ClassifyCode(code int64) ErrorClassification {
	classification := dcinternal.ClassifyBusinessCode(code)
	return ErrorClassification{
		Category:  Category(classification.Category),
		Retryable: classification.Retryable,
		Known:     classification.Known,
	}
}

// ClassifyHTTPStatus maps an HTTP status to its contract category and
// retryability.
func ClassifyHTTPStatus(status int) ErrorClassification {
	classification := dcinternal.ClassifyHTTPStatus(status)
	return ErrorClassification{
		Category:  Category(classification.Category),
		Retryable: classification.Retryable,
		Known:     classification.Known,
	}
}

func newValidationError(field, message string) *DynamicCardError {
	return &DynamicCardError{
		Category: CategoryValidation,
		Message:  field + ": " + message,
	}
}
