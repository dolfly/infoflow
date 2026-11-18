// Package dynamiccard contains the public AI dynamic-card (scheme=1) contract:
// the content and component model, the normalized response decision, the error
// model with category/retryable classification, the lossless 19-digit
// identifier type, and the Send, NewStream, Update and Ack capabilities
// (including structured @ and quoted-reply metadata on the first send).
package dynamiccard

import (
	"bytes"
	"encoding/json"
)

// mergeExtra encodes known and then flattens extra into the same JSON object.
// Known fields win on name collision, and empty extra entries are ignored.
func mergeExtra(known any, extra map[string]json.RawMessage) ([]byte, error) {
	raw, err := json.Marshal(known)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage, len(extra))
	}
	for name, value := range extra {
		if _, exists := fields[name]; exists {
			continue
		}
		if len(bytes.TrimSpace(value)) == 0 {
			continue
		}
		fields[name] = value
	}
	return json.Marshal(fields)
}

// splitExtra decodes raw into target and keeps every key that is not a known
// field in extra, so unknown platform fields survive a round trip.
func splitExtra(raw []byte, target any, extra *map[string]json.RawMessage, known ...string) error {
	if err := json.Unmarshal(raw, target); err != nil {
		return err
	}
	if extra == nil {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, name := range known {
		delete(fields, name)
	}
	if len(fields) == 0 {
		*extra = nil
		return nil
	}
	*extra = fields
	return nil
}

// mergeAnyExtra encodes known and then flattens extra into the same JSON object.
// Known fields win on name collision and nil extra entries are ignored.
func mergeAnyExtra(known any, extra map[string]any) ([]byte, error) {
	raw, err := json.Marshal(known)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage, len(extra))
	}
	for name, value := range extra {
		if _, exists := fields[name]; exists {
			continue
		}
		if value == nil {
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		fields[name] = encoded
	}
	return json.Marshal(fields)
}

// splitAnyExtra decodes raw into target and keeps every key that is not a known
// field in extra. Numbers are decoded as json.Number so they keep their digits.
func splitAnyExtra(raw []byte, target any, extra *map[string]any, known ...string) error {
	if err := json.Unmarshal(raw, target); err != nil {
		return err
	}
	if extra == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var fields map[string]any
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	for _, name := range known {
		delete(fields, name)
	}
	if len(fields) == 0 {
		*extra = nil
		return nil
	}
	*extra = fields
	return nil
}
