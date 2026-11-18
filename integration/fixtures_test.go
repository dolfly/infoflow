package integration_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	infoflow "github.com/dolfly/infoflow"
	"github.com/dolfly/infoflow/internal/testkit"
)

const (
	fixtureAppKey    = testkit.AppKey
	fixtureSecret    = testkit.Secret
	fixtureSecretMD5 = testkit.SecretMD5
	fixtureToken     = testkit.Token
)

type countingRoundTripper = testkit.CountingRoundTripper

func newFixtureClient(t *testing.T, server *httptest.Server, _ func() time.Time, overrides ...func(*infoflow.Config)) *infoflow.Client {
	t.Helper()
	cfg := infoflow.Config{AppKey: fixtureAppKey, AppSecret: fixtureSecret, BaseURL: server.URL + "/api/v1", HTTPClient: server.Client()}
	for _, override := range overrides {
		override(&cfg)
	}
	client, err := infoflow.NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

var writeJSON = testkit.WriteJSON
var respondWithToken = testkit.RespondWithToken
var readRequestBody = testkit.ReadRequestBody
var assertJSONEqual = testkit.AssertJSONEqual

func assertConfigError(t *testing.T, err error) {
	t.Helper()
	var target *infoflow.ConfigError
	if !errors.As(err, &target) {
		t.Fatalf("error = %T %v, want *ConfigError", err, err)
	}
}

var assertErrorStringExcludes = testkit.ErrorStringExcludes

func extendedServer(_ *testing.T, handler func(http.ResponseWriter, *http.Request)) *httptest.Server {
	return testkit.ExtendedServer(handler)
}
