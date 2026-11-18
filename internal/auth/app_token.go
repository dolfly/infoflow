package auth

import (
	"context"
	"crypto/md5" //nolint:gosec // the platform protocol requires an MD5 secret digest
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/dolfly/infoflow/internal/envelope"
	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/transport"
)

// AppTokenRoute is the platform route that issues an app_access_token.
const AppTokenRoute = "/auth/app_access_token"

// AppSecretDigest returns the lowercase MD5 hex digest the platform expects in
// the app_secret field. The digest is a credential: never log it.
func AppSecretDigest(secret string) string {
	digest := md5.Sum([]byte(secret)) //nolint:gosec // platform protocol
	return strings.ToLower(hex.EncodeToString(digest[:]))
}

// FetchAppAccessToken performs exactly one POST {base}/auth/app_access_token
// through client and decodes the token envelope. headers are route-scoped
// request headers (for example LOGID and the dynamic-card content type); a nil
// header set keeps the legacy request encoding. The caller owns caching and
// refresh.
func FetchAppAccessToken(
	ctx context.Context,
	client *transport.Client,
	appKey string,
	appSecret string,
	headers http.Header,
) (string, time.Duration, error) {
	raw, err := client.PostJSONWithHeaders(ctx, AppTokenRoute, tokenRequest{
		AppKey:    appKey,
		AppSecret: AppSecretDigest(appSecret),
	}, "", headers)
	if err != nil {
		return "", 0, err
	}
	env, err := envelope.Decode(raw)
	if err != nil {
		return "", 0, err
	}
	if env.Code != "ok" {
		return "", 0, sdkerrors.NewAuth(env.Code, env.Message, raw)
	}
	var data tokenData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return "", 0, sdkerrors.NewMalformed("authentication data is invalid", raw, err)
	}
	if strings.TrimSpace(data.AppAccessToken) == "" || data.Expire <= 0 {
		return "", 0, sdkerrors.NewMalformed("authentication data is incomplete", raw, nil)
	}
	return data.AppAccessToken, time.Duration(data.Expire) * time.Second, nil
}
