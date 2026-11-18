package infoflow

import (
	"time"

	"github.com/dolfly/infoflow/bot"
	botcalendar "github.com/dolfly/infoflow/bot/calendar"
	"github.com/dolfly/infoflow/bot/dynamiccard"
	"github.com/dolfly/infoflow/bot/events"
	"github.com/dolfly/infoflow/bot/im"
	botmeeting "github.com/dolfly/infoflow/bot/meeting"
	"github.com/dolfly/infoflow/bot/robot"
	"github.com/dolfly/infoflow/bot/webhook"
	"github.com/dolfly/infoflow/bot/websocket"
	"github.com/dolfly/infoflow/internal/auth"
	dcinternal "github.com/dolfly/infoflow/internal/dynamiccard"
	internalruntime "github.com/dolfly/infoflow/internal/runtime"
	"github.com/dolfly/infoflow/internal/transport"
	"github.com/dolfly/infoflow/user"
	"github.com/dolfly/infoflow/userauthorization"
)

// Client is the InfoFlow SDK facade. IM contains messaging capabilities.
type Client struct {
	Bot               bot.Scope
	User              user.Scope
	UserAuthorization userauthorization.Service
	config            runtimeConfig
}

type clientOptions struct {
	now func() time.Time
}

// NewClient validates and copies configuration, then assembles the SDK
// runtime. It performs no network request.
func NewClient(cfg Config) (*Client, error) {
	return newClientWithOptions(cfg, clientOptions{now: time.Now})
}

func newClientWithOptions(cfg Config, options clientOptions) (*Client, error) {
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	now := options.now
	if now == nil {
		now = time.Now
	}
	transportClient := transport.New(
		normalized.baseURL,
		normalized.connectTimeout,
		normalized.requestTimeout,
		normalized.httpClient,
		normalized.accessChannel,
	)
	tokens := auth.NewTokenManager(
		transportClient,
		normalized.appKey,
		normalized.appSecret,
		normalized.tokenRefreshSkew,
		now,
	)
	message := im.NewMessageService(transportClient, tokens, internalruntime.NewMessageIDGenerator(now))
	group := im.NewGroupService(transportClient, tokens)
	bosTransport := transport.New(im.BOSGatewayBaseURL, normalized.connectTimeout, normalized.requestTimeout, normalized.httpClient, normalized.accessChannel)
	file := im.NewFileService(transportClient, bosTransport, tokens)
	logIDs := internalruntime.NewMessageIDGenerator(now)
	summary := im.NewMessageSummaryService(transportClient, tokens, logIDs, now)
	robotService := robot.NewService(transportClient, tokens, logIDs)
	applicationBase := normalized.baseURL
	if !normalized.baseURLExplicit {
		applicationBase = DefaultQyOpenAPIBaseURL
	}
	applicationTransport := transport.New(applicationBase, normalized.connectTimeout, normalized.requestTimeout, normalized.httpClient, normalized.accessChannel)
	applicationTokens := auth.NewTokenManager(applicationTransport, normalized.appKey, normalized.appSecret, normalized.tokenRefreshSkew, now)
	calendarService := botcalendar.NewService(applicationTransport, applicationTokens, internalruntime.NewMessageIDGenerator(now))
	meetingService := botmeeting.NewService(applicationTransport, applicationTokens, internalruntime.NewMessageIDGenerator(now))
	dynamicCardTransport, err := dcinternal.NewTransport(dcinternal.TransportOptions{
		AppKey:         normalized.appKey,
		AppSecret:      normalized.appSecret,
		ConnectTimeout: normalized.connectTimeout,
		RequestTimeout: normalized.requestTimeout,
		RefreshSkew:    normalized.tokenRefreshSkew,
		HTTPClient:     normalized.httpClient,
	})
	if err != nil {
		return nil, err
	}
	dynamicCard := dynamiccard.NewService(dynamicCardTransport)
	dispatcher := events.NewDispatcher(events.Options{
		Observer: normalized.realtime.resolvedObserver(),
		OnError:  normalized.realtime.OnError,
		Now:      now,
	})
	observer := normalized.realtime.resolvedObserver()
	webhookService := webhook.NewService(dispatcher, transportClient, tokens, logIDs, observer, normalized.realtime.OnError)
	websocketService := websocket.NewService(dispatcher, transportClient, tokens, logIDs, observer, normalized.realtime.OnError, normalized.appKey, normalized.appSecret, normalized.httpClient)
	userScope, authorization := user.NewRuntime(normalized.user, user.RuntimeOptions{
		ConnectTimeout: normalized.connectTimeout, RequestTimeout: normalized.requestTimeout,
		HTTPClient: normalized.httpClient, AccessChannel: normalized.accessChannel, Tokens: tokens,
	})
	return &Client{
		Bot: bot.Scope{
			Calendar: calendarService, IM: im.Service{Message: message, Group: group, File: file, Menu: robot.NewMenuService(transportClient, tokens), DynamicCard: dynamicCard}, Meeting: meetingService, MessageSummary: summary, Robot: robotService,
			Events: dispatcher, Webhook: webhookService, WebSocket: websocketService,
		},
		User:              userScope,
		UserAuthorization: authorization,
		config:            normalized,
	}, nil
}
