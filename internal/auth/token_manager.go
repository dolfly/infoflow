package auth

import (
	"context"
	"sync"
	"time"

	"github.com/dolfly/infoflow/internal/transport"
)

type TokenManager struct {
	transport   *transport.Client
	appKey      string
	appSecret   string
	refreshSkew time.Duration
	now         func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func NewTokenManager(
	transportClient *transport.Client,
	appKey string,
	appSecret string,
	refreshSkew time.Duration,
	now func() time.Time,
) *TokenManager {
	return &TokenManager{
		transport:   transportClient,
		appKey:      appKey,
		appSecret:   appSecret,
		refreshSkew: refreshSkew,
		now:         now,
	}
}

func (m *TokenManager) Token(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	if m.token != "" && now.Add(m.refreshSkew).Before(m.expiresAt) {
		return m.token, nil
	}
	token, expiresAt, err := m.fetch(ctx, now)
	if err != nil {
		return "", err
	}
	m.token = token
	m.expiresAt = expiresAt
	return token, nil
}

type tokenRequest struct {
	AppKey    string `json:"app_key"`
	AppSecret string `json:"app_secret"`
}

type tokenData struct {
	AppAccessToken string `json:"app_access_token"`
	Expire         int64  `json:"expire"`
}

func (m *TokenManager) fetch(ctx context.Context, now time.Time) (string, time.Time, error) {
	token, expiresIn, err := FetchAppAccessToken(ctx, m.transport, m.appKey, m.appSecret, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, now.Add(expiresIn), nil
}
