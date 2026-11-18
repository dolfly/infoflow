package dynamiccard

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// ID is a platform identifier such as the 19-digit message key. It is stored as
// int64 and always serialized as a JSON string, so consumers in languages with
// 53-bit integer precision never lose digits. UnmarshalJSON accepts both the
// numeric and the string wire form.
type ID int64

// NewID converts an int64 identifier.
func NewID(value int64) ID { return ID(value) }

// ParseID parses a decimal identifier and rejects empty or non-numeric input.
func ParseID(text string) (ID, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, errors.New("dynamic card id must not be blank")
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, err
	}
	return ID(value), nil
}

// Int64 returns the identifier value.
func (id ID) Int64() int64 { return int64(id) }

// String returns the decimal identifier.
func (id ID) String() string { return strconv.FormatInt(int64(id), 10) }

// MarshalJSON encodes the identifier as a JSON string.
func (id ID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

// UnmarshalJSON accepts a JSON number, a decimal string, or null (zero).
func (id *ID) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*id = 0
		return nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return err
		}
		parsed, err := ParseID(text)
		if err != nil {
			return err
		}
		*id = parsed
		return nil
	}
	var value int64
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return err
	}
	*id = ID(value)
	return nil
}
