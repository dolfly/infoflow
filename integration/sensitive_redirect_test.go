package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	infoflow "github.com/dolfly/infoflow"
	"github.com/dolfly/infoflow/bot/webhook"
)

func TestSensitiveCallbackRegistrationsRejectEveryRedirectStatus(t *testing.T) {
	statuses := []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	}
	for _, mode := range []string{"websocket", "webhook"} {
		for _, status := range statuses {
			t.Run(mode+"/"+fmt.Sprint(status), func(t *testing.T) {
				var targetRequests atomic.Int32
				target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					targetRequests.Add(1)
					writer.WriteHeader(http.StatusOK)
				}))
				defer target.Close()

				source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					if request.URL.Path == "/api/v1/auth/app_access_token" {
						respondWithToken(writer, 7200)
						return
					}
					writer.Header().Set("Location", target.URL+"/capture")
					writer.WriteHeader(status)
				}))
				defer source.Close()

				callerClient := source.Client()
				var callerRedirectPolicyCalls atomic.Int32
				callerClient.CheckRedirect = func(*http.Request, []*http.Request) error {
					callerRedirectPolicyCalls.Add(1)
					return nil
				}
				client, err := infoflow.NewClient(infoflow.Config{
					AppKey: fixtureAppKey, AppSecret: fixtureSecret,
					BaseURL: source.URL + "/api/v1", HTTPClient: callerClient,
				})
				if err != nil {
					t.Fatalf("infoflow.NewClient() error = %v", err)
				}
				if mode == "websocket" {
					err = client.Bot.WebSocket.Register(context.Background(), "abcdef0123456789abcdef0123456789")
				} else {
					_, err = client.Bot.Webhook.Register(context.Background(), webhook.Registration{
						CallbackURL: "https://callback.example.test/infoflow",
						Token:       "sensitive-callback-token", EncodingAESKey: "sensitive-callback-aes-key",
					})
				}
				var targetError *infoflow.HTTPError
				if !errors.As(err, &targetError) || targetError.StatusCode != status {
					t.Fatalf("registration error = %T %#v, want HTTP %d", err, err, status)
				}
				if targetRequests.Load() != 0 || callerRedirectPolicyCalls.Load() != 0 ||
					callerClient.CheckRedirect == nil {
					t.Fatalf("target=%d caller redirect policy=%d", targetRequests.Load(), callerRedirectPolicyCalls.Load())
				}
			})
		}
	}
}
