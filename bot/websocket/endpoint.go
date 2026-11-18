package websocket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dolfly/infoflow/bot/internal/realtime"
)

const (
	DefaultGateway         = "infoflow-open-gateway.baidu.com"
	defaultEndpointTimeout = 30 * time.Second
	maxEndpointResponse    = 1 << 20
)

// Endpoint is the short-lived Phase-1 allocation result. URL contains a
// one-time credential and must never be persisted or logged.
type Endpoint struct {
	URL    string
	Config ServerConfig
}

// ServerConfig contains optional service policy. Durations are converted from
// the endpoint response's seconds into native Go durations.
type ServerConfig struct {
	ReconnectCount    *int
	ReconnectInterval *time.Duration
	ReconnectNonce    *time.Duration
	PingInterval      *time.Duration
}

// EndpointAllocator allocates a fresh one-time endpoint for every attempt.
type EndpointAllocator interface {
	Allocate(context.Context) (Endpoint, error)
}

// EndpointAllocatorFunc adapts a function to EndpointAllocator.
type EndpointAllocatorFunc func(context.Context) (Endpoint, error)

func (function EndpointAllocatorFunc) Allocate(ctx context.Context) (Endpoint, error) {
	return function(ctx)
}

// EndpointOptions configures the dedicated raw-app-secret Phase-1 client.
type EndpointOptions struct {
	AppKey     string
	AppSecret  string
	Gateway    string
	Timeout    time.Duration
	HTTPClient *http.Client
	Observer   realtime.Observer
	LookupEnv  func(string) (string, bool)
	Now        func() time.Time
}

// EndpointError is intentionally closed and never retains an HTTP response,
// endpoint URL, query, credential, or underlying error string.
type EndpointError struct {
	Category string
}

func (e *EndpointError) Error() string {
	return "InfoFlow WebSocket endpoint allocation failed (" + e.Category + ")"
}

type httpEndpointAllocator struct {
	appKey     string
	appSecret  string
	requestURL string
	timeout    time.Duration
	httpClient *http.Client
	observer   realtime.Observer
	now        func() time.Time
}

// NewEndpointAllocator validates configuration without network I/O.
func NewEndpointAllocator(options EndpointOptions) (EndpointAllocator, error) {
	if strings.TrimSpace(options.AppKey) == "" {
		return nil, errors.New("websocket AppKey must not be blank")
	}
	if strings.TrimSpace(options.AppSecret) == "" {
		return nil, errors.New("websocket AppSecret must not be blank")
	}
	if options.Timeout < 0 {
		return nil, errors.New("websocket endpoint timeout must not be negative")
	}
	lookupEnv := options.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	gateway := options.Gateway
	if gateway == "" {
		gateway, _ = lookupEnv("INFOFLOW_WS_GATEWAY")
	}
	if gateway == "" {
		gateway = DefaultGateway
	}
	if strings.TrimSpace(gateway) != gateway {
		return nil, errors.New("websocket gateway must be a domain or domain:port")
	}
	parsed, err := url.Parse("https://" + gateway)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return nil, errors.New("websocket gateway must be a domain or domain:port")
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = defaultEndpointTimeout
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &httpEndpointAllocator{
		appKey: options.AppKey, appSecret: options.AppSecret,
		requestURL: "https://" + parsed.Host + "/open/ws/endpoint",
		timeout:    timeout, httpClient: &clientCopy, observer: options.Observer, now: now,
	}, nil
}

func (allocator *httpEndpointAllocator) Allocate(ctx context.Context) (Endpoint, error) {
	if ctx == nil {
		return Endpoint{}, &EndpointError{Category: "invalid_context"}
	}
	ctx, requestID := realtime.EnsureRequestID(ctx)
	started := allocator.now()
	realtime.Emit(allocator.observer, realtime.Diagnostic{
		Event: "ws.endpoint.allocate.start", Level: "debug", Component: "websocket",
		Operation: "endpoint_allocate", Result: "started", RequestID: requestID, Timestamp: started,
	})
	requestContext, cancel := context.WithTimeout(ctx, allocator.timeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"app_key": allocator.appKey, "app_secret": allocator.appSecret})
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, allocator.requestURL, bytes.NewReader(body))
	if err != nil {
		return Endpoint{}, allocator.failure(requestID, started, "request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := allocator.httpClient.Do(request)
	if err != nil {
		category := "http"
		if requestContext.Err() != nil {
			category = "timeout"
		}
		return Endpoint{}, allocator.failure(requestID, started, category)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Endpoint{}, allocator.failure(requestID, started, "http_status")
	}
	var envelope struct {
		Code *int `json:"code"`
		Data *struct {
			URL          string `json:"url"`
			ClientConfig *struct {
				ReconnectCount    *int64 `json:"reconnect_count"`
				ReconnectInterval *int64 `json:"reconnect_interval"`
				ReconnectNonce    *int64 `json:"reconnect_nonce"`
				PingInterval      *int64 `json:"ping_interval"`
			} `json:"client_config"`
		} `json:"data"`
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxEndpointResponse+1))
	if err != nil || len(responseBody) > maxEndpointResponse {
		return Endpoint{}, allocator.failure(requestID, started, "malformed_response")
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	if err := decoder.Decode(&envelope); err != nil {
		return Endpoint{}, allocator.failure(requestID, started, "malformed_response")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Endpoint{}, allocator.failure(requestID, started, "malformed_response")
	}
	if envelope.Code == nil {
		return Endpoint{}, allocator.failure(requestID, started, "malformed_response")
	}
	if *envelope.Code != 0 {
		return Endpoint{}, allocator.failure(requestID, started, "business")
	}
	if envelope.Data == nil || strings.TrimSpace(envelope.Data.URL) == "" {
		return Endpoint{}, allocator.failure(requestID, started, "missing_url")
	}
	serverConfig, err := decodeServerConfig(envelope.Data.ClientConfig)
	if err != nil {
		return Endpoint{}, allocator.failure(requestID, started, "malformed_config")
	}
	realtime.Emit(allocator.observer, realtime.Diagnostic{
		Event: "ws.endpoint.allocate.success", Level: "info", Component: "websocket",
		Operation: "endpoint_allocate", Result: "success", RequestID: requestID,
		Timestamp: allocator.now(), ElapsedMS: allocator.now().Sub(started).Milliseconds(),
	})
	return Endpoint{URL: envelope.Data.URL, Config: serverConfig}, nil
}

func (allocator *httpEndpointAllocator) failure(requestID string, started time.Time, category string) error {
	realtime.Emit(allocator.observer, realtime.Diagnostic{
		Event: "ws.endpoint.allocate.failure", Level: "warn", Component: "websocket",
		Operation: "endpoint_allocate", Result: "failure", ErrorType: category,
		RequestID: requestID, Timestamp: allocator.now(), ElapsedMS: allocator.now().Sub(started).Milliseconds(),
	})
	return &EndpointError{Category: category}
}

func decodeServerConfig(config *struct {
	ReconnectCount    *int64 `json:"reconnect_count"`
	ReconnectInterval *int64 `json:"reconnect_interval"`
	ReconnectNonce    *int64 `json:"reconnect_nonce"`
	PingInterval      *int64 `json:"ping_interval"`
}) (ServerConfig, error) {
	if config == nil {
		return ServerConfig{}, nil
	}
	result := ServerConfig{}
	if config.ReconnectCount != nil {
		if *config.ReconnectCount < -1 || *config.ReconnectCount > int64(^uint(0)>>1) {
			return ServerConfig{}, errors.New("invalid reconnect count")
		}
		value := int(*config.ReconnectCount)
		result.ReconnectCount = &value
	}
	convert := func(seconds *int64) (*time.Duration, error) {
		if seconds == nil {
			return nil, nil
		}
		if *seconds < 0 || *seconds > int64((1<<63-1)/int64(time.Second)) {
			return nil, errors.New("invalid duration")
		}
		value := time.Duration(*seconds) * time.Second
		return &value, nil
	}
	var err error
	if result.ReconnectInterval, err = convert(config.ReconnectInterval); err != nil {
		return ServerConfig{}, err
	}
	if result.ReconnectNonce, err = convert(config.ReconnectNonce); err != nil {
		return ServerConfig{}, err
	}
	if result.PingInterval, err = convert(config.PingInterval); err != nil {
		return ServerConfig{}, err
	}
	return result, nil
}
