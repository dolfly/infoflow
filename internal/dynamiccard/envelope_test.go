package dynamiccard

import (
	"bytes"
	"testing"
)

// T-01: nested ok envelope with a zero business code is a success.
func TestDecideSuccessEnvelope(t *testing.T) {
	decision := Decide([]byte(`{"code":"ok","data":{"code":0,"card_id":"card-1"}}`))
	if decision.Shape != ShapeSuccess {
		t.Fatalf("shape = %q, want %q", decision.Shape, ShapeSuccess)
	}
	if decision.Code != "0" {
		t.Fatalf("code = %q, want %q", decision.Code, "0")
	}
	if !bytes.Contains(decision.Payload, []byte("card-1")) {
		t.Fatalf("payload = %s, want it to contain card-1", decision.Payload)
	}
	if !decision.OK() {
		t.Fatal("OK() = false, want true")
	}
}

func TestDecideSuccessWithNonZeroBusinessCodeIsBusiness(t *testing.T) {
	decision := Decide([]byte(`{"code":"ok","data":{"code":1009,"message":"sequence stale"}}`))
	if decision.Shape != ShapeBusiness {
		t.Fatalf("shape = %q, want %q", decision.Shape, ShapeBusiness)
	}
	if decision.Code != "1009" || decision.Message != "sequence stale" {
		t.Fatalf("code/message = %q/%q", decision.Code, decision.Message)
	}
}

// T-02: a flat numeric code is a business failure.
func TestDecideFlatBusinessFailure(t *testing.T) {
	decision := Decide([]byte(`{"code":1001,"message":"content too large"}`))
	if decision.Shape != ShapeBusiness {
		t.Fatalf("shape = %q, want %q", decision.Shape, ShapeBusiness)
	}
	if decision.Code != "1001" {
		t.Fatalf("code = %q, want 1001", decision.Code)
	}
	if decision.Message != "content too large" {
		t.Fatalf("message = %q", decision.Message)
	}
	if len(decision.Payload) != 0 {
		t.Fatalf("payload = %s, want empty", decision.Payload)
	}
}

// T-03: any other string code is a gateway error.
func TestDecideGatewayStringCode(t *testing.T) {
	decision := Decide([]byte(`{"code":"request error","msg":"upstream timeout"}`))
	if decision.Shape != ShapeGateway {
		t.Fatalf("shape = %q, want %q", decision.Shape, ShapeGateway)
	}
	if decision.Code != "request error" {
		t.Fatalf("code = %q", decision.Code)
	}
	if decision.Message != "upstream timeout" {
		t.Fatalf("message = %q", decision.Message)
	}
}

func TestDecideMalformedBodies(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantShape Shape
	}{
		{"array", `[1,2,3]`, ShapeGateway},
		{"scalar", `"bad gateway"`, ShapeGateway},
		{"invalid json", `{"code":`, ShapeGateway},
		{"missing code", `{"data":{"code":0}}`, ShapeUnknown},
		{"empty object", `{}`, ShapeUnknown},
		{"ok without data", `{"code":"ok"}`, ShapeUnknown},
		{"ok with null data", `{"code":"ok","data":null}`, ShapeUnknown},
		{"ok with scalar data", `{"code":"ok","data":"x"}`, ShapeUnknown},
		{"ok without business code", `{"code":"ok","data":{"card_id":"c"}}`, ShapeUnknown},
		{"ok with string business code", `{"code":"ok","data":{"code":"0"}}`, ShapeUnknown},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			decision := Decide([]byte(testCase.raw))
			if decision.Shape != testCase.wantShape {
				t.Fatalf("shape = %q, want %q (message %q)", decision.Shape, testCase.wantShape, decision.Message)
			}
			if decision.OK() {
				t.Fatal("OK() = true for a non-success decision")
			}
			if string(decision.Raw) != testCase.raw {
				t.Fatalf("raw = %q, want %q", decision.Raw, testCase.raw)
			}
		})
	}
}

// T-04: every documented HTTP status maps to its category and retryability.
func TestClassifyHTTPStatus(t *testing.T) {
	cases := []struct {
		status    int
		category  string
		retryable bool
	}{
		{400, CategoryValidation, false},
		{401, CategoryAuth, false},
		{403, CategoryOwnership, false},
		{404, CategoryNotFound, false},
		{405, CategoryNotFound, false},
		{406, CategoryStreamState, false},
		{407, CategorySequenceConflict, false},
		{408, CategoryValidation, false},
		{409, CategoryDuplicate, true},
		{410, CategoryOwnership, false},
		{411, CategoryValidation, false},
		{429, CategoryRateLimited, true},
		{500, CategoryServer, true},
		{501, CategoryServer, true},
	}
	for _, testCase := range cases {
		classification := ClassifyHTTPStatus(testCase.status)
		if !classification.Known {
			t.Fatalf("status %d not known", testCase.status)
		}
		if classification.Category != testCase.category || classification.Retryable != testCase.retryable {
			t.Fatalf("status %d = %s/%v, want %s/%v", testCase.status,
				classification.Category, classification.Retryable, testCase.category, testCase.retryable)
		}
	}
	for _, status := range []int{0, 200, 402, 418, 502, 503} {
		if classification := ClassifyHTTPStatus(status); classification.Known {
			t.Fatalf("status %d unexpectedly known as %s", status, classification.Category)
		}
	}
}

// T-05: every documented business code maps to its category and retryability.
func TestClassifyBusinessCode(t *testing.T) {
	cases := []struct {
		code      int64
		category  string
		retryable bool
	}{
		{1001, CategoryValidation, false},
		{1002, CategoryValidation, false},
		{1009, CategorySequenceConflict, false},
		{1010, CategoryStreamState, false},
		{1011, CategoryAckExpired, false},
		{3003, CategoryOwnership, false},
		{6001, CategoryRateLimited, true},
		{9001, CategoryServer, true},
	}
	for _, testCase := range cases {
		classification := ClassifyBusinessCode(testCase.code)
		if !classification.Known {
			t.Fatalf("code %d not known", testCase.code)
		}
		if classification.Category != testCase.category || classification.Retryable != testCase.retryable {
			t.Fatalf("code %d = %s/%v, want %s/%v", testCase.code,
				classification.Category, classification.Retryable, testCase.category, testCase.retryable)
		}
	}
	if classification := ClassifyBusinessCode(1234); classification.Known {
		t.Fatalf("unknown code classified as %s", classification.Category)
	}
	if classification := ClassifyBusinessCodeString("not-a-number"); classification.Known {
		t.Fatal("non-numeric code must not be known")
	}
	if classification := ClassifyBusinessCodeString("6001"); !classification.Known || !classification.Retryable {
		t.Fatal("string 6001 must be known and retryable")
	}
}
