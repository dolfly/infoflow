package dynamiccard

import (
	"encoding/json"
	"strings"
	"testing"
)

type idCarrier struct {
	MsgKey ID `json:"msg_key"`
}

// T-17: a 19-digit identifier survives both wire forms and is always emitted
// as a string.
func TestIDIsLossless(t *testing.T) {
	const digits = "1234567890123456789"

	var fromNumber idCarrier
	if err := json.Unmarshal([]byte(`{"msg_key":1234567890123456789}`), &fromNumber); err != nil {
		t.Fatalf("decode number: %v", err)
	}
	if got := fromNumber.MsgKey.String(); got != digits {
		t.Fatalf("numeric decode = %q, want %q", got, digits)
	}
	if got := fromNumber.MsgKey.Int64(); got != 1234567890123456789 {
		t.Fatalf("int64 = %d", got)
	}

	var fromString idCarrier
	if err := json.Unmarshal([]byte(`{"msg_key":"1234567890123456789"}`), &fromString); err != nil {
		t.Fatalf("decode string: %v", err)
	}
	if fromString.MsgKey != fromNumber.MsgKey {
		t.Fatalf("string and number forms differ: %d vs %d", fromString.MsgKey, fromNumber.MsgKey)
	}

	raw, err := json.Marshal(fromNumber)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	want := `{"msg_key":"` + digits + `"}`
	if string(raw) != want {
		t.Fatalf("encode = %s, want %s", raw, want)
	}
	if strings.Contains(string(raw), "1234567890123456789,") {
		t.Fatalf("identifier was emitted as a number: %s", raw)
	}
}

func TestIDNullAndOptional(t *testing.T) {
	var carrier idCarrier
	if err := json.Unmarshal([]byte(`{"msg_key":null}`), &carrier); err != nil {
		t.Fatalf("decode null: %v", err)
	}
	if carrier.MsgKey != 0 {
		t.Fatalf("null id = %d, want 0", carrier.MsgKey)
	}

	type optional struct {
		Key ID `json:"msg_key,omitempty"`
	}
	raw, err := json.Marshal(optional{})
	if err != nil {
		t.Fatalf("encode optional: %v", err)
	}
	if string(raw) != `{}` {
		t.Fatalf("optional zero id = %s, want it omitted", raw)
	}
}

func TestIDRejectsInvalidInput(t *testing.T) {
	if _, err := ParseID(""); err == nil {
		t.Fatal("blank id accepted")
	}
	if _, err := ParseID("abc"); err == nil {
		t.Fatal("non-numeric id accepted")
	}
	if _, err := ParseID("9223372036854775808"); err == nil {
		t.Fatal("overflowing id accepted")
	}
	var carrier idCarrier
	if err := json.Unmarshal([]byte(`{"msg_key":1.5}`), &carrier); err == nil {
		t.Fatal("fractional id accepted")
	}
	if err := json.Unmarshal([]byte(`{"msg_key":"x"}`), &carrier); err == nil {
		t.Fatal("non-numeric string id accepted")
	}
	parsed, err := ParseID(" 42 ")
	if err != nil || parsed.Int64() != 42 {
		t.Fatalf("parse = %v, %v", parsed, err)
	}
}
