package dynamiccard

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dolfly/infoflow/internal/auth"
	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/transport"
)

// Transport posts dynamic-card requests to one qyopenapi base profile. It is
// the only place that stamps the new-route headers, fetches the profile token
// and applies the envelope decision; later units add the public send, stream,
// update and ack methods on top of it.
type Transport struct {
	profile   string
	baseURL   string
	appKey    string
	appSecret string

	client         *transport.Client
	tokens         *TokenCache
	httpClient     *http.Client
	connectTimeout time.Duration
	requestTimeout time.Duration
}

// TransportOptions configures a dynamic-card transport. AppKey and AppSecret are
// required; BaseURL overrides the environment variable and the production
// default; Profile defaults to ProfileQyOpenAPI.
type TransportOptions struct {
	AppKey         string
	AppSecret      string
	BaseURL        string
	Profile        string
	HTTPClient     *http.Client
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
	RefreshSkew    time.Duration
	Now            func() time.Time
}

// NewTransport builds a transport without performing any network request.
func NewTransport(options TransportOptions) (*Transport, error) {
	if strings.TrimSpace(options.AppKey) == "" {
		return nil, sdkerrors.NewConfig("appKey", "must not be blank")
	}
	if strings.TrimSpace(options.AppSecret) == "" {
		return nil, sdkerrors.NewConfig("appSecret", "must not be blank")
	}
	profile := strings.TrimSpace(options.Profile)
	if profile == "" {
		profile = ProfileQyOpenAPI
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	requestTimeout := options.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 30 * time.Second
	}
	connectTimeout := options.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = 10 * time.Second
	}
	baseURL := ResolveBaseURL(options.BaseURL)
	value := &Transport{
		profile:        profile,
		baseURL:        baseURL,
		appKey:         options.AppKey,
		appSecret:      options.AppSecret,
		client:         transport.New(baseURL, connectTimeout, requestTimeout, options.HTTPClient, AccessChannel),
		httpClient:     options.HTTPClient,
		connectTimeout: connectTimeout,
		requestTimeout: requestTimeout,
	}
	value.tokens = NewTokenCache(value, options.RefreshSkew, now)
	return value, nil
}

// Profile returns the base profile id used for token caching.
func (t *Transport) Profile() string {
	return t.profile
}

// BaseURL returns the resolved route base URL.
func (t *Transport) BaseURL() string {
	return t.baseURL
}

// Token returns the cached profile token, refreshing it when needed.
func (t *Transport) Token(ctx context.Context) (string, error) {
	return t.tokens.Token(ctx, t.profile, t.baseURL)
}

// FetchAppAccessToken implements TokenFetcher for the token cache. The token
// request carries the same content type, channel and LOGID headers as any other
// new-route request, and its body never contains the plain app secret.
func (t *Transport) FetchAppAccessToken(ctx context.Context, baseURL string) (string, time.Duration, error) {
	logID, err := NewLogID()
	if err != nil {
		return "", 0, err
	}
	client := t.client
	if baseURL != t.baseURL {
		client = transport.New(baseURL, t.connectTimeout, t.requestTimeout, t.httpClient, AccessChannel)
	}
	return auth.FetchAppAccessToken(ctx, client, t.appKey, t.appSecret, NewHeaders("", logID))
}

// PostJSON performs one authenticated new-route POST and returns the envelope
// decision. A non-2xx response that still carries a body is decoded and
// returned with HTTPStatus set, so callers classify it through the same error
// model instead of guessing from the status alone. Transport failures without
// a body are returned as errors.
func (t *Transport) PostJSON(ctx context.Context, route string, payload any) (Decision, error) {
	token, err := t.Token(ctx)
	if err != nil {
		return Decision{}, err
	}
	return t.post(ctx, route, payload, token)
}

// PostUnauthenticatedJSON performs one anonymous new-route POST. It exists for
// routes such as the token endpoint that must not send an Authorization header.
func (t *Transport) PostUnauthenticatedJSON(ctx context.Context, route string, payload any) (Decision, error) {
	return t.post(ctx, route, payload, "")
}

func (t *Transport) post(ctx context.Context, route string, payload any, token string) (Decision, error) {
	logID, err := NewLogID()
	if err != nil {
		return Decision{}, err
	}
	raw, err := t.client.PostJSONWithHeaders(ctx, route, payload, token, NewHeaders(token, logID))
	if err != nil {
		var httpErr *sdkerrors.HTTPError
		if errors.As(err, &httpErr) && len(httpErr.Body()) > 0 {
			decision := Decide(httpErr.Body()).WithHTTPStatus(httpErr.StatusCode)
			decision.LogID = logID
			return decision, nil
		}
		return Decision{}, err
	}
	decision := Decide(raw).WithHTTPStatus(http.StatusOK)
	decision.LogID = logID
	return decision, nil
}
