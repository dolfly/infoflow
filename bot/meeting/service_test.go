package meeting

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dolfly/infoflow/internal/auth"
	"github.com/dolfly/infoflow/internal/runtime"
	"github.com/dolfly/infoflow/internal/testkit"
	"github.com/dolfly/infoflow/internal/transport"
)

func newTestService(t *testing.T, handler http.HandlerFunc) Service {
	t.Helper()
	server := testkit.ExtendedServer(handler)
	t.Cleanup(server.Close)
	now := func() time.Time { return time.UnixMilli(1700000000000) }
	client := transport.New(server.URL+"/api/v1", time.Second, time.Second, server.Client(), "sdk")
	tokens := auth.NewTokenManager(client, testkit.AppKey, testkit.Secret, 0, now)
	return NewService(client, tokens, runtime.NewMessageIDGenerator(now))
}

func TestFetchAigcByEventIdRouteAndParsing(t *testing.T) {
	service := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workflow/api/calendar/qyOpenApi/v1/fetchAigcByEventId" || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var body FetchAigcByEventIDRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.EventID != "evt-1" {
			t.Fatalf("eventId = %q, want evt-1", body.EventID)
		}
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"aigcText":"event summary","items":[]}}`)
	})
	result, err := service.FetchAigcByEventId(context.Background(), "evt-1")
	if err != nil {
		t.Fatalf("FetchAigcByEventId() error = %v", err)
	}
	if result.AIGCText != "event summary" {
		t.Fatalf("AIGCText = %q, want event summary", result.AIGCText)
	}
	if len(result.RawJSON()) == 0 {
		t.Fatal("raw JSON not retained")
	}
}

func TestFetchAigcByEventIdValidatesBlankEventID(t *testing.T) {
	service := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("blank eventId reached network")
	})
	if _, err := service.FetchAigcByEventId(context.Background(), "  "); err == nil {
		t.Fatal("blank eventId did not produce an error")
	}
}
