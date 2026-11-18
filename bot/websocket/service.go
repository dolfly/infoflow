package websocket

import (
	"context"
	"net/http"
	"regexp"
	"strconv"

	"github.com/dolfly/infoflow/bot/events"
	"github.com/dolfly/infoflow/bot/internal/callback"
	"github.com/dolfly/infoflow/bot/internal/realtime"
	"github.com/dolfly/infoflow/internal/auth"
	internalruntime "github.com/dolfly/infoflow/internal/runtime"
	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/transport"
)

// WebSocketService creates independent sessions bound to the Client's shared
// dispatcher, credentials, callback registrar, and redaction policy.
type Service struct {
	dispatcher    *events.Dispatcher
	transport     *transport.Client
	tokens        *auth.TokenManager
	logIDs        *internalruntime.MessageIDGenerator
	observer      events.Observer
	onError       events.ErrorHandler
	appKey        string
	appSecret     string
	httpClient    *http.Client
	dialerFactory func(*http.Client) Dialer
}

// NewSession performs configuration validation only; Connect performs Phase
// 1 and Phase 2. Advanced fakes supplied in options take precedence.
func NewService(dispatcher *events.Dispatcher, client *transport.Client, tokens *auth.TokenManager, logIDs *internalruntime.MessageIDGenerator, observer events.Observer, onError events.ErrorHandler, appKey, appSecret string, httpClient *http.Client) Service {
	return Service{dispatcher: dispatcher, transport: client, tokens: tokens, logIDs: logIDs, observer: observer, onError: onError, appKey: appKey, appSecret: appSecret, httpClient: httpClient, dialerFactory: func(client *http.Client) Dialer { return NewCoderDialer(client) }}
}

func (service Service) NewSession(options SessionOptions) (*Session, error) {
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = service.httpClient
	}
	if options.Dispatcher == nil {
		options.Dispatcher = service.dispatcher
	}
	if options.Observer == nil && options.Diagnostics == nil {
		options.Observer = service.observer
	}
	if options.OnError == nil {
		options.OnError = service.onError
	}
	if options.EndpointAllocator == nil {
		observer := realtime.Observers(options.Observer, options.Diagnostics)
		allocator, err := NewEndpointAllocator(EndpointOptions{
			AppKey: service.appKey, AppSecret: service.appSecret, Gateway: options.Gateway,
			Timeout: options.EndpointTimeout, HTTPClient: httpClient,
			Observer: observer, LookupEnv: options.LookupEnv, Now: options.Now,
		})
		if err != nil {
			return nil, err
		}
		options.EndpointAllocator = allocator
	}
	if options.Dialer == nil {
		options.Dialer = service.dialerFactory(httpClient)
	}
	if options.Registrar == nil {
		options.Registrar = websocketModeRegistrar{service: service}
	}
	return NewSession(options)
}

// Register synchronously switches the platform callback mode to WebSocket.
// NewSession performs the same call automatically by default; this explicit
// form is useful when an operator must order registration and later restore a
// previous callback mode without a delayed registration race.
func (service Service) Register(ctx context.Context, callbackToken string) error {
	return (websocketModeRegistrar{service: service}).RegisterWebSocket(ctx, callbackToken)
}

type websocketModeRegistrar struct {
	service Service
}

type websocketRegistrationRequest struct {
	EnableWebSocket int    `json:"enWebSocket"`
	ReceiveState    int    `json:"receiveMsgState"`
	Token           string `json:"token"`
}

var websocketCallbackToken = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (registrar websocketModeRegistrar) RegisterWebSocket(ctx context.Context, callbackToken string) error {
	if ctx == nil {
		return sdkerrors.NewConfig("context", "must not be nil")
	}
	if !websocketCallbackToken.MatchString(callbackToken) {
		return sdkerrors.NewConfig("callbackToken", "must be a 32-character lowercase hex value")
	}
	ctx, requestID := realtime.EnsureRequestID(ctx)
	observer := registrar.service.observer
	realtime.Emit(observer, realtime.Diagnostic{
		Event: "ws.registration.start", Level: "debug", Component: "websocket",
		Operation: "registration", Route: "POST_robot_callback_update", Result: "started", RequestID: requestID,
	})
	token, err := registrar.service.tokens.Token(ctx)
	if err != nil {
		registrar.failure(requestID, "authentication")
		return err
	}
	logID := strconv.FormatInt(registrar.service.logIDs.Next(), 10)
	raw, err := registrar.service.transport.PostSensitiveJSONWithHeaders(
		ctx,
		"/imRobot/updateReCallUrl",
		websocketRegistrationRequest{EnableWebSocket: 1, ReceiveState: 1, Token: callbackToken},
		token,
		http.Header{
			"Content-Type": []string{"application/json; charset=utf-8"},
			"LOGID":        []string{logID},
		},
	)
	if err != nil {
		registrar.failure(requestID, "http")
		return err
	}
	if _, err := callback.DecodeRegistration(raw); err != nil {
		registrar.failure(requestID, "business_or_malformed")
		return err
	}
	realtime.Emit(observer, realtime.Diagnostic{
		Event: "ws.registration.success", Level: "info", Component: "websocket",
		Operation: "registration", Route: "POST_robot_callback_update", Result: "success", RequestID: requestID,
	})
	return nil
}

func (registrar websocketModeRegistrar) failure(requestID, category string) {
	realtime.Report(registrar.service.onError, realtime.RuntimeError{
		Component: "websocket", Operation: "registration", Category: category,
		Route: "POST_robot_callback_update",
	})
	realtime.Emit(registrar.service.observer, realtime.Diagnostic{
		Event: "ws.registration.failure", Level: "warn", Component: "websocket",
		Operation: "registration", Route: "POST_robot_callback_update", Result: "failure",
		ErrorType: category, RequestID: requestID,
	})
}
