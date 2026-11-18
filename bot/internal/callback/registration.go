// Package callback owns Bot-only callback registration envelope decoding.
package callback

import (
	"encoding/json"
	"strconv"

	"github.com/dolfly/infoflow/internal/sdkerrors"
)

type Registration struct {
	Code    int64
	Message string
	LogID   string
	Raw     []byte
}

func DecodeRegistration(raw []byte) (Registration, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Registration{}, sdkerrors.NewMalformed("callback registration response is invalid", raw, err)
	}
	topCode := rawString(envelope["code"])
	if topCode == "" {
		return Registration{}, sdkerrors.NewMalformed("callback registration code is missing", raw, nil)
	}
	message := rawString(envelope["message"])
	if message == "" {
		message = rawString(envelope["msg"])
	}
	if topCode != "ok" {
		return Registration{}, sdkerrors.NewBusiness(topCode, message, raw)
	}
	var data map[string]json.RawMessage
	if len(envelope["data"]) == 0 || json.Unmarshal(envelope["data"], &data) != nil {
		return Registration{}, sdkerrors.NewMalformed("callback registration data is invalid", raw, nil)
	}
	nestedCode := rawScalar(data["code"])
	if nestedCode == "" {
		nestedCode = "0"
	}
	if nestedMessage := rawString(data["msg"]); nestedMessage != "" {
		message = nestedMessage
	}
	if nestedCode != "0" && nestedCode != "200" {
		return Registration{}, sdkerrors.NewBusiness(nestedCode, message, raw)
	}
	code, _ := strconv.ParseInt(nestedCode, 10, 64)
	return Registration{Code: code, Message: message, LogID: rawScalar(data["logId"]), Raw: append([]byte(nil), raw...)}, nil
}

func rawString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func rawScalar(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		return rawString(raw)
	}
	return string(raw)
}
