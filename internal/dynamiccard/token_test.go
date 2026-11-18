package dynamiccard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeTokenFetcher struct {
	mu       sync.Mutex
	calls    map[string]int
	token    string
	lifetime time.Duration
	delay    time.Duration
	err      error
}

func newFakeTokenFetcher() *fakeTokenFetcher {
	return &fakeTokenFetcher{calls: make(map[string]int), token: "token", lifetime: time.Hour}
}

func (f *fakeTokenFetcher) FetchAppAccessToken(ctx context.Context, baseURL string) (string, time.Duration, error) {
	f.mu.Lock()
	f.calls[baseURL]++
	delay, err, token, lifetime := f.delay, f.err, f.token, f.lifetime
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if err != nil {
		return "", 0, err
	}
	if token == "" {
		return "", lifetime, nil
	}
	return token + "@" + baseURL, lifetime, nil
}

func (f *fakeTokenFetcher) callCount(baseURL string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[baseURL]
}

// T-18: tokens are cached per profile and never shared across profiles.
func TestTokenCacheIsolatesProfiles(t *testing.T) {
	fetcher := newFakeTokenFetcher()
	cache := NewTokenCache(fetcher, 0, nil)
	ctx := context.Background()

	first, err := cache.Token(ctx, ProfileQyOpenAPI, "https://open.example/v1")
	if err != nil {
		t.Fatalf("token profile qyopenapi: %v", err)
	}
	second, err := cache.Token(ctx, "other-profile", "https://other.example/v1")
	if err != nil {
		t.Fatalf("token other profile: %v", err)
	}
	if first == second {
		t.Fatalf("profiles shared a token: %q", first)
	}
	if fetcher.callCount("https://open.example/v1") != 1 || fetcher.callCount("https://other.example/v1") != 1 {
		t.Fatalf("calls = %v, want one per profile", fetcher.calls)
	}

	again, err := cache.Token(ctx, ProfileQyOpenAPI, "https://open.example/v1")
	if err != nil {
		t.Fatalf("cached token: %v", err)
	}
	if again != first {
		t.Fatalf("cached token = %q, want %q", again, first)
	}
	if fetcher.callCount("https://open.example/v1") != 1 {
		t.Fatalf("cache hit refetched: %v", fetcher.calls)
	}
}

// T-18: concurrent requests for one profile trigger exactly one refresh.
func TestTokenCacheSingleFlight(t *testing.T) {
	fetcher := newFakeTokenFetcher()
	fetcher.delay = 40 * time.Millisecond
	cache := NewTokenCache(fetcher, 0, nil)

	const workers = 8
	var wait sync.WaitGroup
	tokens := make([]string, workers)
	errs := make([]error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(slot int) {
			defer wait.Done()
			tokens[slot], errs[slot] = cache.Token(context.Background(), ProfileQyOpenAPI, "https://open.example/v1")
		}(index)
	}
	wait.Wait()

	if calls := fetcher.callCount("https://open.example/v1"); calls != 1 {
		t.Fatalf("token fetches = %d, want 1", calls)
	}
	for index := 0; index < workers; index++ {
		if errs[index] != nil {
			t.Fatalf("worker %d: %v", index, errs[index])
		}
		if tokens[index] != tokens[0] {
			t.Fatalf("worker %d token = %q, want %q", index, tokens[index], tokens[0])
		}
	}
}

func TestTokenCacheRefreshesBeforeExpiry(t *testing.T) {
	fetcher := newFakeTokenFetcher()
	fetcher.lifetime = 100 * time.Second
	current := time.Unix(1_700_000_000, 0)
	cache := NewTokenCache(fetcher, 10*time.Second, func() time.Time { return current })

	if _, err := cache.Token(context.Background(), ProfileQyOpenAPI, "https://open.example/v1"); err != nil {
		t.Fatalf("initial token: %v", err)
	}
	current = current.Add(89 * time.Second) // 89+10 < 100: still fresh
	if _, err := cache.Token(context.Background(), ProfileQyOpenAPI, "https://open.example/v1"); err != nil {
		t.Fatalf("token within skew: %v", err)
	}
	if calls := fetcher.callCount("https://open.example/v1"); calls != 1 {
		t.Fatalf("fetches = %d, want 1", calls)
	}
	current = current.Add(6 * time.Second) // 95+10 >= 100: refresh
	if _, err := cache.Token(context.Background(), ProfileQyOpenAPI, "https://open.example/v1"); err != nil {
		t.Fatalf("refreshed token: %v", err)
	}
	if calls := fetcher.callCount("https://open.example/v1"); calls != 2 {
		t.Fatalf("fetches = %d, want 2", calls)
	}
}

func TestTokenCacheRefetchesForNewBase(t *testing.T) {
	fetcher := newFakeTokenFetcher()
	cache := NewTokenCache(fetcher, 0, nil)
	if _, err := cache.Token(context.Background(), ProfileQyOpenAPI, "https://a.example/v1"); err != nil {
		t.Fatalf("token for first base: %v", err)
	}
	token, err := cache.Token(context.Background(), ProfileQyOpenAPI, "https://b.example/v1")
	if err != nil {
		t.Fatalf("token for second base: %v", err)
	}
	if token != "token@https://b.example/v1" {
		t.Fatalf("token = %q, want the second base token", token)
	}
}

func TestTokenCacheRejectsInvalidInput(t *testing.T) {
	cache := NewTokenCache(newFakeTokenFetcher(), 0, nil)
	if _, err := cache.Token(context.Background(), " ", "https://open.example/v1"); err == nil {
		t.Fatal("blank profile returned no error")
	}
	if _, err := cache.Token(context.Background(), ProfileQyOpenAPI, " "); err == nil {
		t.Fatal("blank base URL returned no error")
	}
	var missing *TokenCache
	if _, err := missing.Token(context.Background(), ProfileQyOpenAPI, "https://open.example/v1"); err == nil {
		t.Fatal("nil cache returned no error")
	}
}

func TestTokenCachePropagatesFetchFailure(t *testing.T) {
	fetcher := newFakeTokenFetcher()
	fetcher.err = errors.New("network down")
	cache := NewTokenCache(fetcher, 0, nil)
	if _, err := cache.Token(context.Background(), ProfileQyOpenAPI, "https://open.example/v1"); err == nil {
		t.Fatal("fetch failure returned no error")
	}
}

func TestTokenCacheRejectsEmptyToken(t *testing.T) {
	fetcher := newFakeTokenFetcher()
	fetcher.token = ""
	cache := NewTokenCache(fetcher, 0, nil)
	if _, err := cache.Token(context.Background(), ProfileQyOpenAPI, "https://open.example/v1"); err == nil {
		t.Fatal("empty token returned no error")
	}
	fetcher.token = "ok"
	fetcher.lifetime = 0
	if _, err := cache.Token(context.Background(), ProfileQyOpenAPI, "https://open.example/v1"); err == nil {
		t.Fatal("zero lifetime returned no error")
	}
}
