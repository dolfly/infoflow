package webhook

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dolfly/infoflow/bot/events"
	"github.com/dolfly/infoflow/bot/internal/callback"
	"github.com/dolfly/infoflow/bot/internal/realtime"
	"github.com/dolfly/infoflow/internal/auth"
	internalruntime "github.com/dolfly/infoflow/internal/runtime"
	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/transport"
)

// WebhookService creates receivers on the Client's shared dispatcher and
// changes callback mode through the existing authenticated runtime.
type Service struct {
	dispatcher *events.Dispatcher
	transport  *transport.Client
	tokens     *auth.TokenManager
	logIDs     *internalruntime.MessageIDGenerator
	observer   events.Observer
	onError    events.ErrorHandler
}

func NewService(dispatcher *events.Dispatcher, client *transport.Client, tokens *auth.TokenManager, logIDs *internalruntime.MessageIDGenerator, observer events.Observer, onError events.ErrorHandler) Service {
	return Service{dispatcher: dispatcher, transport: client, tokens: tokens, logIDs: logIDs, observer: observer, onError: onError}
}

func (s Service) NewReceiver(options Options) (*Receiver, error) {
	if options.Dispatcher == nil {
		options.Dispatcher = s.dispatcher
	}
	if options.Observer == nil && options.Diagnostics == nil {
		options.Observer = s.observer
	}
	if options.OnError == nil {
		options.OnError = s.onError
	}
	return NewReceiver(options)
}

type webhookRegistrationRequest struct {
	CallbackURL    string `json:"recallUrl"`
	Token          string `json:"token"`
	EncodingAESKey string `json:"encodingAesKey"`
	ReceiveState   int    `json:"receiveMsgState"`
}

func (s Service) Register(ctx context.Context, registration Registration) (RegistrationResult, error) {
	if ctx == nil {
		return RegistrationResult{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	callbackURL, err := url.Parse(registration.CallbackURL)
	if err != nil || callbackURL.Scheme != "https" || callbackURL.Host == "" || callbackURL.User != nil ||
		strings.TrimSpace(registration.CallbackURL) != registration.CallbackURL {
		return RegistrationResult{}, sdkerrors.NewConfig("CallbackURL", "must be an absolute HTTPS URL")
	}
	if strings.TrimSpace(registration.Token) == "" {
		return RegistrationResult{}, sdkerrors.NewConfig("Token", "must not be blank")
	}
	if strings.TrimSpace(registration.EncodingAESKey) == "" {
		return RegistrationResult{}, sdkerrors.NewConfig("EncodingAESKey", "must not be blank")
	}
	ctx, requestID := realtime.EnsureRequestID(ctx)
	observer := s.observer
	realtime.Emit(observer, realtime.Diagnostic{
		Level: "debug", Event: "webhook.registration.start", Component: "webhook",
		Operation: "registration", Route: "POST_robot_callback_update", Result: "started", RequestID: requestID,
	})
	token, err := s.tokens.Token(ctx)
	if err != nil {
		s.registrationFailure(ctx, "authentication")
		return RegistrationResult{}, err
	}
	logID := strconv.FormatInt(s.logIDs.Next(), 10)
	raw, err := s.transport.PostSensitiveJSONWithHeaders(
		ctx,
		"/imRobot/updateReCallUrl",
		webhookRegistrationRequest{
			CallbackURL: registration.CallbackURL, Token: registration.Token,
			EncodingAESKey: registration.EncodingAESKey, ReceiveState: 1,
		},
		token,
		http.Header{
			"Content-Type": []string{"application/json; charset=utf-8"},
			"LOGID":        []string{logID},
		},
	)
	if err != nil {
		s.registrationFailure(ctx, "http")
		return RegistrationResult{}, err
	}
	result, err := decodeCallbackRegistration(raw)
	if err != nil {
		s.registrationFailure(ctx, "business_or_malformed")
		return RegistrationResult{}, err
	}
	realtime.Emit(observer, realtime.Diagnostic{
		Level: "info", Event: "webhook.registration.success", Component: "webhook",
		Operation: "registration", Route: "POST_robot_callback_update", Result: "success", RequestID: requestID,
	})
	return result, nil
}

func (s Service) registrationFailure(ctx context.Context, category string) {
	runtimeError := realtime.RuntimeError{
		Component: "webhook", Operation: "registration", Category: category, Route: "POST_robot_callback_update",
	}
	realtime.Report(s.onError, runtimeError)
	realtime.Emit(s.observer, realtime.Diagnostic{
		Level: "warn", Event: "webhook.registration.failure", Component: "webhook",
		Operation: "registration", Route: "POST_robot_callback_update", Result: "failure", ErrorType: category,
		RequestID: realtime.RequestID(ctx),
	})
}

func decodeCallbackRegistration(raw []byte) (RegistrationResult, error) {
	decoded, err := callback.DecodeRegistration(raw)
	if err != nil {
		return RegistrationResult{}, err
	}
	return NewRegistrationResult(decoded.Code, decoded.Message, decoded.LogID, decoded.Raw), nil
}
