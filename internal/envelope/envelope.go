package envelope

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/dolfly/infoflow/internal/sdkerrors"
)

type Envelope struct {
	Code    string
	Message string
	Data    json.RawMessage
	raw     []byte
}

type wireEnvelope struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Msg     string          `json:"msg"`
	ErrMsg  string          `json:"errmsg"`
	Data    json.RawMessage `json:"data"`
}

func Decode(raw []byte) (Envelope, error) {
	var wire wireEnvelope
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Envelope{}, sdkerrors.NewMalformed("response is not valid JSON", raw, err)
	}
	if wire.Code == "" {
		return Envelope{}, sdkerrors.NewMalformed("top-level code is missing", raw, nil)
	}
	message := wire.Message
	if message == "" {
		message = wire.Msg
	}
	if message == "" {
		message = wire.ErrMsg
	}
	return Envelope{
		Code:    wire.Code,
		Message: message,
		Data:    append(json.RawMessage(nil), wire.Data...),
		raw:     append([]byte(nil), raw...),
	}, nil
}

type businessStatus struct {
	ErrCode json.RawMessage `json:"errcode"`
	ErrMsg  string          `json:"errmsg"`
}

// DecodeBusiness centralizes top-level and nested Envelope B status handling.
// It returns the envelope data only when both levels indicate success.
func DecodeBusiness(raw []byte) (Envelope, error) {
	env, err := Decode(raw)
	if err != nil {
		return Envelope{}, err
	}
	if env.Code != "ok" {
		return Envelope{}, sdkerrors.NewBusiness(env.Code, env.Message, env.raw)
	}
	if len(env.Data) == 0 || bytes.Equal(bytes.TrimSpace(env.Data), []byte("null")) {
		return Envelope{}, sdkerrors.NewMalformed("business data is missing", raw, nil)
	}
	var status businessStatus
	if err := json.Unmarshal(env.Data, &status); err != nil {
		return Envelope{}, sdkerrors.NewMalformed("business data is invalid", raw, err)
	}
	code, err := platformCode(status.ErrCode)
	if err != nil {
		return Envelope{}, sdkerrors.NewMalformed("business errcode is missing or invalid", raw, err)
	}
	if code != "0" {
		return Envelope{}, sdkerrors.NewBusiness(code, status.ErrMsg, env.raw)
	}
	return env, nil
}

// BusinessEnvelopeC is the platform's nested business envelope used by the
// message summary routes: { code: "ok", data: { bizCode, bizMsg, bizData } }.
// BizData is empty when the server returns no payload (bizData null).
type BusinessEnvelopeC struct {
	BizCode string
	BizMsg  string
	BizData json.RawMessage
	raw     []byte
}

// RawJSON returns the complete original response defensively copied.
func (e BusinessEnvelopeC) RawJSON() json.RawMessage {
	return json.RawMessage(append([]byte(nil), e.raw...))
}

// DecodeEnvelopeC decodes the envelope-C (bizCode) profile used by the generic
// message summary endpoints. Unlike DecodeBusiness it does not classify
// bizCode: callers map the documented route codes (for example 200, 730004)
// themselves.
func DecodeEnvelopeC(raw []byte) (BusinessEnvelopeC, error) {
	env, err := Decode(raw)
	if err != nil {
		return BusinessEnvelopeC{}, err
	}
	if env.Code != "ok" {
		return BusinessEnvelopeC{}, sdkerrors.NewBusiness(env.Code, env.Message, env.raw)
	}
	data, err := object(env.Data)
	if err != nil {
		return BusinessEnvelopeC{}, sdkerrors.NewMalformed("business data is invalid", raw, err)
	}
	bizCode, err := platformCodeAt(data, "bizCode")
	if err != nil {
		return BusinessEnvelopeC{}, sdkerrors.NewMalformed("business bizCode is missing or invalid", raw, err)
	}
	bizMsg, _ := stringAt(data, "bizMsg")
	bizData := append(json.RawMessage(nil), data["bizData"]...)
	return BusinessEnvelopeC{
		BizCode: bizCode,
		BizMsg:  bizMsg,
		BizData: bizData,
		raw:     append([]byte(nil), raw...),
	}, nil
}

func platformCodeAt(value map[string]json.RawMessage, name string) (string, error) {
	rawCode, ok := value[name]
	if !ok {
		return "", errors.New("missing " + name)
	}
	return platformCode(rawCode)
}

// DecodeLegacyBFlex decodes the qyOpenAPI calendar and meeting response
// envelope. These routes use an outer success marker and may add one nested
// status envelope.
func DecodeLegacyBFlex(raw []byte, allowEmptyData bool) (json.RawMessage, error) {
	root, err := object(raw)
	if err != nil {
		return nil, sdkerrors.NewMalformed("legacy response is not a JSON object", raw, err)
	}
	if err := legacySuccess(root, raw); err != nil {
		return nil, err
	}
	data, exists := root["data"]
	if !exists || len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		if allowEmptyData {
			return nil, nil
		}
		return nil, sdkerrors.NewMalformed("legacy response data is missing", raw, nil)
	}
	dataObject, err := object(data)
	if err != nil {
		return append(json.RawMessage(nil), data...), nil
	}
	if _, ok := dataObject["code"]; ok {
		if err := legacySuccess(dataObject, raw); err != nil {
			return nil, err
		}
		if nested := dataObject["data"]; len(nested) > 0 && !bytes.Equal(bytes.TrimSpace(nested), []byte("null")) {
			return append(json.RawMessage(nil), nested...), nil
		}
	}
	if _, ok := dataObject["errcode"]; ok {
		if err := legacySuccess(dataObject, raw); err != nil {
			return nil, err
		}
		if nested := dataObject["data"]; len(nested) > 0 && !bytes.Equal(bytes.TrimSpace(nested), []byte("null")) {
			return append(json.RawMessage(nil), nested...), nil
		}
	}
	return append(json.RawMessage(nil), data...), nil
}

func legacySuccess(value map[string]json.RawMessage, raw []byte) error {
	if code, ok := value["code"]; ok {
		trimmed := bytes.TrimSpace(code)
		var text string
		if json.Unmarshal(trimmed, &text) == nil {
			if text == "ok" {
				return nil
			}
			return sdkerrors.NewBusiness(text, legacyMessage(value), raw)
		}
		parsed, err := platformCode(trimmed)
		if err != nil {
			return sdkerrors.NewMalformed("legacy response code is invalid", raw, err)
		}
		if parsed == "0" || parsed == "200" {
			return nil
		}
		return sdkerrors.NewBusiness(parsed, legacyMessage(value), raw)
	}
	if code, ok := value["errcode"]; ok {
		parsed, err := platformCode(code)
		if err != nil {
			return sdkerrors.NewMalformed("legacy response errcode is invalid", raw, err)
		}
		if parsed == "0" {
			return nil
		}
		return sdkerrors.NewBusiness(parsed, legacyMessage(value), raw)
	}
	return sdkerrors.NewMalformed("legacy response success marker is missing", raw, nil)
}

func legacyMessage(value map[string]json.RawMessage) string {
	for _, name := range []string{"errmsg", "message", "msg"} {
		if message, ok := stringAt(value, name); ok {
			return message
		}
	}
	return ""
}

func platformCode(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", errors.New("missing platform code")
	}
	if trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil || strings.TrimSpace(value) == "" {
			return "", errors.New("invalid string platform code")
		}
		return value, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value json.Number
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if _, err := value.Int64(); err != nil {
		return "", err
	}
	return value.String(), nil
}

// DecodeGroupCreateBFlex applies the route-declared B-flex profile for
// POST /robot/group/create. The profile deliberately enumerates every
// accepted path: it never searches JSON recursively.
func DecodeGroupCreateBFlex(raw []byte) (json.RawMessage, error) {
	root, err := object(raw)
	if err != nil {
		return nil, sdkerrors.NewMalformed("group create response is not a JSON object", raw, err)
	}
	requestID := groupCreateRequestID(root)
	code, ok := stringAt(root, "code")
	if !ok {
		return nil, sdkerrors.NewMalformed("group create success marker is missing", raw, nil, requestID)
	}
	if code != "ok" {
		return nil, sdkerrors.NewBusiness(groupCreateBusinessCode(root), groupCreateBusinessMessage(root), raw, requestID)
	}
	for _, status := range groupCreateStatusObjects(root) {
		if value, exists := status["errcode"]; exists {
			businessCode, err := platformCode(value)
			if err != nil {
				return nil, sdkerrors.NewMalformed("group create business errcode is invalid", raw, err, requestID)
			}
			if businessCode != "0" {
				return nil, sdkerrors.NewBusiness(businessCode, groupCreateBusinessMessage(root), raw, requestID)
			}
		}
	}
	for _, payload := range groupCreatePayloads(root) {
		if groupID, ok := positiveGroupID(payload["groupid"]); ok {
			return groupCreateResultPayload(payload, groupID), nil
		}
		if groupID, ok := positiveGroupID(payload["groupId"]); ok {
			return groupCreateResultPayload(payload, groupID), nil
		}
	}
	return nil, sdkerrors.NewMalformed("group create result is incomplete", raw, nil, requestID)
}

func object(raw []byte) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("expected JSON object")
	}
	return value, nil
}

func nestedObject(parent map[string]json.RawMessage, name string) map[string]json.RawMessage {
	raw, ok := parent[name]
	if !ok {
		return nil
	}
	child, err := object(raw)
	if err != nil {
		return nil
	}
	return child
}

func stringAt(value map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := value[name]
	if !ok {
		return "", false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", false
	}
	return text, true
}

func groupCreateStatusObjects(root map[string]json.RawMessage) []map[string]json.RawMessage {
	data := nestedObject(root, "data")
	return []map[string]json.RawMessage{root, data, nestedObject(data, "data")}
}

func groupCreatePayloads(root map[string]json.RawMessage) []map[string]json.RawMessage {
	data := nestedObject(root, "data")
	return []map[string]json.RawMessage{root, data, nestedObject(data, "data")}
}

func groupCreateRequestID(root map[string]json.RawMessage) string {
	data := nestedObject(root, "data")
	for _, value := range []map[string]json.RawMessage{root, data} {
		if requestID, ok := stringAt(value, "requestId"); ok {
			return requestID
		}
		if requestID, ok := stringAt(value, "request_id"); ok {
			return requestID
		}
	}
	return ""
}

func groupCreateBusinessCode(root map[string]json.RawMessage) string {
	for _, value := range groupCreateStatusObjects(root) {
		if errcode, ok := value["errcode"]; ok {
			if code, err := platformCode(errcode); err == nil {
				return code
			}
		}
	}
	return ""
}

func groupCreateBusinessMessage(root map[string]json.RawMessage) string {
	for _, value := range groupCreateStatusObjects(root) {
		if message, ok := stringAt(value, "errmsg"); ok {
			return message
		}
	}
	return ""
}

func positiveGroupID(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var id int64
	if err := json.Unmarshal(raw, &id); err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func groupCreateResultPayload(payload map[string]json.RawMessage, groupID int64) json.RawMessage {
	copy := make(map[string]json.RawMessage, len(payload)+1)
	for key, value := range payload {
		copy[key] = value
	}
	copy["groupId"], _ = json.Marshal(groupID)
	result, _ := json.Marshal(copy)
	return result
}
