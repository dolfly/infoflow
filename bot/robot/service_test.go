package robot_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	infoflow "github.com/dolfly/infoflow"
)

func TestGetUserFollowStatusMatchesNodeRequestAndNormalizesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/app_access_token":
			if r.Method != http.MethodPost {
				t.Fatalf("token method = %s", r.Method)
			}
			writeRobotTestJSON(w, `{"code":"ok","data":{"app_access_token":"fixture","expire":3600}}`)
		case "/api/v1/app/agent/isFollow":
			if r.Method != http.MethodPost {
				t.Fatalf("follow method = %s", r.Method)
			}
			if !regexp.MustCompile(`^[0-9]+$`).MatchString(r.Header.Get("LOGID")) {
				t.Fatalf("LOGID = %q", r.Header.Get("LOGID"))
			}
			if r.Header.Get("X-OP-ACCESS-CHANNEL") != "go_sdk" {
				t.Fatalf("X-OP-ACCESS-CHANNEL = %q", r.Header.Get("X-OP-ACCESS-CHANNEL"))
			}
			if r.Header.Get("Authorization") != "Bearer-fixture" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			var body struct {
				UserIDs []string `json:"userids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body.UserIDs) != 1 || body.UserIDs[0] != "safe-user" {
				t.Fatalf("request body = %#v", body)
			}
			writeRobotTestJSON(w, `{"errcode":0,"errmsg":"ok","userlist":[{"userid":"safe-user","isFollow":true,"followTime":"2026-07-28 10:00:00"}]}`)
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := infoflow.NewClient(infoflow.Config{
		AppKey: "app", AppSecret: "secret", BaseURL: server.URL + "/api/v1", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := client.Bot.Robot.GetUserFollowStatus(context.Background(), []string{"safe-user"})
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].UserID != "safe-user" || !statuses[0].IsFollow {
		t.Fatalf("statuses = %#v", statuses)
	}
}

func writeRobotTestJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}
