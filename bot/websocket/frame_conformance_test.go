package websocket_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"testing"

	infows "github.com/dolfly/infoflow/bot/websocket"
)

func TestFrameCanonicalGoldenAndMethodMapping(t *testing.T) {
	frame := &infows.Frame{
		SeqId: 1, LogId: "log", Method: int32(infows.MethodData),
		Headers: []*infows.Header{{Key: "type", Value: "event"}}, Payload: []byte("{}"),
	}
	encoded, err := infows.EncodeFrame(frame)
	if err != nil {
		t.Fatalf("EncodeFrame() error = %v", err)
	}
	const golden = "080112036c6f6720012a0d0a047479706512056576656e7432027b7d"
	if got := hex.EncodeToString(encoded); got != golden {
		t.Fatalf("wire = %s, want %s", got, golden)
	}
	wantMethods := []infows.Method{
		infows.MethodControl, infows.MethodData, infows.MethodRequest, infows.MethodResponse,
	}
	for value, method := range wantMethods {
		if int32(method) != int32(value) {
			t.Errorf("method[%d] = %d", value, method)
		}
		encoded, _ := infows.EncodeFrame(&infows.Frame{Method: int32(method), Payload: []byte("{}")})
		decoded, err := infows.DecodeFrame(encoded)
		if err != nil || decoded.GetMethod() != int32(value) {
			t.Errorf("method round trip %d: frame=%#v err=%v", value, decoded, err)
		}
	}
}

func TestFrameUnknownFieldRetentionAndMalformedInputs(t *testing.T) {
	empty, err := infows.DecodeFrame(nil)
	if err != nil || empty.GetMethod() != int32(infows.MethodControl) {
		t.Fatalf("canonical zero Frame = %#v err=%v", empty, err)
	}
	base, _ := infows.EncodeFrame(&infows.Frame{SeqId: 1, Method: int32(infows.MethodData), Payload: []byte("{}")})
	// Unknown field 99, wire type varint, value 7.
	unknown := []byte{0x98, 0x06, 0x07}
	decoded, err := infows.DecodeFrame(append(append([]byte(nil), base...), unknown...))
	if err != nil {
		t.Fatalf("DecodeFrame(unknown) error = %v", err)
	}
	reencoded, err := infows.EncodeFrame(decoded)
	if err != nil || !bytes.Contains(reencoded, unknown) {
		t.Fatalf("unknown field not retained: %x err=%v", reencoded, err)
	}
	if _, err := infows.DecodeFrame([]byte{0xff}); err == nil {
		t.Fatal("malformed protobuf returned nil error")
	}
	if _, err := infows.DecodePayload([]byte(`{"broken"`)); err == nil {
		t.Fatal("malformed JSON returned nil error")
	}
}

func TestFrameDefensiveCloneAndPayloadRoundTrip(t *testing.T) {
	original := &infows.Frame{
		SeqId: 42, Headers: []*infows.Header{{Key: "type", Value: "event"}},
		Payload: []byte(`{"messageid":9007199254740993123}`),
	}
	clone := infows.CloneFrame(original)
	clone.Headers[0].Value = "changed"
	clone.Payload[0] = 'x'
	if original.Headers[0].Value != "event" || original.Payload[0] != '{' {
		t.Fatalf("CloneFrame mutated original: %#v", original)
	}
	payload, err := infows.DecodePayload(original.Payload)
	if err != nil || payload["messageid"] != json.Number("9007199254740993123") {
		t.Fatalf("payload=%#v err=%v", payload, err)
	}
}
