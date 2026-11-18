package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	infoflow "github.com/dolfly/infoflow"
	"github.com/dolfly/infoflow/bot/events"
	botwebsocket "github.com/dolfly/infoflow/bot/websocket"
)

func TestWebSocketModeRegistrarUsesDedicatedEnvelopeAndRouteHeaders(t *testing.T) {
	const callbackToken = "0123456789abcdef0123456789abcdef"
	responses := []string{
		`{"code":"ok","data":{}}`,
		`{"code":"ok","data":{"code":0}}`,
		`{"code":"ok","data":{"code":200,"msg":"accepted"}}`,
	}
	var mu sync.Mutex
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/auth/app_access_token":
			respondWithToken(writer, 7200)
		case "/api/v1/imRobot/updateReCallUrl":
			if got := request.Header.Get("Authorization"); got != "Bearer-"+fixtureToken {
				t.Errorf("Authorization = %q", got)
			}
			if got := request.Header.Get("LOGID"); !regexp.MustCompile(`^[0-9]+$`).MatchString(got) {
				t.Errorf("LOGID = %q", got)
			}
			if got := request.Header.Get("X-OP-ACCESS-CHANNEL"); got != "go_sdk" {
				t.Errorf("X-OP-ACCESS-CHANNEL = %q", got)
			}
			assertJSONEqual(t, readRequestBody(t, request), `{
				"enWebSocket":1,"receiveMsgState":1,"token":"`+callbackToken+`"
			}`)
			mu.Lock()
			response := responses[requestCount]
			requestCount++
			mu.Unlock()
			writeJSON(writer, http.StatusOK, response)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := newFixtureClient(t, server, time.Now)
	for range responses {
		if err := client.Bot.WebSocket.Register(context.Background(), callbackToken); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if requestCount != len(responses) {
		t.Fatalf("callback requests = %d", requestCount)
	}
}

func TestWebSocketModeRegistrarErrorsAndObserverAreRedacted(t *testing.T) {
	const callbackToken = "abcdef0123456789abcdef0123456789"
	var records []events.Diagnostic
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/auth/app_access_token" {
			respondWithToken(writer, 7200)
			return
		}
		writeJSON(writer, http.StatusOK, `{"code":"ok","data":{"code":40044,"msg":"`+callbackToken+`"}}`)
	}))
	defer server.Close()
	client := newFixtureClient(t, server, time.Now, func(config *infoflow.Config) {
		config.Realtime.Observer = events.DiagnosticHandler(func(record events.Diagnostic) {
			records = append(records, record)
		})
	})
	err := client.Bot.WebSocket.Register(context.Background(), callbackToken)
	if err == nil {
		t.Fatal("RegisterWebSocket() error = nil")
	}
	serialized, _ := json.Marshal(records)
	combined := fmt.Sprint(err, string(serialized))
	for _, sensitive := range []string{callbackToken, fixtureToken, "Bearer-" + fixtureToken} {
		if strings.Contains(combined, sensitive) {
			t.Fatalf("registrar leaked %q: %s", sensitive, combined)
		}
	}
}

func TestWebSocketModeRegistrarSensitivePOSTDoesNotFollowRedirect(t *testing.T) {
	const callbackToken = "abcdef0123456789abcdef0123456789"
	targetRequests := 0
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		targetRequests++
		if strings.Contains(string(readRequestBody(t, request)), callbackToken) {
			t.Error("redirect target received callback token")
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/auth/app_access_token" {
			respondWithToken(writer, 7200)
			return
		}
		writer.Header().Set("Location", target.URL+"/capture")
		writer.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	callerClient := source.Client()
	redirectPolicyCalls := 0
	callerClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		redirectPolicyCalls++
		return nil
	}
	client, err := infoflow.NewClient(infoflow.Config{
		AppKey: fixtureAppKey, AppSecret: fixtureSecret,
		BaseURL: source.URL + "/api/v1", HTTPClient: callerClient,
	})
	if err != nil {
		t.Fatalf("infoflow.NewClient() error = %v", err)
	}
	err = client.Bot.WebSocket.Register(context.Background(), callbackToken)
	var targetError *infoflow.HTTPError
	if !errors.As(err, &targetError) || targetError.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("RegisterWebSocket() error = %T %#v, want HTTP 307", err, err)
	}
	if targetRequests != 0 || redirectPolicyCalls != 0 || callerClient.CheckRedirect == nil {
		t.Fatalf("redirect target=%d caller policy calls=%d", targetRequests, redirectPolicyCalls)
	}
}

type rootFixtureTransport struct {
	writes chan []byte
}

func (transport *rootFixtureTransport) Write(_ context.Context, payload []byte) error {
	transport.writes <- bytes.Clone(payload)
	return nil
}

func (*rootFixtureTransport) Close() error { return nil }

type rootFixtureDialer struct {
	transport *rootFixtureTransport
	callbacks botwebsocket.TransportCallbacks
}

func (dialer *rootFixtureDialer) Dial(_ context.Context, _ string, _ botwebsocket.DialOptions, callbacks botwebsocket.TransportCallbacks) (botwebsocket.Transport, error) {
	dialer.callbacks = callbacks
	return dialer.transport, nil
}

func TestClientWebSocketSessionUsesSharedDispatcherAndObserver(t *testing.T) {
	observed := make(chan struct{}, 1)
	client, err := infoflow.NewClient(infoflow.Config{
		AppKey: "app-key", AppSecret: "app-secret",
		Realtime: infoflow.RealtimeConfig{Observer: events.DiagnosticHandler(func(record events.Diagnostic) {
			select {
			case observed <- struct{}{}:
			default:
			}
		})},
	})
	if err != nil {
		t.Fatalf("infoflow.NewClient() error = %v", err)
	}
	delivered := make(chan events.Event, 1)
	if err := client.Bot.Events.On("private.text", func(_ context.Context, event events.Event) error {
		delivered <- event
		return nil
	}); err != nil {
		t.Fatalf("On() error = %v", err)
	}
	transport := &rootFixtureTransport{writes: make(chan []byte, 8)}
	dialer := &rootFixtureDialer{transport: transport}
	disabled := false
	session, err := client.Bot.WebSocket.NewSession(botwebsocket.SessionOptions{
		EndpointAllocator: botwebsocket.EndpointAllocatorFunc(func(context.Context) (botwebsocket.Endpoint, error) {
			return botwebsocket.Endpoint{URL: "wss://fixture.example.test/ws?connection_id=private"}, nil
		}),
		Dialer: dialer, AutoRegister: &disabled,
	})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.Connect(ctx); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	<-transport.writes // immediate ping
	frame, _ := botwebsocket.EncodeFrame(&botwebsocket.Frame{
		SeqId: 1, LogId: "log", Method: int32(botwebsocket.MethodData), Payload: []byte(`{"MsgType":"text"}`),
	})
	dialer.callbacks.OnMessage(frame)
	select {
	case event := <-delivered:
		if event.Type != "private.text" {
			t.Fatalf("event = %#v", event)
		}
	case <-ctx.Done():
		t.Fatal("shared dispatcher did not receive WebSocket event")
	}
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("root observer received no WebSocket records")
	}
}
