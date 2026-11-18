package dynamiccard

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDynamicCardErrorPredicates(t *testing.T) {
	cases := []struct {
		category  Category
		predicate func(*DynamicCardError) bool
	}{
		{CategoryValidation, (*DynamicCardError).IsValidation},
		{CategorySequenceConflict, (*DynamicCardError).IsSequenceConflict},
		{CategoryStreamState, (*DynamicCardError).IsStreamState},
		{CategoryAckExpired, (*DynamicCardError).IsAckExpired},
		{CategoryOwnership, (*DynamicCardError).IsOwnership},
		{CategoryRateLimited, (*DynamicCardError).IsRateLimited},
		{CategoryAuth, (*DynamicCardError).IsAuth},
		{CategoryNotFound, (*DynamicCardError).IsNotFound},
		{CategoryDuplicate, (*DynamicCardError).IsDuplicate},
		{CategoryServer, (*DynamicCardError).IsServer},
		{CategoryGateway, (*DynamicCardError).IsGateway},
		{CategoryUnknown, (*DynamicCardError).IsUnknown},
	}
	for _, testCase := range cases {
		failure := &DynamicCardError{Category: testCase.category}
		if !testCase.predicate(failure) {
			t.Fatalf("predicate for %q = false", testCase.category)
		}
		for _, other := range cases {
			if other.category == testCase.category {
				continue
			}
			if other.predicate(failure) {
				t.Fatalf("predicate for %q matched %q", other.category, testCase.category)
			}
		}
	}
	var missing *DynamicCardError
	if missing.IsValidation() {
		t.Fatal("nil error matched a predicate")
	}
}

func TestDynamicCardErrorFormatting(t *testing.T) {
	cause := errors.New("connection reset")
	failure := &DynamicCardError{
		Code:      int64(1009),
		Category:  CategorySequenceConflict,
		Message:   "sequence stale",
		Operation: OperationStreamAppend,
		Cause:     cause,
	}
	message := failure.Error()
	if message != "dynamic card stream.append failed (sequence_conflict), code 1009: sequence stale" {
		t.Fatalf("message = %q", message)
	}
	if !errors.Is(failure, cause) {
		t.Fatal("errors.Is did not unwrap the cause")
	}

	var asError error = failure
	var target *DynamicCardError
	if !errors.As(asError, &target) {
		t.Fatal("errors.As did not match DynamicCardError")
	}

	plain := &DynamicCardError{Operation: OperationAck, Code: "request error"}
	if got := plain.Error(); got != "dynamic card ack failed (unknown), code request error" {
		t.Fatalf("message = %q", got)
	}
	empty := &DynamicCardError{}
	if got := empty.Error(); got != "dynamic card request failed (unknown)" {
		t.Fatalf("message = %q", got)
	}
}

func TestDynamicCardErrorCodeString(t *testing.T) {
	cases := []struct {
		code any
		want string
	}{
		{nil, ""},
		{int64(6001), "6001"},
		{42, "42"},
		{"request error", "request error"},
	}
	for _, testCase := range cases {
		failure := &DynamicCardError{Code: testCase.code}
		if got := failure.CodeString(); got != testCase.want {
			t.Fatalf("CodeString(%v) = %q, want %q", testCase.code, got, testCase.want)
		}
	}
}

func TestDynamicCardErrorRawJSONIsDefensive(t *testing.T) {
	original := json.RawMessage(`{"code":1001}`)
	failure := &DynamicCardError{Raw: original}
	raw := failure.RawJSON()
	raw[0] = 'X'
	if string(failure.Raw) != `{"code":1001}` {
		t.Fatalf("raw mutated through the accessor: %s", failure.Raw)
	}
}

func TestPublicClassification(t *testing.T) {
	if classification := ClassifyCode(1011); !classification.Known || classification.Category != CategoryAckExpired {
		t.Fatalf("1011 = %+v", classification)
	}
	if classification := ClassifyCode(6001); !classification.Retryable {
		t.Fatal("6001 must be retryable")
	}
	if classification := ClassifyCode(42); classification.Known {
		t.Fatal("unknown code reported as known")
	}
	if classification := ClassifyHTTPStatus(409); !classification.Known ||
		classification.Category != CategoryDuplicate || !classification.Retryable {
		t.Fatalf("409 = %+v", classification)
	}
	if classification := ClassifyHTTPStatus(418); classification.Known {
		t.Fatal("418 reported as known")
	}
}
