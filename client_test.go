package infoflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dolfly/infoflow/bot/im"
)

const (
	fixtureAppKey    = "fixture-app"
	fixtureSecret    = "fixture-secret"
	fixtureSecretMD5 = "4c7400aa9bc33b3a304a208e880bac23"
	fixtureToken     = "fixture-token"
)

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type countingRoundTripper struct {
	requests atomic.Int32
}

func (r *countingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	r.requests.Add(1)
	return nil, errors.New("unexpected network request")
}

func newFixtureClient(t *testing.T, server *httptest.Server, now func() time.Time, overrides ...func(*Config)) *Client {
	t.Helper()
	cfg := Config{
		AppKey:     fixtureAppKey,
		AppSecret:  fixtureSecret,
		BaseURL:    server.URL + "/api/v1",
		HTTPClient: server.Client(),
	}
	for _, override := range overrides {
		override(&cfg)
	}
	client, err := newClientWithOptions(cfg, clientOptions{now: now})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func respondWithToken(w http.ResponseWriter, expire int64) {
	writeJSON(w, http.StatusOK, fmt.Sprintf(`{"code":"ok","data":{"app_access_token":"%s","expire":%d}}`, fixtureToken, expire))
}

func readRequestBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read request body: %v", err)
	}
	return body
}

func assertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue any
	var wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Errorf("request body is not JSON: %v; body=%q", err, got)
		return
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("invalid expected JSON: %v", err)
	}
	gotCanonical, _ := json.Marshal(gotValue)
	wantCanonical, _ := json.Marshal(wantValue)
	if string(gotCanonical) != string(wantCanonical) {
		t.Errorf("JSON body = %s, want %s", gotCanonical, wantCanonical)
	}
}

func assertConfigError(t *testing.T, err error) {
	t.Helper()
	var target *ConfigError
	if !errors.As(err, &target) {
		t.Fatalf("error = %T %v, want *ConfigError", err, err)
	}
}

func assertErrorStringExcludes(t *testing.T, err error, sensitiveValues ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want typed failure")
	}
	message := err.Error()
	for _, sensitive := range sensitiveValues {
		if sensitive != "" && strings.Contains(message, sensitive) {
			t.Errorf("typed error string %q contains sensitive value %q", message, sensitive)
		}
	}
}

func TestNewClientValidationAndDefaults(t *testing.T) {
	roundTripper := &countingRoundTripper{}
	httpClient := &http.Client{Transport: roundTripper}

	tests := []struct {
		name string
		cfg  Config
	}{
		{"blank app key", Config{AppKey: "  ", AppSecret: fixtureSecret, HTTPClient: httpClient}},
		{"blank app secret", Config{AppKey: fixtureAppKey, AppSecret: "\t", HTTPClient: httpClient}},
		{"invalid base URL", Config{AppKey: fixtureAppKey, AppSecret: fixtureSecret, BaseURL: "://bad", HTTPClient: httpClient}},
		{"base URL missing host", Config{AppKey: fixtureAppKey, AppSecret: fixtureSecret, BaseURL: "https:///api/v1", HTTPClient: httpClient}},
		{"negative connect timeout", Config{AppKey: fixtureAppKey, AppSecret: fixtureSecret, ConnectTimeout: -time.Second, HTTPClient: httpClient}},
		{"negative request timeout", Config{AppKey: fixtureAppKey, AppSecret: fixtureSecret, RequestTimeout: -time.Second, HTTPClient: httpClient}},
		{"negative refresh skew", Config{AppKey: fixtureAppKey, AppSecret: fixtureSecret, TokenRefreshSkew: -time.Second, HTTPClient: httpClient}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(tt.cfg)
			if client != nil {
				t.Fatalf("NewClient() client = %#v, want nil", client)
			}
			assertConfigError(t, err)
		})
	}
	if got := roundTripper.requests.Load(); got != 0 {
		t.Fatalf("invalid config made %d requests, want 0", got)
	}

	client, err := NewClient(Config{AppKey: fixtureAppKey, AppSecret: fixtureSecret, HTTPClient: httpClient})
	if err != nil {
		t.Fatalf("NewClient(defaults) error = %v", err)
	}
	if client.config.baseURL != DefaultBaseURL {
		t.Errorf("base URL = %q, want %q", client.config.baseURL, DefaultBaseURL)
	}
	if client.config.connectTimeout != 10*time.Second {
		t.Errorf("connect timeout = %v, want 10s", client.config.connectTimeout)
	}
	if client.config.requestTimeout != 30*time.Second {
		t.Errorf("request timeout = %v, want 30s", client.config.requestTimeout)
	}
	if client.config.tokenRefreshSkew != 60*time.Second {
		t.Errorf("refresh skew = %v, want 60s", client.config.tokenRefreshSkew)
	}

	override, err := NewClient(Config{
		AppKey:           fixtureAppKey,
		AppSecret:        fixtureSecret,
		BaseURL:          "https://example.test/custom/api/v1/",
		ConnectTimeout:   2 * time.Second,
		RequestTimeout:   3 * time.Second,
		TokenRefreshSkew: 4 * time.Second,
		HTTPClient:       httpClient,
	})
	if err != nil {
		t.Fatalf("NewClient(overrides) error = %v", err)
	}
	if override.config.baseURL != "https://example.test/custom/api/v1" ||
		override.config.connectTimeout != 2*time.Second ||
		override.config.requestTimeout != 3*time.Second ||
		override.config.tokenRefreshSkew != 4*time.Second {
		t.Fatalf("normalized overrides = %#v", override.config)
	}
}

func TestConfigScopesObservabilityToRealtime(t *testing.T) {
	wantFields := []string{
		"AppKey",
		"AppSecret",
		"BaseURL",
		"AccessChannel",
		"ConnectTimeout",
		"RequestTimeout",
		"TokenRefreshSkew",
		"HTTPClient",
		"Realtime",
		"User",
	}
	typeOfConfig := reflect.TypeOf(Config{})
	if typeOfConfig.NumField() != len(wantFields) {
		t.Fatalf("Config has %d fields, want silent L1 surface %v", typeOfConfig.NumField(), wantFields)
	}
	for i, want := range wantFields {
		if got := typeOfConfig.Field(i).Name; got != want {
			t.Fatalf("Config field %d = %s, want %s", i, got, want)
		}
	}
}

func TestAccessChannelAppliesToTokenAndMessageRoutes(t *testing.T) {
	var mu sync.Mutex
	var tokenChannel, messageChannel []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/app_access_token":
			mu.Lock()
			tokenChannel = append(tokenChannel, r.Header.Get("X-OP-ACCESS-CHANNEL"))
			mu.Unlock()
			respondWithToken(w, 7200)
		case "/api/v1/app/message/send":
			mu.Lock()
			messageChannel = append(messageChannel, r.Header.Get("X-OP-ACCESS-CHANNEL"))
			mu.Unlock()
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"errmsg":"ok","msgkey":123}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	run := func(accessChannel string) {
		t.Helper()
		client := newFixtureClient(t, server, time.Now, func(cfg *Config) {
			cfg.AccessChannel = accessChannel
		})
		if _, err := client.Bot.IM.Message.SendToUser(context.Background(), "zhangsan", "Hello!"); err != nil {
			t.Fatalf("SendToUser() error = %v", err)
		}
	}

	// Default access channel is "go_sdk" for blank and whitespace values.
	run("")
	run("   ")
	// Explicit source prefixes normalize to "{prefix}_go_sdk", idempotently.
	run("myapp")
	run("myapp_go_sdk")
	run("go_sdk")

	mu.Lock()
	defer mu.Unlock()
	if len(tokenChannel) != 5 || len(messageChannel) != 5 {
		t.Fatalf("captured token=%v message=%v", tokenChannel, messageChannel)
	}
	if tokenChannel[0] != "go_sdk" {
		t.Errorf("token request X-OP-ACCESS-CHANNEL = %q, want go_sdk", tokenChannel[0])
	}
	if messageChannel[0] != "go_sdk" {
		t.Errorf("message request X-OP-ACCESS-CHANNEL = %q, want go_sdk", messageChannel[0])
	}
	if tokenChannel[1] != "go_sdk" {
		t.Errorf("whitespace token request X-OP-ACCESS-CHANNEL = %q, want go_sdk", tokenChannel[1])
	}
	if messageChannel[1] != "go_sdk" {
		t.Errorf("whitespace message request X-OP-ACCESS-CHANNEL = %q, want go_sdk", messageChannel[1])
	}
	if tokenChannel[2] != "myapp_go_sdk" {
		t.Errorf("custom token request X-OP-ACCESS-CHANNEL = %q, want myapp_go_sdk", tokenChannel[2])
	}
	if messageChannel[2] != "myapp_go_sdk" {
		t.Errorf("custom message request X-OP-ACCESS-CHANNEL = %q, want myapp_go_sdk", messageChannel[2])
	}
	if tokenChannel[3] != "myapp_go_sdk" {
		t.Errorf("suffixed token request X-OP-ACCESS-CHANNEL = %q, want myapp_go_sdk", tokenChannel[3])
	}
	if messageChannel[3] != "myapp_go_sdk" {
		t.Errorf("suffixed message request X-OP-ACCESS-CHANNEL = %q, want myapp_go_sdk", messageChannel[3])
	}
	if tokenChannel[4] != "go_sdk" {
		t.Errorf("go_sdk token request X-OP-ACCESS-CHANNEL = %q, want go_sdk", tokenChannel[4])
	}
	if messageChannel[4] != "go_sdk" {
		t.Errorf("go_sdk message request X-OP-ACCESS-CHANNEL = %q, want go_sdk", messageChannel[4])
	}
}

func TestLocalInputValidationMakesNoRequest(t *testing.T) {
	roundTripper := &countingRoundTripper{}
	client, err := NewClient(Config{
		AppKey:     fixtureAppKey,
		AppSecret:  fixtureSecret,
		HTTPClient: &http.Client{Transport: roundTripper},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	tests := []struct {
		name string
		call func() error
	}{
		{"blank user", func() error {
			_, err := client.Bot.IM.Message.SendToUser(context.Background(), " ", "hello")
			return err
		}},
		{"blank user text", func() error {
			_, err := client.Bot.IM.Message.SendToUser(context.Background(), "zhangsan", "\n\t")
			return err
		}},
		{"zero group", func() error {
			_, err := client.Bot.IM.Message.SendToGroup(context.Background(), 0, "hello")
			return err
		}},
		{"negative group", func() error {
			_, err := client.Bot.IM.Message.SendToGroup(context.Background(), -1, "hello")
			return err
		}},
		{"blank group text", func() error { _, err := client.Bot.IM.Message.SendToGroup(context.Background(), 1, " "); return err }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertConfigError(t, tt.call())
		})
	}
	if got := roundTripper.requests.Load(); got != 0 {
		t.Fatalf("local validation made %d requests, want 0", got)
	}
}

func TestSendToUserExactAuthPayloadAndRawCopy(t *testing.T) {
	var tokenRequests atomic.Int32
	var messageRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/app_access_token":
			tokenRequests.Add(1)
			if r.Method != http.MethodPost {
				t.Errorf("auth method = %s, want POST", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("auth Authorization = %q, want empty", got)
			}
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("auth Content-Type = %q", got)
			}
			assertJSONEqual(t, readRequestBody(t, r), `{"app_key":"fixture-app","app_secret":"`+fixtureSecretMD5+`"}`)
			respondWithToken(w, 7200)
		case "/api/v1/app/message/send":
			messageRequests.Add(1)
			if r.Method != http.MethodPost {
				t.Errorf("message method = %s, want POST", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer-"+fixtureToken {
				t.Errorf("Authorization = %q, want Bearer fixture", got)
			}
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("message Content-Type = %q", got)
			}
			if got := r.Header.Get("LOGID"); got != "" {
				t.Errorf("L1 message LOGID = %q, want absent", got)
			}
			if got := r.Header.Get("X-OP-ACCESS-CHANNEL"); got != "go_sdk" {
				t.Errorf("L1 message X-OP-ACCESS-CHANNEL = %q, want go_sdk", got)
			}
			assertJSONEqual(t, readRequestBody(t, r), `{"touser":"zhangsan","msgtype":"text","text":{"content":"Hello!"}}`)
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"errmsg":"ok","msgkey":123,"invaliduser":"","future":{"kept":true}}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newFixtureClient(t, server, time.Now)
	result, err := client.Bot.IM.Message.SendToUser(context.Background(), "zhangsan", "Hello!")
	if err != nil {
		t.Fatalf("SendToUser() error = %v", err)
	}
	if result.MessageKey != 123 || result.InvalidUser != "" {
		t.Fatalf("result = %#v", result)
	}
	first := result.RawJSON()
	if !strings.Contains(string(first), `"future":{"kept":true}`) {
		t.Fatalf("raw JSON did not retain unknown fields: %s", first)
	}
	first[0] = 'x'
	if second := result.RawJSON(); len(second) == 0 || second[0] != '{' {
		t.Fatalf("RawJSON() did not return a defensive copy: %q", second)
	}
	if tokenRequests.Load() != 1 || messageRequests.Load() != 1 {
		t.Fatalf("requests token=%d message=%d", tokenRequests.Load(), messageRequests.Load())
	}
}

func TestSendToUserAllowsMissingInvalidUser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/app_access_token":
			respondWithToken(w, 7200)
		case "/api/v1/app/message/send":
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"errmsg":"ok","msgkey":456}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newFixtureClient(t, server, time.Now)
	result, err := client.Bot.IM.Message.SendToUser(context.Background(), "zhangsan", "Hello!")
	if err != nil {
		t.Fatalf("SendToUser() error = %v", err)
	}
	if result.MessageKey != 456 || result.InvalidUser != "" {
		t.Fatalf("result = %#v, want MessageKey 456 and empty InvalidUser", result)
	}
	raw := result.RawJSON()
	if !strings.Contains(string(raw), `"msgkey":456`) {
		t.Fatalf("raw JSON = %s", raw)
	}
	raw[0] = 'x'
	if result.RawJSON()[0] != '{' {
		t.Fatal("RawJSON() shares caller-mutated storage")
	}
}

func TestSendToUserStillRequiresMessageKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/app_access_token":
			respondWithToken(w, 7200)
		case "/api/v1/app/message/send":
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"errmsg":"ok","invaliduser":""}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newFixtureClient(t, server, time.Now)
	_, err := client.Bot.IM.Message.SendToUser(context.Background(), "zhangsan", "Hello!")
	var target *MalformedResponseError
	if !errors.As(err, &target) {
		t.Fatalf("error = %T %v, want *MalformedResponseError", err, err)
	}
	raw := target.RawResponse()
	raw[0] = 'x'
	if target.RawResponse()[0] != '{' {
		t.Fatal("MalformedResponseError.RawResponse() shares caller-mutated storage")
	}
}

func TestSendToGroupExactPayloadAndTypedResult(t *testing.T) {
	fixed := time.UnixMilli(1569398477302)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/app_access_token":
			respondWithToken(w, 7200)
		case "/api/v1/robot/msg/groupmsgsend":
			if got := r.Header.Get("Authorization"); got != "Bearer-"+fixtureToken {
				t.Errorf("Authorization = %q", got)
			}
			assertJSONEqual(t, readRequestBody(t, r), `{
				"message": {
					"header": {
						"toid": 1671623,
						"totype": "GROUP",
						"msgtype": "TEXT",
						"clientmsgid": 1569398477302,
						"role": "robot"
					},
					"body": [{"type":"TEXT","content":"Hello, Group!"}]
				}
			}`)
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"errmsg":"ok","data":{"messageid":1001,"msgseqid":1002,"ctime":1569398477302},"unknown":"kept"}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newFixtureClient(t, server, func() time.Time { return fixed })
	result, err := client.Bot.IM.Message.SendToGroup(context.Background(), 1671623, "Hello, Group!")
	if err != nil {
		t.Fatalf("SendToGroup() error = %v", err)
	}
	if result.MessageID != 1001 || result.MessageSequenceID != 1002 || result.CreatedAtMillis != 1569398477302 {
		t.Fatalf("result = %#v", result)
	}
	raw := result.RawJSON()
	if !strings.Contains(string(raw), `"unknown":"kept"`) {
		t.Fatalf("raw JSON = %s", raw)
	}
	raw[0] = 'x'
	if result.RawJSON()[0] != '{' {
		t.Fatal("RawJSON() shares caller-mutated storage")
	}
}

func TestTokenCacheReuseAndEarlyRefreshBoundary(t *testing.T) {
	clock := &manualClock{now: time.Unix(1_700_000_000, 0)}
	var tokenRequests atomic.Int32
	var messageRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/app_access_token":
			tokenRequests.Add(1)
			respondWithToken(w, 120)
		case "/api/v1/app/message/send":
			messageRequests.Add(1)
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"errmsg":"ok","msgkey":1,"invaliduser":""}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newFixtureClient(t, server, clock.Now)
	for i, advance := range []time.Duration{0, 59 * time.Second, time.Second} {
		clock.Advance(advance)
		if _, err := client.Bot.IM.Message.SendToUser(context.Background(), "zhangsan", "hello"); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if got := tokenRequests.Load(); got != 2 {
		t.Fatalf("token requests = %d, want 2 (refresh at equality boundary)", got)
	}
	if got := messageRequests.Load(); got != 3 {
		t.Fatalf("message requests = %d, want 3", got)
	}
}

func TestConcurrentFirstUseAcquiresOneToken(t *testing.T) {
	var tokenRequests atomic.Int32
	var messageRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/app_access_token":
			tokenRequests.Add(1)
			time.Sleep(20 * time.Millisecond)
			respondWithToken(w, 7200)
		case "/api/v1/app/message/send":
			messageRequests.Add(1)
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"errmsg":"ok","msgkey":1,"invaliduser":""}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newFixtureClient(t, server, time.Now)
	const callers = 24
	start := make(chan struct{})
	errCh := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := client.Bot.IM.Message.SendToUser(context.Background(), "zhangsan", "hello")
			errCh <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Errorf("concurrent send error = %v", err)
		}
	}
	if got := tokenRequests.Load(); got != 1 {
		t.Fatalf("token requests = %d, want 1", got)
	}
	if got := messageRequests.Load(); got != callers {
		t.Fatalf("message requests = %d, want %d", got, callers)
	}
}

func TestGroupClientMessageIDsAreStrictlyIncreasingWithFixedClockAndConcurrency(t *testing.T) {
	fixedMillis := int64(1_700_000_000_123)
	fixed := time.UnixMilli(fixedMillis)
	var idsMu sync.Mutex
	var ids []int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/app_access_token":
			respondWithToken(w, 7200)
		case "/api/v1/robot/msg/groupmsgsend":
			var request struct {
				Message struct {
					Header struct {
						ClientMessageID int64 `json:"clientmsgid"`
					} `json:"header"`
				} `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode group request: %v", err)
			}
			idsMu.Lock()
			ids = append(ids, request.Message.Header.ClientMessageID)
			idsMu.Unlock()
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"errmsg":"ok","data":{"messageid":1,"msgseqid":2,"ctime":3}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newFixtureClient(t, server, func() time.Time { return fixed })
	for range 2 {
		if _, err := client.Bot.IM.Message.SendToGroup(context.Background(), 1671623, "hello"); err != nil {
			t.Fatalf("sequential group send: %v", err)
		}
	}

	const concurrentCalls = 24
	start := make(chan struct{})
	errCh := make(chan error, concurrentCalls)
	var wg sync.WaitGroup
	for range concurrentCalls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := client.Bot.IM.Message.SendToGroup(context.Background(), 1671623, "hello")
			errCh <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Errorf("concurrent group send: %v", err)
		}
	}

	idsMu.Lock()
	gotIDs := append([]int64(nil), ids...)
	idsMu.Unlock()
	if len(gotIDs) != 2+concurrentCalls {
		t.Fatalf("captured %d clientmsgid values, want %d", len(gotIDs), 2+concurrentCalls)
	}
	if gotIDs[0] != fixedMillis || gotIDs[1] != fixedMillis+1 {
		t.Fatalf("sequential clientmsgid values = %v, want [%d %d]", gotIDs[:2], fixedMillis, fixedMillis+1)
	}
	sort.Slice(gotIDs, func(i, j int) bool { return gotIDs[i] < gotIDs[j] })
	for i, id := range gotIDs {
		want := fixedMillis + int64(i)
		if id != want || id <= 0 {
			t.Fatalf("sorted clientmsgid[%d] = %d, want positive %d; all=%v", i, id, want, gotIDs)
		}
	}
}

func TestTypedErrorMappingAndNoRetry(t *testing.T) {
	t.Run("auth envelope", func(t *testing.T) {
		var messageRequests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/auth/app_access_token" {
				writeJSON(w, http.StatusOK, `{"code":"auth.invalidAppSecret","data":null}`)
				return
			}
			messageRequests.Add(1)
			http.NotFound(w, r)
		}))
		defer server.Close()
		client := newFixtureClient(t, server, time.Now)
		_, err := client.Bot.IM.Message.SendToUser(context.Background(), "zhangsan", "hello")
		var target *AuthError
		if !errors.As(err, &target) || target.Code != "auth.invalidAppSecret" {
			t.Fatalf("error = %T %v", err, err)
		}
		if strings.Contains(target.Error(), fixtureSecret) || strings.Contains(target.Error(), fixtureSecretMD5) {
			t.Fatalf("auth error leaks credential: %v", target)
		}
		if messageRequests.Load() != 0 {
			t.Fatalf("message requests = %d, want 0", messageRequests.Load())
		}
	})

	t.Run("nested business envelope", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/auth/app_access_token" {
				respondWithToken(w, 7200)
				return
			}
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":40044,"errmsg":"robot not in group"}}`)
		}))
		defer server.Close()
		client := newFixtureClient(t, server, time.Now)
		_, err := client.Bot.IM.Message.SendToGroup(context.Background(), 1671623, "hello")
		var target *BusinessError
		if !errors.As(err, &target) || target.Code != "40044" || target.Message != "robot not in group" {
			t.Fatalf("error = %T %#v", err, err)
		}
		raw := target.RawJSON()
		raw[0] = 'x'
		if target.RawJSON()[0] != '{' {
			t.Fatal("BusinessError.RawJSON shares storage")
		}
	})

	t.Run("top-level business envelope", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/auth/app_access_token" {
				respondWithToken(w, 7200)
				return
			}
			writeJSON(w, http.StatusOK, `{"code":"message.denied","message":"denied","data":null}`)
		}))
		defer server.Close()
		client := newFixtureClient(t, server, time.Now)
		_, err := client.Bot.IM.Message.SendToUser(context.Background(), "zhangsan", "hello")
		var target *BusinessError
		if !errors.As(err, &target) || target.Code != "message.denied" || target.Message != "denied" {
			t.Fatalf("error = %T %#v", err, err)
		}
	})

	t.Run("HTTP status makes one attempt", func(t *testing.T) {
		var attempts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/auth/app_access_token" {
				respondWithToken(w, 7200)
				return
			}
			attempts.Add(1)
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		}))
		defer server.Close()
		client := newFixtureClient(t, server, time.Now)
		_, err := client.Bot.IM.Message.SendToUser(context.Background(), "zhangsan", "hello")
		var target *HTTPError
		if !errors.As(err, &target) || target.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("error = %T %#v", err, err)
		}
		body := target.Body()
		body[0] = 'x'
		if got := string(target.Body()); !strings.HasPrefix(got, "temporarily") {
			t.Fatalf("HTTPError.Body() not defensive: %q", got)
		}
		if attempts.Load() != 1 {
			t.Fatalf("attempts = %d, want 1", attempts.Load())
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/auth/app_access_token" {
				respondWithToken(w, 7200)
				return
			}
			writeJSON(w, http.StatusOK, `{not-json`)
		}))
		defer server.Close()
		client := newFixtureClient(t, server, time.Now)
		_, err := client.Bot.IM.Message.SendToUser(context.Background(), "zhangsan", "hello")
		var target *MalformedResponseError
		if !errors.As(err, &target) {
			t.Fatalf("error = %T %v", err, err)
		}
		if got := string(target.RawResponse()); got != `{not-json` {
			t.Fatalf("raw response = %q", got)
		}
	})
}

func TestTypedErrorStringsExcludeCredentialsAuthorizationAndMessageContent(t *testing.T) {
	const (
		userMessage  = "private-user-message-7d92"
		groupMessage = "private-group-message-a614"
	)
	authorizationValue := "Bearer-" + fixtureToken
	sensitiveValues := []string{
		fixtureSecret,
		fixtureSecretMD5,
		fixtureToken,
		authorizationValue,
		"Authorization: " + authorizationValue,
		userMessage,
		groupMessage,
	}
	sensitiveResponseText := strings.Join(sensitiveValues, "|")

	t.Run("configuration", func(t *testing.T) {
		roundTripper := &countingRoundTripper{}
		client, err := NewClient(Config{
			AppKey:     fixtureAppKey,
			AppSecret:  fixtureSecret,
			HTTPClient: &http.Client{Transport: roundTripper},
		})
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		_, err = client.Bot.IM.Message.SendToGroup(context.Background(), 0, groupMessage)
		var target *ConfigError
		if !errors.As(err, &target) {
			t.Fatalf("error = %T %v, want *ConfigError", err, err)
		}
		assertErrorStringExcludes(t, err, sensitiveValues...)
		if got := roundTripper.requests.Load(); got != 0 {
			t.Fatalf("configuration failure made %d requests, want 0", got)
		}
	})

	t.Run("authentication", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, fmt.Sprintf(
				`{"code":"auth.invalidAppSecret","message":%q,"data":null}`,
				sensitiveResponseText,
			))
		}))
		defer server.Close()

		client := newFixtureClient(t, server, time.Now)
		_, err := client.Bot.IM.Message.SendToUser(context.Background(), "zhangsan", userMessage)
		var target *AuthError
		if !errors.As(err, &target) {
			t.Fatalf("error = %T %v, want *AuthError", err, err)
		}
		assertErrorStringExcludes(t, err, sensitiveValues...)
	})

	t.Run("HTTP status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/auth/app_access_token" {
				respondWithToken(w, 7200)
				return
			}
			http.Error(w, sensitiveResponseText, http.StatusServiceUnavailable)
		}))
		defer server.Close()

		client := newFixtureClient(t, server, time.Now)
		_, err := client.Bot.IM.Message.SendToUser(context.Background(), "zhangsan", userMessage)
		var target *HTTPError
		if !errors.As(err, &target) {
			t.Fatalf("error = %T %v, want *HTTPError", err, err)
		}
		assertErrorStringExcludes(t, err, sensitiveValues...)
	})

	t.Run("business", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/auth/app_access_token" {
				respondWithToken(w, 7200)
				return
			}
			writeJSON(w, http.StatusOK, fmt.Sprintf(
				`{"code":"ok","data":{"errcode":40044,"errmsg":%q}}`,
				sensitiveResponseText,
			))
		}))
		defer server.Close()

		client := newFixtureClient(t, server, time.Now)
		_, err := client.Bot.IM.Message.SendToGroup(context.Background(), 1671623, groupMessage)
		var target *BusinessError
		if !errors.As(err, &target) {
			t.Fatalf("error = %T %v, want *BusinessError", err, err)
		}
		assertErrorStringExcludes(t, err, sensitiveValues...)
	})

	t.Run("malformed response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/auth/app_access_token" {
				respondWithToken(w, 7200)
				return
			}
			writeJSON(w, http.StatusOK, `{not-json|`+sensitiveResponseText)
		}))
		defer server.Close()

		client := newFixtureClient(t, server, time.Now)
		_, err := client.Bot.IM.Message.SendToUser(context.Background(), "zhangsan", userMessage)
		var target *MalformedResponseError
		if !errors.As(err, &target) {
			t.Fatalf("error = %T %v, want *MalformedResponseError", err, err)
		}
		assertErrorStringExcludes(t, err, sensitiveValues...)
	})
}

func TestInjectedHTTPClientStillUsesRequestDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/app_access_token" {
			respondWithToken(w, 7200)
			return
		}
		// The finite delay is longer than the SDK deadline but guarantees the
		// handler exits even if a particular net/http implementation does not
		// propagate client cancellation until after a response is available.
		time.Sleep(200 * time.Millisecond)
		writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"errmsg":"ok","msgkey":1,"invaliduser":""}}`)
	}))
	defer server.Close()

	client := newFixtureClient(t, server, time.Now, func(cfg *Config) {
		cfg.RequestTimeout = 50 * time.Millisecond
		cfg.ConnectTimeout = 25 * time.Millisecond
	})
	started := time.Now()
	_, err := client.Bot.IM.Message.SendToUser(context.Background(), "zhangsan", "hello")
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("request deadline took %v", elapsed)
	}
	var target *HTTPError
	if !errors.As(err, &target) || target.StatusCode != 0 {
		t.Fatalf("error = %T %#v, want network *HTTPError", err, err)
	}
}

func TestBotMessageSummaryWiringThroughClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/app_access_token":
			respondWithToken(w, 7200)
			return
		case "/api/v1/im/messageSummaryTasks":
			if r.Method == http.MethodGet {
				if got := r.URL.Query().Get("taskId"); got != "3f2a6c1e-8b47-4d9a-9f0e-2c5d7a1b4e63" {
					t.Errorf("taskId query = %q", got)
				}
				writeJSON(w, http.StatusOK, `{"code":"ok","data":{"bizCode":200,"bizMsg":"success","bizData":{"taskId":"3f2a6c1e-8b47-4d9a-9f0e-2c5d7a1b4e63","status":"SUCCESS","result":{"summary":"summary text"},"error":null,"expireAt":1767312000000}}}`)
				return
			}
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"bizCode":200,"bizMsg":"accepted","bizData":{"taskId":"3f2a6c1e-8b47-4d9a-9f0e-2c5d7a1b4e63","status":"PENDING","expireAt":1767312000000}}}`)
			return
		}
		t.Errorf("unexpected path %s", r.URL.Path)
	}))
	defer server.Close()

	client := newFixtureClient(t, server, time.Now)

	created, err := client.Bot.MessageSummary.CreateTask(context.Background(), im.CreateMessageSummaryTaskRequest{
		CorpID:     1,
		AgentID:    6752,
		AuthUser:   "zhangsan",
		UgateToken: "ugate-secret-token",
		Intent:     "重点总结风险和待办",
	})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if created.TaskID != "3f2a6c1e-8b47-4d9a-9f0e-2c5d7a1b4e63" || created.Status != "PENDING" {
		t.Fatalf("created = %#v", created)
	}

	fetched, err := client.Bot.MessageSummary.GetTask(context.Background(), im.GetMessageSummaryTaskRequest{
		CorpID:     1,
		AgentID:    6752,
		AuthUser:   "zhangsan",
		UgateToken: "ugate-secret-token",
		TaskID:     created.TaskID,
	})
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if fetched.Status != "SUCCESS" || fetched.Summary != "summary text" {
		t.Fatalf("fetched = %#v", fetched)
	}
}
