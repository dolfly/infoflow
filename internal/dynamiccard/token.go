package dynamiccard

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/dolfly/infoflow/internal/sdkerrors"
)

// TokenFetcher performs exactly one app_access_token request against baseURL
// and returns the token together with its lifetime. Implementations must not
// cache: caching and single-flight are owned by TokenCache.
type TokenFetcher interface {
	FetchAppAccessToken(ctx context.Context, baseURL string) (string, time.Duration, error)
}

// TokenCache caches app_access_token per base profile. A token fetched for one
// profile is never reused for another profile, and concurrent requests for the
// same profile share one refresh.
type TokenCache struct {
	fetcher     TokenFetcher
	refreshSkew time.Duration
	now         func() time.Time

	mu      sync.Mutex
	entries map[string]*tokenEntry
}

type tokenEntry struct {
	mu        sync.Mutex
	baseURL   string
	token     string
	expiresAt time.Time
}

// NewTokenCache builds a profile-isolated token cache. now may be nil, in which
// case time.Now is used.
func NewTokenCache(fetcher TokenFetcher, refreshSkew time.Duration, now func() time.Time) *TokenCache {
	if now == nil {
		now = time.Now
	}
	return &TokenCache{
		fetcher:     fetcher,
		refreshSkew: refreshSkew,
		now:         now,
		entries:     make(map[string]*tokenEntry),
	}
}

// Token returns a cached token for profile, refreshing it through the base URL
// when it is missing, expired, or was issued for a different base. Holding the
// per-profile lock across the fetch makes concurrent callers single-flight.
func (c *TokenCache) Token(ctx context.Context, profile, baseURL string) (string, error) {
	if c == nil || c.fetcher == nil {
		return "", sdkerrors.NewConfig("dynamic card token", "token cache is not initialized")
	}
	if strings.TrimSpace(profile) == "" {
		return "", sdkerrors.NewConfig("dynamic card profile", "must not be blank")
	}
	if strings.TrimSpace(baseURL) == "" {
		return "", sdkerrors.NewConfig("dynamic card base URL", "must not be blank")
	}

	entry := c.entryFor(profile)
	entry.mu.Lock()
	defer entry.mu.Unlock()

	now := c.now()
	if entry.token != "" && entry.baseURL == baseURL && now.Add(c.refreshSkew).Before(entry.expiresAt) {
		return entry.token, nil
	}

	token, lifetime, err := c.fetcher.FetchAppAccessToken(ctx, baseURL)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(token) == "" || lifetime <= 0 {
		return "", sdkerrors.NewMalformed("dynamic card token response is incomplete", nil, nil)
	}
	entry.token = token
	entry.baseURL = baseURL
	entry.expiresAt = now.Add(lifetime)
	return token, nil
}

func (c *TokenCache) entryFor(profile string) *tokenEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[profile]
	if !ok {
		entry = &tokenEntry{}
		c.entries[profile] = entry
	}
	return entry
}
