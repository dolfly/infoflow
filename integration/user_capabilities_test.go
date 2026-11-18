package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	infoflow "github.com/dolfly/infoflow"
	"github.com/dolfly/infoflow/user"
	usercalendar "github.com/dolfly/infoflow/user/calendar"
	userim "github.com/dolfly/infoflow/user/im"
	usermeeting "github.com/dolfly/infoflow/user/meeting"
	"github.com/dolfly/infoflow/userauthorization"
)

func TestUserRobot2CProfileUsesAppAuthAndOneIdentityHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robot2c/auth/list":
			if got := r.Header.Get("Authorization"); got != "Bearer-"+fixtureToken {
				t.Fatalf("Authorization = %q", got)
			}
			if r.Header.Get("Robot2c-Token") != "robot-token" || r.Header.Get("Ugate-Token") != "" {
				t.Fatalf("identity headers = %#v", r.Header)
			}
			if r.Header.Get("X-LOGID") != "" || r.Header.Get("X-OP-CORP-ID") != "" {
				t.Fatalf("historical headers leaked: %#v", r.Header)
			}
			writeJSON(w, http.StatusOK, `{"code":0,"data":[]}`)
		default:
			respondWithToken(w, 3600)
		}
	}))
	defer server.Close()
	client := newFixtureClient(t, server, nil, func(c *infoflow.Config) {
		c.User = user.Config{Robot2CToken: "robot-token", Robot2CBaseURL: server.URL}
	})
	if _, err := client.UserAuthorization.List(context.Background(), userauthorization.ListRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestUserCalendarProfileDoesNotLeakAppAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/calendar/freebusy" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Robot2c-Token") != "" || r.Header.Get("Ugate-Token") != "ugate-token" {
			t.Fatalf("headers = %#v", r.Header)
		}
		writeJSON(w, http.StatusOK, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	client := newFixtureClient(t, server, nil, func(c *infoflow.Config) {
		c.User = user.Config{UGateToken: "ugate-token", CalendarBaseURL: server.URL}
	})
	if _, err := client.User.Calendar.GetFreeBusy(context.Background(), usercalendar.FreeBusyRequest{StartTime: 1, EndTime: 2, Emails: []string{"safe@example.test"}}); err != nil {
		t.Fatal(err)
	}
}

func TestUserUGateJSONInputUsesOnlyTokenField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Ugate-Token"); got != "cache-token" {
			t.Fatalf("Ugate-Token = %q", got)
		}
		if strings.Contains(r.Header.Get("Ugate-Token"), "{") {
			t.Fatal("JSON blob leaked into Ugate-Token")
		}
		writeJSON(w, http.StatusOK, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	client := newFixtureClient(t, server, nil, func(c *infoflow.Config) {
		c.User = user.Config{UGateToken: `{"token":"cache-token","expires_at":"ignored"}`, CalendarBaseURL: server.URL}
	})
	if _, err := client.User.Calendar.GetFreeBusy(context.Background(), usercalendar.FreeBusyRequest{StartTime: 1, EndTime: 2, Emails: []string{"safe@example.test"}}); err != nil {
		t.Fatal(err)
	}
}

func TestUserUGateJSONWithoutTokenDoesNotRequest(t *testing.T) {
	roundTripper := &countingRoundTripper{}
	client, err := infoflow.NewClient(infoflow.Config{AppKey: fixtureAppKey, AppSecret: fixtureSecret, HTTPClient: &http.Client{Transport: roundTripper}, User: user.Config{UGateToken: `{}`}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.User.Calendar.GetFreeBusy(context.Background(), usercalendar.FreeBusyRequest{StartTime: 1, EndTime: 2, Emails: []string{"safe@example.test"}}); err == nil {
		t.Fatal("invalid JSON UGate token accepted")
	}
	if roundTripper.Requests.Load() != 0 {
		t.Fatalf("requests = %d", roundTripper.Requests.Load())
	}
}

func TestUserMeetingValidationDoesNotCallNetwork(t *testing.T) {
	client, err := infoflow.NewClient(infoflow.Config{AppKey: fixtureAppKey, AppSecret: fixtureSecret, HTTPClient: &http.Client{Transport: &countingRoundTripper{}}, User: user.Config{UGateToken: "token"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.User.Meeting.CallMeeting(context.Background(), usermeeting.CallRequest{}); err == nil {
		t.Fatal("CallMeeting accepted invalid input")
	}
}

func TestUserGrantAndRevokeRequireUserWithoutNetwork(t *testing.T) {
	roundTripper := &countingRoundTripper{}
	client, err := infoflow.NewClient(infoflow.Config{AppKey: fixtureAppKey, AppSecret: fixtureSecret, HTTPClient: &http.Client{Transport: roundTripper}, User: user.Config{Robot2CToken: "robot"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.UserAuthorization.Grant(context.Background(), userauthorization.GrantRequest{InstanceID: "instance", Permissions: []string{"p"}, ExpiredDays: 1}); err == nil {
		t.Fatal("Grant accepted missing user")
	}
	if _, err := client.UserAuthorization.Revoke(context.Background(), userauthorization.RevokeRequest{InstanceID: "instance"}); err == nil {
		t.Fatal("Revoke accepted missing user")
	}
	if roundTripper.Requests.Load() != 0 {
		t.Fatalf("requests = %d", roundTripper.Requests.Load())
	}
}

func TestUserGrantSerializesUser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robot2c/auth/grant":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["user"] != "safe-user" || body["instanceId"] != "instance" {
				t.Fatalf("body=%#v", body)
			}
			writeJSON(w, http.StatusOK, `{"code":0,"data":{"status":"open"}}`)
		default:
			respondWithToken(w, 3600)
		}
	}))
	defer server.Close()
	client := newFixtureClient(t, server, nil, func(c *infoflow.Config) {
		c.User = user.Config{Robot2CToken: "robot-token", Robot2CBaseURL: server.URL}
	})
	if _, err := client.UserAuthorization.Grant(context.Background(), userauthorization.GrantRequest{User: "safe-user", InstanceID: "instance", Permissions: []string{"scope"}, ExpiredDays: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestUserLatestContactsParsesNestedResultSetAndKeepsPaging(t *testing.T) {
	var timestamps []int64
	page := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/app_access_token" {
			respondWithToken(w, 3600)
			return
		}
		if r.URL.Path != "/im/get_latest_contacts" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var body struct {
			Timestamp int64 `json:"timestamp"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		timestamps = append(timestamps, body.Timestamp)
		page++
		if page == 1 {
			writeJSON(w, http.StatusOK, `{"code":0,"msg":"success","data":{"code":210,"timestamp":"100","result_set":{"top_list":{"result_set.top_list":[{"id":"top-1","type":"2","name":"Top"}]},"change_set":{"result_set.change_set":[{"id":"change-1","type":"2","name":"Change"}]},"delete_set":[],"unknown_set":""}}}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"code":0,"msg":"success","data":{"code":0,"timestamp":"200","top_list":[{"id":"top-1","type":"2","name":"Top"}],"change_set":[{"id":"change-2","type":"2","name":"Change 2"}],"delete_set":[],"unknown_set":[]}}`)
	}))
	defer server.Close()
	client := newFixtureClient(t, server, nil, func(c *infoflow.Config) {
		c.User = user.Config{Robot2CToken: "robot-token", Robot2CBaseURL: server.URL}
	})
	result, err := client.User.IM.Message.GetLatestContacts(context.Background(), userim.LatestContactsRequest{MaxPages: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(timestamps) != 2 || timestamps[0] != 0 || timestamps[1] != 100 {
		t.Fatalf("paging timestamps=%v", timestamps)
	}
	if len(result.Contacts) != 3 {
		t.Fatalf("contacts=%#v", result.Contacts)
	}
	if result.Contacts[0].ID != "top-1" || result.Contacts[1].ID != "change-1" || result.Contacts[2].ID != "change-2" {
		t.Fatalf("contacts=%#v", result.Contacts)
	}
	for _, contact := range result.Contacts {
		if len(contact.Raw) != 0 {
			t.Fatalf("default contact raw leaked: %#v", contact.Raw)
		}
	}
}

func TestUserLatestContactsRawRequiresExplicitOptIn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/app_access_token" {
			respondWithToken(w, 3600)
			return
		}
		writeJSON(w, http.StatusOK, `{"code":0,"msg":"success","data":{"code":0,"timestamp":"0","result_set":{"top_list":[{"id":"contact-1","type":"2","name":"Contact"}]}}}`)
	}))
	defer server.Close()
	client := newFixtureClient(t, server, nil, func(c *infoflow.Config) {
		c.User = user.Config{Robot2CToken: "robot-token", Robot2CBaseURL: server.URL}
	})
	withoutRaw, err := client.User.IM.Message.GetLatestContacts(context.Background(), userim.LatestContactsRequest{MaxPages: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutRaw.RawPages) != 0 || len(withoutRaw.Contacts) != 1 || len(withoutRaw.Contacts[0].Raw) != 0 {
		t.Fatalf("default raw output=%#v", withoutRaw)
	}
	withRaw, err := client.User.IM.Message.GetLatestContacts(context.Background(), userim.LatestContactsRequest{MaxPages: 1, IncludeRaw: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(withRaw.RawPages) != 1 || len(withRaw.Contacts) != 1 || len(withRaw.Contacts[0].Raw) == 0 {
		t.Fatalf("opt-in raw output=%#v", withRaw)
	}
}

func TestUserSummaryRejectsNestedBusinessErrorBeforeAsyncWait(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/app_access_token" {
			respondWithToken(w, 3600)
			return
		}
		if r.URL.Path != "/im/robot/summary" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		assertJSONEqual(t, readRequestBody(t, r), `{"chatId":"group-1","chatType":2,"startTime":1000,"endTime":2000}`)
		writeJSON(w, http.StatusOK, `{"code":"ok","data":{"code":40001,"message":"invalid param","requestId":"request-1"}}`)
	}))
	defer server.Close()
	client := newFixtureClient(t, server, nil, func(c *infoflow.Config) {
		c.User = user.Config{Robot2CToken: "robot-token", Robot2CBaseURL: server.URL}
	})
	_, err := client.User.IM.Message.CreateSummary(context.Background(), userim.CreateSummaryRequest{ChatID: "group-1", ChatType: "2", StartTime: 1000, EndTime: 2000})
	var business *infoflow.BusinessError
	if !errors.As(err, &business) || business.Code != "40001" || business.Message != "invalid param" || business.RequestID != "request-1" {
		t.Fatalf("error=%T %#v", err, err)
	}
}

func TestUserImportSummaryRequestShapeUsesStringTimesAndCurrentFilters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/app_access_token" {
			respondWithToken(w, 3600)
			return
		}
		if r.URL.Path != "/im/importMsgSummary" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		assertJSONEqual(t, readRequestBody(t, r), `{"startQueryTime":"1000","endQueryTime":"2000","topicType":"1","intent":"intent text","unreadType":"1"}`)
		writeJSON(w, http.StatusOK, `{"code":"ok","data":{"taskId":"task-1"}}`)
	}))
	defer server.Close()
	client := newFixtureClient(t, server, nil, func(c *infoflow.Config) {
		c.User = user.Config{Robot2CToken: "robot-token", Robot2CBaseURL: server.URL}
	})
	result, err := client.User.IM.Message.ImportSummary(context.Background(), userim.ImportSummaryRequest{StartQueryTime: 1000, EndQueryTime: 2000, TopicType: "1", Intent: " intent text ", UnreadType: "1"})
	if err != nil || result.TaskID != "task-1" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestUserSummaryRejectsUnknownChatTypeWithoutNetwork(t *testing.T) {
	transport := &countingRoundTripper{}
	client, err := infoflow.NewClient(infoflow.Config{AppKey: fixtureAppKey, AppSecret: fixtureSecret, HTTPClient: &http.Client{Transport: transport}, User: user.Config{Robot2CToken: "robot"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.User.IM.Message.CreateSummary(context.Background(), userim.CreateSummaryRequest{ChatID: "group-1", ChatType: "group", StartTime: 1000, EndTime: 2000})
	if err == nil || transport.Requests.Load() != 0 {
		t.Fatalf("err=%v requests=%d", err, transport.Requests.Load())
	}
}
