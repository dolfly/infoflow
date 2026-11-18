package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	infoflow "github.com/dolfly/infoflow"
	realtime "github.com/dolfly/infoflow/bot/events"
	botwebhook "github.com/dolfly/infoflow/bot/webhook"
)

type callbackCaptureObserver struct {
	records []realtime.Diagnostic
}

func (observer *callbackCaptureObserver) Observe(record realtime.Diagnostic) {
	observer.records = append(observer.records, record)
}

func TestWebhookClientSurfaceSharesDispatcherAndRegistersRequiredHeaders(t *testing.T) {
	var callbackRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/app_access_token":
			respondWithToken(w, 7200)
		case "/api/v1/imRobot/updateReCallUrl":
			callbackRequests++
			if got := r.Header.Get("Authorization"); got != "Bearer-"+fixtureToken {
				t.Errorf("Authorization = %q", got)
			}
			if got := r.Header.Get("LOGID"); !regexp.MustCompile(`^[0-9]+$`).MatchString(got) {
				t.Errorf("LOGID = %q, want non-empty numeric", got)
			}
			if got := r.Header.Get("X-OP-ACCESS-CHANNEL"); got != "go_sdk" {
				t.Errorf("X-OP-ACCESS-CHANNEL = %q, want go_sdk", got)
			}
			assertJSONEqual(t, readRequestBody(t, r), `{
				"recallUrl":"https://callback.example.test/infoflow",
				"token":"callback-token",
				"encodingAesKey":"encoding-key",
				"receiveMsgState":1
			}`)
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"code":200,"msg":"success","logId":9007199254740993123}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newFixtureClient(t, server, time.Now)
	receiver, err := client.Bot.Webhook.NewReceiver(botwebhook.Options{
		CheckToken:     "callback-token",
		EncodingAESKey: "encoding-key",
	})
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}
	if receiver.Dispatcher() != client.Bot.Events {
		t.Fatal("Webhook receiver did not inherit client.Bot.Events")
	}

	result, err := client.Bot.Webhook.Register(context.Background(), botwebhook.Registration{
		CallbackURL:    "https://callback.example.test/infoflow",
		Token:          "callback-token",
		EncodingAESKey: "encoding-key",
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if result.Code != 200 || result.Message != "success" || result.LogID != "9007199254740993123" {
		t.Fatalf("result = %#v", result)
	}
	raw := result.RawJSON()
	raw[0] = 'x'
	if result.RawJSON()[0] != '{' {
		t.Fatal("botwebhook.RegistrationResult.RawJSON() shares caller mutation")
	}
	if callbackRequests != 1 {
		t.Fatalf("callback requests = %d, want 1", callbackRequests)
	}
}

func TestWebhookRegisterRejectsNonHTTPSBeforeTokenIO(t *testing.T) {
	tests := []struct {
		name         string
		registration botwebhook.Registration
	}{
		{"non HTTPS callback", botwebhook.Registration{CallbackURL: "http://callback.example.test/infoflow", Token: "token", EncodingAESKey: "key"}},
		{"missing callback", botwebhook.Registration{Token: "token", EncodingAESKey: "key"}},
		{"missing token", botwebhook.Registration{CallbackURL: "https://callback.example.test/infoflow", EncodingAESKey: "key"}},
		{"missing encoding key", botwebhook.Registration{CallbackURL: "https://callback.example.test/infoflow", Token: "token"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roundTripper := &countingRoundTripper{}
			client, err := infoflow.NewClient(infoflow.Config{
				AppKey: fixtureAppKey, AppSecret: fixtureSecret,
				HTTPClient: &http.Client{Transport: roundTripper},
			})
			if err != nil {
				t.Fatalf("infoflow.NewClient() error = %v", err)
			}
			_, err = client.Bot.Webhook.Register(context.Background(), tt.registration)
			var target *infoflow.ConfigError
			if !errors.As(err, &target) {
				t.Fatalf("error = %T %v, want *infoflow.ConfigError", err, err)
			}
			if got := roundTripper.Requests.Load(); got != 0 {
				t.Fatalf("invalid registration made %d requests, want 0", got)
			}
		})
	}
}

func TestWebhookRegisterSensitivePOSTDoesNotFollowRedirect(t *testing.T) {
	const callbackSecret = "sensitive-webhook-callback-token"
	targetRequests := 0
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		targetRequests++
		if strings.Contains(string(readRequestBody(t, request)), callbackSecret) {
			t.Error("redirect target received callback secret")
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/auth/app_access_token" {
			respondWithToken(writer, 7200)
			return
		}
		writer.Header().Set("Location", target.URL+"/capture")
		writer.WriteHeader(http.StatusPermanentRedirect)
	}))
	defer source.Close()

	callerClient := source.Client()
	redirectPolicyCalls := 0
	callerClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		redirectPolicyCalls++
		return nil
	}
	client, err := infoflow.NewClient(infoflow.Config{
		AppKey: fixtureAppKey, AppSecret: fixtureSecret,
		BaseURL: source.URL + "/api/v1", HTTPClient: callerClient,
	})
	if err != nil {
		t.Fatalf("infoflow.NewClient() error = %v", err)
	}
	_, err = client.Bot.Webhook.Register(context.Background(), botwebhook.Registration{
		CallbackURL: "https://callback.example.test/infoflow",
		Token:       callbackSecret, EncodingAESKey: "sensitive-webhook-aes-key",
	})
	var targetError *infoflow.HTTPError
	if !errors.As(err, &targetError) || targetError.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("Register() error = %T %#v, want HTTP 308", err, err)
	}
	if targetRequests != 0 || redirectPolicyCalls != 0 || callerClient.CheckRedirect == nil {
		t.Fatalf("redirect target=%d caller policy calls=%d", targetRequests, redirectPolicyCalls)
	}
}

func TestWebhookRegisterServiceErrorAndDiagnosticsAreRedacted(t *testing.T) {
	const (
		callbackToken = "private-callback-token"
		encodingKey   = "private-encoding-key"
	)
	var generatedLogID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/app_access_token" {
			respondWithToken(w, 7200)
			return
		}
		generatedLogID = r.Header.Get("LOGID")
		writeJSON(w, http.StatusOK, `{"code":"ok","data":{"code":40044,"msg":"service rejected callback"}}`)
	}))
	defer server.Close()

	observer := &callbackCaptureObserver{}
	client := newFixtureClient(t, server, time.Now, func(cfg *infoflow.Config) {
		cfg.Realtime = infoflow.RealtimeConfig{Observer: observer}
	})
	_, err := client.Bot.Webhook.Register(context.Background(), botwebhook.Registration{
		CallbackURL: "https://callback.example.test/infoflow", Token: callbackToken, EncodingAESKey: encodingKey,
	})
	var target *infoflow.BusinessError
	if !errors.As(err, &target) || target.Code != "40044" {
		t.Fatalf("error = %T %#v", err, err)
	}
	serialized := fmt.Sprint(err, observer.records)
	for _, sensitive := range []string{callbackToken, encodingKey, fixtureToken, "Bearer-" + fixtureToken, generatedLogID} {
		if sensitive != "" && strings.Contains(serialized, sensitive) {
			t.Fatalf("diagnostic output leaked sensitive value %q: %s", sensitive, serialized)
		}
	}
	if len(observer.records) < 2 || observer.records[len(observer.records)-1].Event != "webhook.registration.failure" {
		t.Fatalf("diagnostics = %#v", observer.records)
	}
	if observer.records[0].RequestID == "" || observer.records[0].RequestID != observer.records[len(observer.records)-1].RequestID {
		t.Fatalf("registration records are not correlated: %#v", observer.records)
	}
}
