package infoflow

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dolfly/infoflow/bot/events"
	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/user"
)

const DefaultBaseURL = "https://api.im.baidu.com/api/v1"
const DefaultQyOpenAPIBaseURL = "https://open-ruliu.baidu-int.com/open-plat/api/v1"

const (
	defaultConnectTimeout   = 10 * time.Second
	defaultRequestTimeout   = 30 * time.Second
	defaultTokenRefreshSkew = 60 * time.Second
)

// Config contains app credentials and optional runtime overrides.
//
// Duration fields use the Go zero value to select their documented defaults.
// Negative durations are invalid. When HTTPClient is supplied, the caller
// owns that client's connection, proxy, and TLS policy; RequestTimeout still
// applies as an SDK request deadline.
type Config struct {
	AppKey    string
	AppSecret string
	BaseURL   string
	// AccessChannel is the source prefix sent in the X-OP-ACCESS-CHANNEL
	// header on every HTTP request (including the app token request). When
	// blank it defaults to "go_sdk"; explicit values normalize to
	// "{prefix}_go_sdk" (idempotent when already suffixed).
	AccessChannel    string
	ConnectTimeout   time.Duration
	RequestTimeout   time.Duration
	TokenRefreshSkew time.Duration
	HTTPClient       *http.Client
	Realtime         RealtimeConfig
	User             user.Config
}

// RealtimeConfig configures only the additive T4 runtime. Existing L1 calls
// remain silent and do not emit these diagnostics.
type RealtimeConfig struct {
	Observer    events.Observer
	Diagnostics events.DiagnosticHandler
	OnError     events.ErrorHandler
}

func (config RealtimeConfig) resolvedObserver() events.Observer {
	return combineObservers(config.Observer, config.Diagnostics)
}

type observerGroup []events.Observer

func (group observerGroup) Observe(diagnostic events.Diagnostic) {
	for _, observer := range group {
		observer.Observe(diagnostic)
	}
}

func combineObservers(observers ...events.Observer) events.Observer {
	group := make(observerGroup, 0, len(observers))
	for _, observer := range observers {
		if observer != nil {
			group = append(group, observer)
		}
	}
	if len(group) == 0 {
		return nil
	}
	if len(group) == 1 {
		return group[0]
	}
	return group
}

type runtimeConfig struct {
	appKey           string
	appSecret        string
	baseURL          string
	baseURLExplicit  bool
	accessChannel    string
	connectTimeout   time.Duration
	requestTimeout   time.Duration
	tokenRefreshSkew time.Duration
	httpClient       *http.Client
	realtime         RealtimeConfig
	user             user.Config
}

func normalizeConfig(cfg Config) (runtimeConfig, error) {
	if strings.TrimSpace(cfg.AppKey) == "" {
		return runtimeConfig{}, sdkerrors.NewConfig("AppKey", "must not be blank")
	}
	if strings.TrimSpace(cfg.AppSecret) == "" {
		return runtimeConfig{}, sdkerrors.NewConfig("AppSecret", "must not be blank")
	}
	if cfg.ConnectTimeout < 0 {
		return runtimeConfig{}, sdkerrors.NewConfig("ConnectTimeout", "must not be negative")
	}
	if cfg.RequestTimeout < 0 {
		return runtimeConfig{}, sdkerrors.NewConfig("RequestTimeout", "must not be negative")
	}
	if cfg.TokenRefreshSkew < 0 {
		return runtimeConfig{}, sdkerrors.NewConfig("TokenRefreshSkew", "must not be negative")
	}

	baseURLExplicit := strings.TrimSpace(cfg.BaseURL) != ""
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	} else if strings.TrimSpace(baseURL) != baseURL || baseURL == "" {
		return runtimeConfig{}, sdkerrors.NewConfig("BaseURL", "must be an absolute HTTP(S) URL")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return runtimeConfig{}, sdkerrors.NewConfig("BaseURL", "must be an absolute HTTP(S) URL without user info, query, or fragment")
	}

	connectTimeout := cfg.ConnectTimeout
	if connectTimeout == 0 {
		connectTimeout = defaultConnectTimeout
	}
	requestTimeout := cfg.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = defaultRequestTimeout
	}
	refreshSkew := cfg.TokenRefreshSkew
	if refreshSkew == 0 {
		refreshSkew = defaultTokenRefreshSkew
	}
	accessChannel := strings.TrimSpace(cfg.AccessChannel)
	if accessChannel == "" {
		accessChannel = "go_sdk"
	} else if accessChannel != "go_sdk" && !strings.HasSuffix(accessChannel, "_go_sdk") {
		accessChannel += "_go_sdk"
	}

	return runtimeConfig{
		appKey:           cfg.AppKey,
		appSecret:        cfg.AppSecret,
		baseURL:          strings.TrimRight(baseURL, "/"),
		baseURLExplicit:  baseURLExplicit,
		accessChannel:    accessChannel,
		connectTimeout:   connectTimeout,
		requestTimeout:   requestTimeout,
		tokenRefreshSkew: refreshSkew,
		httpClient:       cfg.HTTPClient,
		realtime:         cfg.Realtime,
		user:             cfg.User,
	}, nil
}
