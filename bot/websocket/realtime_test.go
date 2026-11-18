package websocket_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dolfly/infoflow/bot/events"
	infows "github.com/dolfly/infoflow/bot/websocket"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestEndpointAllocationRequestAndServerConfig(t *testing.T) {
	const secret = "endpoint-secret-fixture"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.String() != "https://gateway.example.test:8443/open/ws/endpoint" {
			t.Errorf("request = %s %s", request.Method, request.URL)
		}
		body, _ := io.ReadAll(request.Body)
		var payload map[string]string
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("request body error = %v", err)
		}
		if payload["app_key"] != "app-key" || payload["app_secret"] != secret {
			t.Fatalf("payload = %#v", payload)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"code":0,"msg":"ok","data":{
					"url":"wss://allocated.example.test/ws?connection_id=private-id",
					"client_config":{"reconnect_count":3,"reconnect_interval":5,"reconnect_nonce":2,"ping_interval":7}
				}
			}`)),
		}, nil
	})}
	allocator, err := infows.NewEndpointAllocator(infows.EndpointOptions{
		AppKey: "app-key", AppSecret: secret, Gateway: "gateway.example.test:8443",
		Timeout: 2 * time.Second, HTTPClient: client,
	})
	if err != nil {
		t.Fatalf("NewEndpointAllocator() error = %v", err)
	}
	endpoint, err := allocator.Allocate(context.Background())
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	if !strings.HasPrefix(endpoint.URL, "wss://") || endpoint.Config.ReconnectCount == nil || *endpoint.Config.ReconnectCount != 3 {
		t.Fatalf("endpoint = %#v", endpoint)
	}
	if endpoint.Config.PingInterval == nil || *endpoint.Config.PingInterval != 7*time.Second {
		t.Fatalf("ping interval = %#v", endpoint.Config.PingInterval)
	}
}

func TestFrameGeneratedBindingRoundTripAndLargeJSONInteger(t *testing.T) {
	frame := &infows.Frame{
		SeqId:   9007199254740993123,
		LogId:   "platform-log",
		Service: 7,
		Method:  int32(infows.MethodData),
		Headers: []*infows.Header{{Key: "type", Value: "event"}},
		Payload: []byte(`{"messageid":9007199254740993123}`),
	}
	encoded, err := infows.EncodeFrame(frame)
	if err != nil {
		t.Fatalf("EncodeFrame() error = %v", err)
	}
	decoded, err := infows.DecodeFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeFrame() error = %v", err)
	}
	if decoded.SeqId != frame.SeqId || decoded.LogId != frame.LogId {
		t.Fatalf("decoded = %#v", decoded)
	}
	payload, err := infows.DecodePayload(decoded.Payload)
	if err != nil {
		t.Fatalf("DecodePayload() error = %v", err)
	}
	if payload["messageid"] != json.Number("9007199254740993123") {
		t.Fatalf("messageid = %#v", payload["messageid"])
	}
}

type fixtureAllocator struct {
	endpoint infows.Endpoint
}

func (allocator fixtureAllocator) Allocate(context.Context) (infows.Endpoint, error) {
	return allocator.endpoint, nil
}

type fixtureTransport struct {
	mu     sync.Mutex
	writes [][]byte
	closed bool
}

func (transport *fixtureTransport) Write(_ context.Context, payload []byte) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.writes = append(transport.writes, bytes.Clone(payload))
	return nil
}

func (transport *fixtureTransport) Close() error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.closed = true
	return nil
}

type fixtureDialer struct {
	transport *fixtureTransport
	url       string
	options   infows.DialOptions
	callbacks infows.TransportCallbacks
}

func (dialer *fixtureDialer) Dial(_ context.Context, endpointURL string, options infows.DialOptions, callbacks infows.TransportCallbacks) (infows.Transport, error) {
	dialer.url = endpointURL
	dialer.options = options
	dialer.callbacks = callbacks
	return dialer.transport, nil
}

func TestSessionConnectPhase2AndImmediatePing(t *testing.T) {
	disabled := false
	transport := &fixtureTransport{}
	dialer := &fixtureDialer{transport: transport}
	session, err := infows.NewSession(infows.SessionOptions{
		Dispatcher: events.NewDispatcher(events.Options{}),
		EndpointAllocator: fixtureAllocator{endpoint: infows.Endpoint{
			URL: "wss://allocated.example.test/ws?connection_id=private-connection-id",
		}},
		Dialer: dialer, ConnectDomain: "override.example.test:9443", XSource: "go-sdk-test",
		AutoRegister: &disabled, HeartbeatInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.Connect(ctx); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if session.State() != infows.StateConnected {
		t.Fatalf("state = %s", session.State())
	}
	if dialer.url != "wss://override.example.test:9443/ws?connection_id=private-connection-id" ||
		dialer.options.Header.Get("X-Source") != "go-sdk-test_go_sdk" || !dialer.options.CompressionDisabled {
		t.Fatalf("dial URL/options = %q %#v", dialer.url, dialer.options)
	}
	deadline := time.Now().Add(time.Second)
	for {
		transport.mu.Lock()
		writes := len(transport.writes)
		transport.mu.Unlock()
		if writes > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("immediate ping was not written")
		}
		time.Sleep(time.Millisecond)
	}
	if err := session.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
}
