package dynamiccard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dolfly/infoflow/internal/auth"
)

type capturedRequest struct {
	method        string
	path          string
	header        http.Header
	body          []byte
	authorization string
}

type requestLog struct {
	mu      sync.Mutex
	entries []capturedRequest
}

func (l *requestLog) add(entry capturedRequest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

func (l *requestLog) snapshot() []capturedRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]capturedRequest(nil), l.entries...)
}

func newDynamicCardTestServer(t *testing.T, sendResponse string, sendStatus int) (*httptest.Server, *requestLog) {
	t.Helper()
	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		log.add(capturedRequest{
			method:        request.Method,
			path:          request.URL.Path,
			header:        request.Header.Clone(),
			body:          body,
			authorization: request.Header.Get("Authorization"),
		})
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == RouteToken {
			_, _ = writer.Write([]byte(`{"code":"ok","data":{"app_access_token":"token-1","expire":7200}}`))
			return
		}
		writer.WriteHeader(sendStatus)
		_, _ = writer.Write([]byte(sendResponse))
	}))
	t.Cleanup(server.Close)
	return server, log
}

func newTestTransport(t *testing.T, server *httptest.Server) *Transport {
	t.Helper()
	transport, err := NewTransport(TransportOptions{
		AppKey:     "app-key",
		AppSecret:  "app-secret",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	return transport
}

// T-18 + T-19: the transport fetches one profile token and stamps every
// required new-route header.
func TestTransportStampsHeadersAndCachesToken(t *testing.T) {
	server, log := newDynamicCardTestServer(t, `{"code":"ok","data":{"code":0,"card_id":"card-1"}}`, http.StatusOK)
	transport := newTestTransport(t, server)
	if transport.Profile() != ProfileQyOpenAPI {
		t.Fatalf("profile = %q, want %q", transport.Profile(), ProfileQyOpenAPI)
	}
	if transport.BaseURL() != server.URL {
		t.Fatalf("base URL = %q, want %q", transport.BaseURL(), server.URL)
	}

	for attempt := 0; attempt < 2; attempt++ {
		decision, err := transport.PostJSON(context.Background(), RouteSend, map[string]any{"to_user": "u1"})
		if err != nil {
			t.Fatalf("post attempt %d: %v", attempt, err)
		}
		if !decision.OK() || decision.Code != "0" {
			t.Fatalf("decision = %+v, want success", decision)
		}
	}

	requests := log.snapshot()
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want 1 token + 2 sends", len(requests))
	}
	tokenRequest := requests[0]
	if tokenRequest.path != RouteToken || tokenRequest.method != http.MethodPost {
		t.Fatalf("token request = %s %s", tokenRequest.method, tokenRequest.path)
	}
	var tokenBody struct {
		AppKey    string `json:"app_key"`
		AppSecret string `json:"app_secret"`
	}
	if err := json.Unmarshal(tokenRequest.body, &tokenBody); err != nil {
		t.Fatalf("token body: %v", err)
	}
	if tokenBody.AppKey != "app-key" {
		t.Fatalf("app_key = %q", tokenBody.AppKey)
	}
	if want := auth.AppSecretDigest("app-secret"); tokenBody.AppSecret != want {
		t.Fatalf("app_secret = %q, want the MD5 digest", tokenBody.AppSecret)
	}
	if tokenRequest.authorization != "" {
		t.Fatalf("token request sent Authorization %q", tokenRequest.authorization)
	}
	if got := tokenRequest.header.Get("X-OP-ACCESS-CHANNEL"); got != AccessChannel {
		t.Fatalf("token request channel = %q, want %q", got, AccessChannel)
	}
	if !isUUIDv4(tokenRequest.header.Get("LOGID")) {
		t.Fatalf("token request LOGID = %q, want a version 4 UUID", tokenRequest.header.Get("LOGID"))
	}

	for _, captured := range requests[1:] {
		if captured.path != RouteSend {
			t.Fatalf("send path = %q", captured.path)
		}
		if captured.authorization != "Bearer-token-1" {
			t.Fatalf("Authorization = %q, want Bearer-token-1", captured.authorization)
		}
		if got := captured.header.Get("Content-Type"); got != ContentTypeJSON {
			t.Fatalf("Content-Type = %q, want %q", got, ContentTypeJSON)
		}
		if got := captured.header.Get("X-OP-ACCESS-CHANNEL"); got != AccessChannel {
			t.Fatalf("X-OP-ACCESS-CHANNEL = %q, want %q", got, AccessChannel)
		}
		if !isUUIDv4(captured.header.Get("LOGID")) {
			t.Fatalf("LOGID = %q, want a version 4 UUID", captured.header.Get("LOGID"))
		}
	}
	if requests[1].header.Get("LOGID") == requests[2].header.Get("LOGID") {
		t.Fatal("LOGID was reused across requests")
	}
}

// T-02/T-05: a non-2xx response with an envelope body is still classified.
func TestTransportDecodesHTTPErrorEnvelope(t *testing.T) {
	server, _ := newDynamicCardTestServer(t, `{"code":1001,"message":"content too large"}`, http.StatusBadRequest)
	transport := newTestTransport(t, server)
	decision, err := transport.PostJSON(context.Background(), RouteSend, map[string]any{})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if decision.Shape != ShapeBusiness || decision.Code != "1001" {
		t.Fatalf("decision = %+v, want business 1001", decision)
	}
	if decision.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("HTTPStatus = %d, want 400", decision.HTTPStatus)
	}
	if !isUUIDv4(decision.LogID) {
		t.Fatalf("decision LOGID = %q, want a UUID", decision.LogID)
	}
}

// T-03: a non-JSON gateway body is still a gateway decision, not a success.
func TestTransportDecodesGatewayBody(t *testing.T) {
	server, _ := newDynamicCardTestServer(t, `<html>bad gateway</html>`, http.StatusBadGateway)
	transport := newTestTransport(t, server)
	decision, err := transport.PostJSON(context.Background(), RouteUpdate, map[string]any{})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if decision.Shape != ShapeGateway {
		t.Fatalf("shape = %q, want gateway", decision.Shape)
	}
	if decision.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("HTTPStatus = %d, want 502", decision.HTTPStatus)
	}
	if decision.OK() {
		t.Fatal("gateway body reported success")
	}
}

func TestTransportUnauthenticatedPostSendsNoToken(t *testing.T) {
	server, log := newDynamicCardTestServer(t, `{"code":0}`, http.StatusOK)
	transport := newTestTransport(t, server)
	if _, err := transport.PostUnauthenticatedJSON(context.Background(), RouteToken, map[string]any{}); err != nil {
		t.Fatalf("post: %v", err)
	}
	requests := log.snapshot()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if requests[0].authorization != "" {
		t.Fatalf("unauthenticated request sent Authorization %q", requests[0].authorization)
	}
}

func TestTransportRejectsMissingCredentials(t *testing.T) {
	if _, err := NewTransport(TransportOptions{AppSecret: "s", BaseURL: "http://example"}); err == nil {
		t.Fatal("blank app key returned no error")
	}
	if _, err := NewTransport(TransportOptions{AppKey: "k", BaseURL: "http://example"}); err == nil {
		t.Fatal("blank app secret returned no error")
	}
}

func TestTransportSurfacesTokenFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"code":"invalid credential","msg":"bad app_key"}`))
	}))
	defer server.Close()
	transport, err := NewTransport(TransportOptions{
		AppKey: "app-key", AppSecret: "app-secret", BaseURL: server.URL, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	if _, err := transport.PostJSON(context.Background(), RouteSend, map[string]any{}); err == nil {
		t.Fatal("token failure returned no error")
	}
}

func TestTransportUsesEnvironmentBaseWhenNotExplicit(t *testing.T) {
	t.Setenv(BaseURLEnvVar, "https://env.example/open-plat/api/v1")
	transport, err := NewTransport(TransportOptions{AppKey: "k", AppSecret: "s", Profile: "custom"})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	if transport.BaseURL() != "https://env.example/open-plat/api/v1" {
		t.Fatalf("base URL = %q", transport.BaseURL())
	}
	if transport.Profile() != "custom" {
		t.Fatalf("profile = %q", transport.Profile())
	}
}

func isUUIDv4(value string) bool {
	parts := strings.Split(value, "-")
	if len(parts) != 5 {
		return false
	}
	lengths := []int{8, 4, 4, 4, 12}
	for index, part := range parts {
		if len(part) != lengths[index] {
			return false
		}
		for _, character := range part {
			switch {
			case character >= '0' && character <= '9':
			case character >= 'a' && character <= 'f':
			default:
				return false
			}
		}
	}
	return parts[2][0] == '4' && strings.ContainsRune("89ab", rune(parts[3][0]))
}
