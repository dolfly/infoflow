package websocket

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/dolfly/infoflow/bot/websocket/framepb"
)

// Frame and Header are the checked-in generated canonical protobuf bindings.
type Frame = framepb.Frame
type Header = framepb.Header

// Method identifies the InfoFlow Frame method.
type Method int32

const (
	MethodControl  Method = 0
	MethodData     Method = 1
	MethodRequest  Method = 2
	MethodResponse Method = 3
)

// EncodeFrame serializes one canonical protobuf Frame deterministically.
func EncodeFrame(frame *Frame) ([]byte, error) {
	if frame == nil {
		return nil, errors.New("websocket frame must not be nil")
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(frame)
}

// DecodeFrame parses one canonical protobuf Frame while retaining unknown
// fields so a decode/encode round-trip remains protocol-open.
func DecodeFrame(encoded []byte) (*Frame, error) {
	frame := new(Frame)
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(encoded, frame); err != nil {
		return nil, errors.New("invalid websocket frame")
	}
	return frame, nil
}

// CloneFrame returns a defensive protobuf copy.
func CloneFrame(frame *Frame) *Frame {
	if frame == nil {
		return nil
	}
	return proto.Clone(frame).(*Frame)
}

// EncodePayload converts an open JSON payload to bytes.
func EncodePayload(payload any) ([]byte, error) {
	if payload == nil {
		return []byte("{}"), nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("invalid websocket JSON payload")
	}
	return encoded, nil
}

// DecodePayload parses an open JSON object and preserves large integers as
// json.Number. Returned maps are newly allocated.
func DecodePayload(encoded []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(encoded)) == 0 {
		return nil, errors.New("invalid websocket JSON payload")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		return nil, errors.New("invalid websocket JSON payload")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("invalid websocket JSON payload")
	}
	return payload, nil
}

func expandEventPayload(encoded []byte) (map[string]any, error) {
	outer, err := DecodePayload(encoded)
	if err != nil {
		return nil, err
	}
	var encrypted any
	exists := false
	for key, value := range outer {
		if strings.EqualFold(key, "Encrypt") {
			encrypted, exists = value, true
			break
		}
	}
	if !exists {
		return outer, nil
	}
	innerJSON, ok := encrypted.(string)
	if !ok {
		return outer, nil
	}
	inner, err := DecodePayload([]byte(innerJSON))
	if err != nil {
		return outer, errors.New("invalid websocket Encrypt payload")
	}
	for key, value := range inner {
		outer[key] = value
	}
	return outer, nil
}

func headerValue(frame *Frame, key string) string {
	for _, header := range frame.GetHeaders() {
		if header.GetKey() == key {
			return header.GetValue()
		}
	}
	return ""
}
