package dynamiccard

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
)

// NewLogID returns a random RFC 4122 version 4 UUID used as the LOGID header on
// every new-route request. Each request gets a fresh value; the same value is
// retained on the error object so it can be matched against server logs.
func NewLogID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("dynamic card log id: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40 // version 4
	raw[8] = (raw[8] & 0x3f) | 0x80 // RFC 4122 variant
	text := hex.EncodeToString(raw[:])
	return text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:32], nil
}

// NewHeaders returns the request headers required by the new dynamic-card
// routes: the JSON content type with the exact charset, the access channel
// marker, the LOGID and, when a token is present, the Bearer- authorization
// value.
func NewHeaders(token, logID string) http.Header {
	headers := make(http.Header, 4)
	headers.Set(HeaderContentType, ContentTypeJSON)
	headers.Set(HeaderAccessChannel, AccessChannel)
	if logID != "" {
		headers.Set(HeaderLogID, logID)
	}
	if token != "" {
		headers.Set(HeaderAuthorization, AuthorizationScheme+token)
	}
	return headers
}
