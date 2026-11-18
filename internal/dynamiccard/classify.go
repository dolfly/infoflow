package dynamiccard

import "strconv"

// Error categories defined by the dynamic-card contract.
const (
	CategoryValidation       = "validation"
	CategorySequenceConflict = "sequence_conflict"
	CategoryStreamState      = "stream_state"
	CategoryAckExpired       = "ack_expired"
	CategoryOwnership        = "ownership"
	CategoryRateLimited      = "rate_limited"
	CategoryAuth             = "auth"
	CategoryNotFound         = "not_found"
	CategoryDuplicate        = "duplicate"
	CategoryServer           = "server"
	CategoryGateway          = "gateway"
	CategoryUnknown          = "unknown"
)

// Classification is the category and retryability of a platform code or HTTP
// status. Known is false when the value is not part of the contract, in which
// case the caller must fall back to CategoryUnknown instead of treating the
// response as success.
type Classification struct {
	Category  string
	Retryable bool
	Known     bool
}

// ClassifyBusinessCode maps a numeric business code to its contract category.
func ClassifyBusinessCode(code int64) Classification {
	switch code {
	case 1001, 1002:
		return Classification{Category: CategoryValidation, Known: true}
	case 1009:
		return Classification{Category: CategorySequenceConflict, Known: true}
	case 1010:
		return Classification{Category: CategoryStreamState, Known: true}
	case 1011:
		return Classification{Category: CategoryAckExpired, Known: true}
	case 3003:
		return Classification{Category: CategoryOwnership, Known: true}
	case 6001:
		return Classification{Category: CategoryRateLimited, Retryable: true, Known: true}
	case 9001:
		return Classification{Category: CategoryServer, Retryable: true, Known: true}
	default:
		return Classification{Category: CategoryUnknown}
	}
}

// ClassifyBusinessCodeString maps a decimal business code string. A value that
// is not an integer is unknown rather than a success.
func ClassifyBusinessCodeString(code string) Classification {
	parsed, err := strconv.ParseInt(code, 10, 64)
	if err != nil {
		return Classification{Category: CategoryUnknown}
	}
	return ClassifyBusinessCode(parsed)
}

// ClassifyHTTPStatus maps an HTTP status to its contract category.
func ClassifyHTTPStatus(status int) Classification {
	switch status {
	case 400, 408, 411:
		return Classification{Category: CategoryValidation, Known: true}
	case 401:
		return Classification{Category: CategoryAuth, Known: true}
	case 403, 410:
		return Classification{Category: CategoryOwnership, Known: true}
	case 404, 405:
		return Classification{Category: CategoryNotFound, Known: true}
	case 406:
		return Classification{Category: CategoryStreamState, Known: true}
	case 407:
		return Classification{Category: CategorySequenceConflict, Known: true}
	case 409:
		return Classification{Category: CategoryDuplicate, Retryable: true, Known: true}
	case 429:
		return Classification{Category: CategoryRateLimited, Retryable: true, Known: true}
	case 500, 501:
		return Classification{Category: CategoryServer, Retryable: true, Known: true}
	default:
		return Classification{Category: CategoryUnknown}
	}
}
