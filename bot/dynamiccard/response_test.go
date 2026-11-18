package dynamiccard

import (
	"bytes"
	"encoding/json"
	"testing"
)

// T-01: the public decoder exposes the success payload and business code.
func TestDecodeResponseSuccess(t *testing.T) {
	raw := []byte(`{"code":"ok","data":{"code":0,"card_id":"card-1"}}`)
	response := DecodeResponse(raw)
	if response.Shape != ResponseSuccess || !response.OK() {
		t.Fatalf("response = %+v, want success", response)
	}
	if response.Code != "0" {
		t.Fatalf("code = %q, want 0", response.Code)
	}
	if !bytes.Contains(response.Payload, []byte("card-1")) {
		t.Fatalf("payload = %s", response.Payload)
	}
	if response.Error(OperationSend) != nil {
		t.Fatal("success produced an error")
	}
}

// T-02: a flat business failure becomes a classified error.
func TestDecodeResponseBusinessFailure(t *testing.T) {
	response := DecodeResponse([]byte(`{"code":1009,"message":"sequence mismatch"}`))
	if response.Shape != ResponseBusiness {
		t.Fatalf("shape = %q", response.Shape)
	}
	failure := response.Error(OperationUpdate)
	if failure == nil {
		t.Fatal("business failure produced no error")
	}
	if !failure.IsSequenceConflict() {
		t.Fatalf("category = %q, want sequence_conflict", failure.Category)
	}
	if failure.Retryable {
		t.Fatal("1009 must not be retryable")
	}
	if failure.CodeString() != "1009" {
		t.Fatalf("code = %q", failure.CodeString())
	}
	if failure.Operation != OperationUpdate || failure.Message != "sequence mismatch" {
		t.Fatalf("error = %+v", failure)
	}
	if string(failure.RawJSON()) != `{"code":1009,"message":"sequence mismatch"}` {
		t.Fatalf("raw = %s", failure.RawJSON())
	}
}

// T-03: a gateway string code is never a business success.
func TestDecodeResponseGateway(t *testing.T) {
	response := DecodeResponse([]byte(`{"code":"request error","msg":"upstream"}`))
	failure := response.Error(OperationStreamAppend)
	if failure == nil || !failure.IsGateway() {
		t.Fatalf("error = %+v, want gateway", failure)
	}
	if failure.CodeString() != "request error" {
		t.Fatalf("code = %q", failure.CodeString())
	}
	if failure.Retryable {
		t.Fatal("gateway errors must not be marked retryable by default")
	}
}

func TestDecodeResponseUnknown(t *testing.T) {
	response := DecodeResponse([]byte(`{"unexpected":true}`))
	if response.Shape != ResponseUnknown {
		t.Fatalf("shape = %q", response.Shape)
	}
	failure := response.Error(OperationAck)
	if failure == nil || !failure.IsUnknown() {
		t.Fatalf("error = %+v, want unknown", failure)
	}
}

// T-04: the HTTP status classifies a body whose business code is unknown.
func TestDecodeResponseHTTPFallback(t *testing.T) {
	response := DecodeResponse([]byte(`{"code":424242}`)).WithHTTPStatus(401)
	failure := response.Error(OperationSend)
	if failure == nil || !failure.IsAuth() {
		t.Fatalf("error = %+v, want auth from HTTP 401", failure)
	}
	if failure.HTTPStatus != 401 {
		t.Fatalf("HTTPStatus = %d", failure.HTTPStatus)
	}
}

// T-05: rate limiting is retryable; the sequence is not part of the decision.
func TestDecodeResponseRateLimited(t *testing.T) {
	failure := DecodeResponse([]byte(`{"code":6001,"msg":"too many appends"}`)).Error(OperationStreamAppend)
	if failure == nil || !failure.IsRateLimited() || !failure.Retryable {
		t.Fatalf("error = %+v, want retryable rate_limited", failure)
	}
}

func TestCardResponseHelpers(t *testing.T) {
	response := DecodeResponse([]byte(`{"code":1001,"message":"bad"}`)).WithHTTPStatus(400).WithLogID("log-1")
	if response.LogID != "log-1" || response.HTTPStatus != 400 {
		t.Fatalf("response = %+v", response)
	}
	failure := response.Error(OperationSend)
	if failure.LogID != "log-1" || failure.HTTPStatus != 400 {
		t.Fatalf("failure = %+v", failure)
	}

	raw := response.RawJSON()
	raw[0] = 'X'
	if bytes.Equal(raw, response.Raw) {
		t.Fatal("RawJSON did not return a defensive copy")
	}

	var renamed json.RawMessage = failure.RawJSON()
	if len(renamed) == 0 {
		t.Fatal("error raw is empty")
	}
}
