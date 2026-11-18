// Package dynamiccard holds the unexported transport, envelope, token and
// sequencing machinery shared by the dynamic-card (scheme=1) capability. The
// public content, error and response models live in bot/dynamiccard; later
// delivery units build the send/stream/update/ack methods on top of this
// package.
package dynamiccard

import (
	"os"
	"strings"
)

// ProfileQyOpenAPI is the open-platform base profile shared by the dynamic-card
// routes. Tokens are cached per profile.
const ProfileQyOpenAPI = "qyopenapi"

// Base URLs of the qyopenapi profile. Production is the default; gray and
// preonline exist for owner-approved integration work only.
const (
	DefaultBaseURL   = "https://open-ruliu.baidu-int.com/open-plat/api/v1"
	GrayBaseURL      = "https://open-ruliu-gray.baidu-int.com/open-plat/api/gray/v1"
	PreonlineBaseURL = "http://xplatform-preonline.dev.weiyun.baidu.com/open-plat/api/devp/v1"
)

// BaseURLEnvVar overrides the base URL when no explicit value is configured.
const BaseURLEnvVar = "INFOFLOW_DYNAMIC_CARD_BASE_URL"

// Dynamic-card route paths, all bound to the qyopenapi profile.
const (
	RouteToken           = "/auth/app_access_token"
	RouteSend            = "/app/message/sendUnifiedMsg"
	RouteModifyStreaming = "/interactive/message/modifyStreaming"
	RouteStreamingAppend = "/interactive/message/streaming/append"
	RouteUpdate          = "/interactive/message/update"
	RouteCardEventAck    = "/interactive/message/card_event_ack"
)

// Request header names and fixed values required by the new routes.
const (
	HeaderAuthorization = "Authorization"
	HeaderContentType   = "Content-Type"
	HeaderLogID         = "LOGID"
	HeaderAccessChannel = "X-OP-ACCESS-CHANNEL"
	ContentTypeJSON     = "application/json; charset=utf-8"
	AccessChannel       = "go_sdk"
	AuthorizationScheme = "Bearer-"
)

// ResolveBaseURL returns the dynamic-card base URL. The override order is
// explicit configuration, then INFOFLOW_DYNAMIC_CARD_BASE_URL, then the
// production default.
func ResolveBaseURL(explicit string) string {
	return resolveBaseURL(explicit, os.Getenv)
}

func resolveBaseURL(explicit string, getenv func(string) string) string {
	if value := strings.TrimSpace(explicit); value != "" {
		return strings.TrimRight(value, "/")
	}
	if getenv != nil {
		if value := strings.TrimSpace(getenv(BaseURLEnvVar)); value != "" {
			return strings.TrimRight(value, "/")
		}
	}
	return DefaultBaseURL
}
