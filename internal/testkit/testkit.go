// Package testkit provides shared, SDK-internal fixtures for tests.
package testkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
)

const (
	AppKey    = "fixture-app"
	Secret    = "fixture-secret"
	SecretMD5 = "4c7400aa9bc33b3a304a208e880bac23"
	Token     = "fixture-token"
)

type TB interface {
	Helper()
	Fatalf(string, ...any)
	Errorf(string, ...any)
}

type CountingRoundTripper struct{ Requests atomic.Int32 }

func (r *CountingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	r.Requests.Add(1)
	return nil, errors.New("unexpected network request")
}

func WriteJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func RespondWithToken(w http.ResponseWriter, expire int64) {
	WriteJSON(w, http.StatusOK, fmt.Sprintf(`{"code":"ok","data":{"app_access_token":"%s","expire":%d}}`, Token, expire))
}

func ReadRequestBody(t TB, r *http.Request) []byte {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read request body: %v", err)
	}
	return body
}

func AssertJSONEqual(t TB, got []byte, want string) {
	t.Helper()
	var gotValue any
	var wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Errorf("request body is not JSON: %v; body=%q", err, got)
		return
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("invalid expected JSON: %v", err)
	}
	gotCanonical, _ := json.Marshal(gotValue)
	wantCanonical, _ := json.Marshal(wantValue)
	if string(gotCanonical) != string(wantCanonical) {
		t.Errorf("JSON body = %s, want %s", gotCanonical, wantCanonical)
	}
}

func ExtendedServer(handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/app_access_token" {
			RespondWithToken(w, 3600)
			return
		}
		handler(w, r)
	}))
}

func ErrorStringExcludes(t TB, err error, sensitiveValues ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want typed failure")
	}
	for _, sensitive := range sensitiveValues {
		if sensitive != "" && strings.Contains(err.Error(), sensitive) {
			t.Errorf("typed error string %q contains sensitive value %q", err.Error(), sensitive)
		}
	}
}
