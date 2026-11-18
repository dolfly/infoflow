package websocket_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dolfly/infoflow/bot/internal/realtime"
	infows "github.com/dolfly/infoflow/bot/websocket"
)

func endpointResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestEndpointGatewayPrecedence(t *testing.T) {
	tests := []struct {
		name        string
		explicit    string
		environment string
		wantHost    string
	}{
		{name: "default", wantHost: infows.DefaultGateway},
		{name: "environment", environment: "env.example.test:9443", wantHost: "env.example.test:9443"},
		{name: "explicit", explicit: "code.example.test:8443", environment: "env.example.test:9443", wantHost: "code.example.test:8443"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observedHost := ""
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				observedHost = request.URL.Host
				return endpointResponse(http.StatusOK, `{"code":0,"data":{"url":"wss://ws.example.test/path"}}`), nil
			})}
			allocator, err := infows.NewEndpointAllocator(infows.EndpointOptions{
				AppKey: "key", AppSecret: "secret", Gateway: test.explicit, HTTPClient: client,
				LookupEnv: func(name string) (string, bool) {
					if name == "INFOFLOW_WS_GATEWAY" && test.environment != "" {
						return test.environment, true
					}
					return "", false
				},
			})
			if err != nil {
				t.Fatalf("NewEndpointAllocator() error = %v", err)
			}
			if _, err := allocator.Allocate(context.Background()); err != nil {
				t.Fatalf("Allocate() error = %v", err)
			}
			if observedHost != test.wantHost {
				t.Fatalf("host = %q, want %q", observedHost, test.wantHost)
			}
		})
	}
}

func TestEndpointSensitivePOSTDoesNotFollowRedirectOrMutateCallerClient(t *testing.T) {
	for _, status := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var targetRequests atomic.Int32
			target := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				targetRequests.Add(1)
				_, _ = io.ReadAll(request.Body)
				writer.WriteHeader(http.StatusOK)
			}))
			defer target.Close()

			source := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Location", target.URL+"/capture")
				writer.WriteHeader(status)
			}))
			defer source.Close()

			callerClient := source.Client()
			var callerRedirectPolicyCalls atomic.Int32
			callerClient.CheckRedirect = func(*http.Request, []*http.Request) error {
				callerRedirectPolicyCalls.Add(1)
				return nil
			}
			allocator, err := infows.NewEndpointAllocator(infows.EndpointOptions{
				AppKey: "sensitive-key", AppSecret: "sensitive-secret",
				Gateway: strings.TrimPrefix(source.URL, "https://"), HTTPClient: callerClient,
			})
			if err != nil {
				t.Fatalf("NewEndpointAllocator() error = %v", err)
			}
			_, err = allocator.Allocate(context.Background())
			var targetError *infows.EndpointError
			if !errors.As(err, &targetError) || targetError.Category != "http_status" {
				t.Fatalf("Allocate() error = %T %#v, want http_status", err, err)
			}
			if targetRequests.Load() != 0 {
				t.Fatalf("redirect target received %d requests", targetRequests.Load())
			}
			if callerRedirectPolicyCalls.Load() != 0 || callerClient.CheckRedirect == nil {
				t.Fatalf("caller redirect policy was invoked or mutated: calls=%d", callerRedirectPolicyCalls.Load())
			}
		})
	}
}

func TestEndpointTypedFailuresTimeoutAndExactRedaction(t *testing.T) {
	const (
		appSecret   = "seed-endpoint-app-secret"
		endpointURL = "wss://private.example.test/ws?connection_id=seed-private-query"
	)
	tests := []struct {
		name     string
		response func(*http.Request) (*http.Response, error)
		category string
	}{
		{"http", func(*http.Request) (*http.Response, error) { return endpointResponse(502, endpointURL), nil }, "http_status"},
		{"business", func(*http.Request) (*http.Response, error) {
			return endpointResponse(200, `{"code":17,"msg":"`+appSecret+`"}`), nil
		}, "business"},
		{"missing code", func(*http.Request) (*http.Response, error) {
			return endpointResponse(200, `{"data":{"url":"`+endpointURL+`"}}`), nil
		}, "malformed_response"},
		{"missing url", func(*http.Request) (*http.Response, error) { return endpointResponse(200, `{"code":0,"data":{}}`), nil }, "missing_url"},
		{"malformed", func(*http.Request) (*http.Response, error) { return endpointResponse(200, `{bad `+appSecret), nil }, "malformed_response"},
		{"oversized", func(*http.Request) (*http.Response, error) {
			body := `{"code":0,"data":{"url":"` + endpointURL + `"}}` + strings.Repeat(" ", (1<<20)+1)
			return endpointResponse(200, body), nil
		}, "malformed_response"},
		{"timeout", func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}, "timeout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var records []realtime.Diagnostic
			allocator, err := infows.NewEndpointAllocator(infows.EndpointOptions{
				AppKey: "key", AppSecret: appSecret, Gateway: "gateway.example.test",
				Timeout: 5 * time.Millisecond, HTTPClient: &http.Client{Transport: roundTripFunc(test.response)},
				Observer: realtime.DiagnosticHandler(func(record realtime.Diagnostic) { records = append(records, record) }),
			})
			if err != nil {
				t.Fatalf("NewEndpointAllocator() error = %v", err)
			}
			_, err = allocator.Allocate(context.Background())
			var target *infows.EndpointError
			if !errors.As(err, &target) || target.Category != test.category {
				t.Fatalf("error = %T %#v, want EndpointError(%s)", err, err, test.category)
			}
			if len(records) != 2 || records[0].Event != "ws.endpoint.allocate.start" ||
				records[1].Event != "ws.endpoint.allocate.failure" || records[0].RequestID != records[1].RequestID {
				t.Fatalf("records = %#v", records)
			}
			serialized, _ := json.Marshal(records)
			combined := fmt.Sprint(err, string(serialized))
			for _, sensitive := range []string{appSecret, endpointURL, "seed-private-query"} {
				if strings.Contains(combined, sensitive) {
					t.Fatalf("failure leaked %q: %s", sensitive, combined)
				}
			}
		})
	}
}

func TestEndpointSuccessObservabilityCorrelationAndRedaction(t *testing.T) {
	const endpointURL = "wss://private.example.test/ws?connection_id=seed-private-query"
	var records []realtime.Diagnostic
	allocator, _ := infows.NewEndpointAllocator(infows.EndpointOptions{
		AppKey: "key", AppSecret: "seed-private-secret", Gateway: "gateway.example.test",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return endpointResponse(200, `{"code":0,"data":{"url":"`+endpointURL+`"}}`), nil
		})},
		Observer: realtime.DiagnosticHandler(func(record realtime.Diagnostic) { records = append(records, record) }),
	})
	endpoint, err := allocator.Allocate(context.Background())
	if err != nil || endpoint.URL != endpointURL {
		t.Fatalf("endpoint=%#v err=%v", endpoint, err)
	}
	if len(records) != 2 || records[0].Event != "ws.endpoint.allocate.start" ||
		records[1].Event != "ws.endpoint.allocate.success" || records[0].RequestID != records[1].RequestID {
		t.Fatalf("records = %#v", records)
	}
	serialized, _ := json.Marshal(records)
	for _, sensitive := range []string{"seed-private-secret", endpointURL, "seed-private-query"} {
		if strings.Contains(string(serialized), sensitive) {
			t.Fatalf("success observer leaked %q: %s", sensitive, serialized)
		}
	}
}
