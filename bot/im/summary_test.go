package im

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dolfly/infoflow/internal/auth"
	internalruntime "github.com/dolfly/infoflow/internal/runtime"
	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/testkit"
	"github.com/dolfly/infoflow/internal/transport"
)

const (
	testCorpID    = 1
	testAgentID   = 6752
	testAuthUser  = "zhangsan"
	testUgate     = "ugate-secret-token"
	testTaskID    = "3f2a6c1e-8b47-4d9a-9f0e-2c5d7a1b4e63"
	testExpireAt  = 1767312000000
	testStartTime = 1735689600
	testEndTime   = 1735776000
)

func newSummaryTestService(t *testing.T, handler http.HandlerFunc) *MessageSummaryService {
	t.Helper()
	server := testkit.ExtendedServer(handler)
	t.Cleanup(server.Close)
	// Fixed clock one hour after testEndTime so the documented window in
	// TestCreateMessageSummaryTaskExactHeadersAndBody is valid.
	now := func() time.Time { return time.Unix(testEndTime+3600, 0) }
	client := transport.New(server.URL+"/api/v1", time.Second, time.Second, server.Client(), "sdk")
	tokens := auth.NewTokenManager(client, testkit.AppKey, testkit.Secret, 0, now)
	service := NewMessageSummaryService(client, tokens, internalruntime.NewMessageIDGenerator(now), now)
	return &service
}

func assertSummaryConfigError(t *testing.T, err error) {
	t.Helper()
	var target *sdkerrors.ConfigError
	if !errors.As(err, &target) {
		t.Fatalf("error = %T %v, want *ConfigError", err, err)
	}
}

func assertSummaryBusinessError(t *testing.T, err error, wantCode string) {
	t.Helper()
	var target *sdkerrors.BusinessError
	if !errors.As(err, &target) {
		t.Fatalf("error = %T %v, want *BusinessError", err, err)
	}
	if target.Code != wantCode {
		t.Fatalf("BusinessError.Code = %q, want %q", target.Code, wantCode)
	}
}

func assertSummaryMalformedError(t *testing.T, err error) {
	t.Helper()
	var target *sdkerrors.MalformedResponseError
	if !errors.As(err, &target) {
		t.Fatalf("error = %T %v, want *MalformedResponseError", err, err)
	}
}

func TestCreateMessageSummaryTaskExactHeadersAndBody(t *testing.T) {
	var gotPath, gotMethod string
	var gotHeaders http.Header
	var requestBody []byte
	service := newSummaryTestService(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotHeaders = r.Header.Clone()
		requestBody = testkit.ReadRequestBody(t, r)
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"bizCode":200,"bizMsg":"accepted","bizData":{"taskId":"`+testTaskID+`","status":"PENDING","expireAt":`+strconv.FormatInt(testExpireAt, 10)+`}}}`)
	})

	result, err := service.CreateTask(context.Background(), CreateMessageSummaryTaskRequest{
		CorpID:            testCorpID,
		AgentID:           testAgentID,
		AuthUser:          testAuthUser,
		UgateToken:        testUgate,
		Intent:            "重点总结风险和待办",
		StartTime:         testStartTime,
		EndTime:           testEndTime,
		Keyword:           "项目风险",
		GroupIDs:          []int64{12685491},
		DoubleChatUserIDs: []string{"lisi"},
		SpeakerUserID:     "wangwu",
	})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if gotPath != "/api/v1/im/messageSummaryTasks" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %s, want POST", gotMethod)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer-"+testkit.Token {
		t.Fatalf("Authorization = %q", got)
	}
	if got := gotHeaders.Get("Ugate-Token"); got != testUgate {
		t.Fatalf("Ugate-Token = %q", got)
	}
	// Legacy identity fields are only emitted when explicitly provided.
	if got := gotHeaders.Get("X-OP-CORP-ID"); got != strconv.FormatInt(testCorpID, 10) {
		t.Fatalf("X-OP-CORP-ID = %q", got)
	}
	if got := gotHeaders.Get("X-OP-AGENT-ID"); got != strconv.FormatInt(testAgentID, 10) {
		t.Fatalf("X-OP-AGENT-ID = %q", got)
	}
	if got := gotHeaders.Get("X-OP-AUTH-USER"); got != testAuthUser {
		t.Fatalf("X-OP-AUTH-USER = %q", got)
	}
	if _, err := strconv.ParseInt(gotHeaders.Get("LOGID"), 10, 64); err != nil {
		t.Fatalf("LOGID = %q is not a pure number: %v", gotHeaders.Get("LOGID"), err)
	}
	testkit.AssertJSONEqual(t, requestBody, `{"intent":"重点总结风险和待办","startTime":1735689600,"endTime":1735776000,"keyword":"项目风险","groupIds":[12685491],"doubleChatUserIds":["lisi"],"speakerUserId":"wangwu"}`)

	if result.TaskID != testTaskID || result.Status != "PENDING" || result.ExpireAt != testExpireAt || result.AcceptanceUnknown {
		t.Fatalf("result = %#v", result)
	}
	if string(result.RawJSON()) == "" {
		t.Fatal("RawJSON() is empty")
	}
}

func TestCreateMessageSummaryTaskOmitsLegacyHeadersByDefault(t *testing.T) {
	var gotHeaders http.Header
	var requestBody []byte
	service := newSummaryTestService(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		requestBody = testkit.ReadRequestBody(t, r)
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"bizCode":200,"bizMsg":"accepted","bizData":{"taskId":"`+testTaskID+`","status":"PENDING","expireAt":1767312000000}}}`)
	})

	_, err := service.CreateTask(context.Background(), CreateMessageSummaryTaskRequest{
		UgateToken: testUgate,
		Intent:     "重点总结风险和待办",
	})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	for _, key := range []string{"X-OP-CORP-ID", "X-OP-AGENT-ID", "X-OP-AUTH-USER"} {
		if got := gotHeaders.Get(key); got != "" {
			t.Fatalf("%s = %q, want absent by default", key, got)
		}
	}
	if got := gotHeaders.Get("Ugate-Token"); got != testUgate {
		t.Fatalf("Ugate-Token = %q", got)
	}
	testkit.AssertJSONEqual(t, requestBody, `{"intent":"重点总结风险和待办"}`)
}

func TestCreateMessageSummaryTaskMinimalBodyAndRequiredUgate(t *testing.T) {
	var requestBody []byte
	var gotUgate string
	service := newSummaryTestService(t, func(w http.ResponseWriter, r *http.Request) {
		requestBody = testkit.ReadRequestBody(t, r)
		gotUgate = r.Header.Get("Ugate-Token")
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"bizCode":200,"bizMsg":"accepted","bizData":{"taskId":"`+testTaskID+`","status":"PENDING","expireAt":1767312000000}}}`)
	})

	_, err := service.CreateTask(context.Background(), CreateMessageSummaryTaskRequest{
		UgateToken: testUgate,
		Intent:     "重点总结风险和待办",
	})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	testkit.AssertJSONEqual(t, requestBody, `{"intent":"重点总结风险和待办"}`)
	if gotUgate != testUgate {
		t.Fatalf("Ugate-Token = %q, want %q", gotUgate, testUgate)
	}
}

func TestCreateMessageSummaryTaskTrimsIdentityHeaderValues(t *testing.T) {
	var gotHeaders http.Header
	service := newSummaryTestService(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"bizCode":200,"bizMsg":"accepted","bizData":{"taskId":"`+testTaskID+`","status":"PENDING","expireAt":1767312000000}}}`)
	})

	_, err := service.CreateTask(context.Background(), CreateMessageSummaryTaskRequest{
		UgateToken: "  " + testUgate + "\n",
		AuthUser:   "\t" + testAuthUser + " ",
		Intent:     "重点总结风险和待办",
	})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if got := gotHeaders.Get("Ugate-Token"); got != testUgate {
		t.Fatalf("Ugate-Token = %q, want trimmed %q", got, testUgate)
	}
	if got := gotHeaders.Get("X-OP-AUTH-USER"); got != testAuthUser {
		t.Fatalf("X-OP-AUTH-USER = %q, want trimmed %q", got, testAuthUser)
	}
}

func TestCreateMessageSummaryTaskAcceptanceUnknown(t *testing.T) {
	service := newSummaryTestService(t, func(w http.ResponseWriter, r *http.Request) {
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"bizCode":730004,"bizMsg":"task acceptance status is unknown","bizData":{"taskId":"`+testTaskID+`"}}}`)
	})

	result, err := service.CreateTask(context.Background(), CreateMessageSummaryTaskRequest{
		UgateToken: testUgate,
		Intent:     "重点总结风险和待办",
	})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if result.TaskID != testTaskID || !result.AcceptanceUnknown || result.Status != "" || result.ExpireAt != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestCreateMessageSummaryTaskBusinessErrorAndTopLevelError(t *testing.T) {
	tests := []struct {
		name     string
		response string
		wantCode string
	}{
		{
			name:     "parameter error with null bizData",
			response: `{"code":"ok","data":{"bizCode":100000,"bizMsg":"intent is required","bizData":null}}`,
			wantCode: "100000",
		},
		{
			name:     "rejected search path",
			response: `{"code":"ok","data":{"bizCode":730003,"bizMsg":"submit rejected","bizData":null}}`,
			wantCode: "730003",
		},
		{
			name:     "top level non-ok",
			response: `{"code":"fail","message":"gateway refused","data":{}}`,
			wantCode: "fail",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newSummaryTestService(t, func(w http.ResponseWriter, r *http.Request) {
				testkit.WriteJSON(w, http.StatusOK, tt.response)
			})
			_, err := service.CreateTask(context.Background(), CreateMessageSummaryTaskRequest{
				UgateToken: testUgate,
				Intent:     "重点总结风险和待办",
			})
			assertSummaryBusinessError(t, err, tt.wantCode)
		})
	}
}

func TestCreateMessageSummaryTaskMalformedResponses(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{name: "not json", response: `not-json`},
		{name: "missing bizCode", response: `{"code":"ok","data":{}}`},
		{name: "incomplete acceptance", response: `{"code":"ok","data":{"bizCode":200,"bizMsg":"accepted","bizData":{}}}`},
		{name: "unknown acceptance without taskId", response: `{"code":"ok","data":{"bizCode":730004,"bizMsg":"unknown","bizData":null}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newSummaryTestService(t, func(w http.ResponseWriter, r *http.Request) {
				testkit.WriteJSON(w, http.StatusOK, tt.response)
			})
			_, err := service.CreateTask(context.Background(), CreateMessageSummaryTaskRequest{
				UgateToken: testUgate,
				Intent:     "重点总结风险和待办",
			})
			assertSummaryMalformedError(t, err)
		})
	}
}

func TestCreateMessageSummaryTaskValidationMakesNoRequest(t *testing.T) {
	now := time.Unix(testEndTime+3600, 0)
	tests := []struct {
		name    string
		request CreateMessageSummaryTaskRequest
		ctxNil  bool
	}{
		{
			name:    "nil context",
			request: CreateMessageSummaryTaskRequest{UgateToken: testUgate, Intent: "x"},
			ctxNil:  true,
		},
		{
			name:    "blank ugate token",
			request: CreateMessageSummaryTaskRequest{UgateToken: "  ", Intent: "x"},
		},
		{
			name:    "invalid corp id when provided",
			request: CreateMessageSummaryTaskRequest{UgateToken: testUgate, CorpID: 100, Intent: "x"},
		},
		{
			name:    "negative agent id when provided",
			request: CreateMessageSummaryTaskRequest{UgateToken: testUgate, AgentID: -5, Intent: "x"},
		},
		{
			name:    "blank intent",
			request: CreateMessageSummaryTaskRequest{UgateToken: testUgate, Intent: "  "},
		},
		{
			name:    "intent too long",
			request: CreateMessageSummaryTaskRequest{UgateToken: testUgate, Intent: strings.Repeat("a", 501)},
		},
		{
			name:    "start time in the future",
			request: CreateMessageSummaryTaskRequest{UgateToken: testUgate, Intent: "x", StartTime: now.Unix() + 120},
		},
		{
			name:    "start time before three years",
			request: CreateMessageSummaryTaskRequest{UgateToken: testUgate, Intent: "x", StartTime: now.AddDate(-3, 0, 0).Unix() - 1},
		},
		{
			name:    "end before start",
			request: CreateMessageSummaryTaskRequest{UgateToken: testUgate, Intent: "x", StartTime: now.Unix() - 100, EndTime: now.Unix() - 200},
		},
		{
			name:    "end time in the future",
			request: CreateMessageSummaryTaskRequest{UgateToken: testUgate, Intent: "x", EndTime: now.Unix() + 120},
		},
		{
			name:    "keyword too long",
			request: CreateMessageSummaryTaskRequest{UgateToken: testUgate, Intent: "x", Keyword: strings.Repeat("k", 21)},
		},
		{
			name:    "too many group ids",
			request: CreateMessageSummaryTaskRequest{UgateToken: testUgate, Intent: "x", GroupIDs: make([]int64, 101)},
		},
		{
			name:    "group id not positive",
			request: CreateMessageSummaryTaskRequest{UgateToken: testUgate, Intent: "x", GroupIDs: []int64{0}},
		},
		{
			name:    "double chat contains self",
			request: CreateMessageSummaryTaskRequest{UgateToken: testUgate, AuthUser: testAuthUser, Intent: "x", DoubleChatUserIDs: []string{testAuthUser}},
		},
		{
			name:    "double chat item too long",
			request: CreateMessageSummaryTaskRequest{UgateToken: testUgate, Intent: "x", DoubleChatUserIDs: []string{strings.Repeat("u", 129)}},
		},
		{
			name:    "speaker user too long",
			request: CreateMessageSummaryTaskRequest{UgateToken: testUgate, Intent: "x", SpeakerUserID: strings.Repeat("s", 129)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newSummaryTestService(t, func(w http.ResponseWriter, r *http.Request) {
				t.Fatalf("unexpected network request to %s", r.URL.Path)
			})
			var ctx context.Context = context.Background()
			if tt.ctxNil {
				ctx = nil
			}
			_, err := service.CreateTask(ctx, tt.request)
			assertSummaryConfigError(t, err)
		})
	}
}

func TestGetMessageSummaryTaskExactQueryAndHeaders(t *testing.T) {
	var gotPath, gotMethod, gotQuery string
	var gotHeaders http.Header
	service := newSummaryTestService(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotQuery = r.URL.RawQuery
		gotHeaders = r.Header.Clone()
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"bizCode":200,"bizMsg":"success","bizData":{"taskId":"`+testTaskID+`","status":"RUNNING","result":null,"error":null,"expireAt":1767312000000}}}`)
	})

	result, err := service.GetTask(context.Background(), GetMessageSummaryTaskRequest{
		CorpID:     testCorpID,
		AgentID:    testAgentID,
		AuthUser:   testAuthUser,
		UgateToken: testUgate,
		TaskID:     testTaskID,
	})
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if gotPath != "/api/v1/im/messageSummaryTasks" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %s, want GET", gotMethod)
	}
	if gotQuery != "taskId="+testTaskID {
		t.Fatalf("query = %q", gotQuery)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer-"+testkit.Token {
		t.Fatalf("Authorization = %q", got)
	}
	// GET requires Ugate-Token per real-service smoke (50013/52002 when absent).
	if got := gotHeaders.Get("Ugate-Token"); got != testUgate {
		t.Fatalf("Ugate-Token = %q", got)
	}
	if got := gotHeaders.Get("X-OP-CORP-ID"); got != strconv.FormatInt(testCorpID, 10) {
		t.Fatalf("X-OP-CORP-ID = %q", got)
	}
	if got := gotHeaders.Get("X-OP-AGENT-ID"); got != strconv.FormatInt(testAgentID, 10) {
		t.Fatalf("X-OP-AGENT-ID = %q", got)
	}
	if got := gotHeaders.Get("X-OP-AUTH-USER"); got != testAuthUser {
		t.Fatalf("X-OP-AUTH-USER = %q", got)
	}
	if _, err := strconv.ParseInt(gotHeaders.Get("LOGID"), 10, 64); err != nil {
		t.Fatalf("LOGID = %q is not a pure number: %v", gotHeaders.Get("LOGID"), err)
	}
	if result.TaskID != testTaskID || result.Status != "RUNNING" || result.Summary != "" || result.ErrorMsg != "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestGetMessageSummaryTaskOmitsLegacyHeadersByDefault(t *testing.T) {
	var gotHeaders http.Header
	service := newSummaryTestService(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"bizCode":200,"bizMsg":"success","bizData":{"taskId":"`+testTaskID+`","status":"SUCCESS","result":{"summary":"s"},"error":null,"expireAt":1767312000000}}}`)
	})

	_, err := service.GetTask(context.Background(), GetMessageSummaryTaskRequest{
		UgateToken: testUgate,
		TaskID:     testTaskID,
	})
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	for _, key := range []string{"X-OP-CORP-ID", "X-OP-AGENT-ID", "X-OP-AUTH-USER"} {
		if got := gotHeaders.Get(key); got != "" {
			t.Fatalf("%s = %q, want absent by default", key, got)
		}
	}
	if got := gotHeaders.Get("Ugate-Token"); got != testUgate {
		t.Fatalf("Ugate-Token = %q", got)
	}
}

func TestGetMessageSummaryTaskTrimsIdentityHeaderValues(t *testing.T) {
	var gotHeaders http.Header
	service := newSummaryTestService(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"bizCode":200,"bizMsg":"success","bizData":{"taskId":"`+testTaskID+`","status":"RUNNING","result":null,"error":null,"expireAt":1767312000000}}}`)
	})

	_, err := service.GetTask(context.Background(), GetMessageSummaryTaskRequest{
		UgateToken: "  " + testUgate + "\n",
		AuthUser:   "\t" + testAuthUser + " ",
		TaskID:     testTaskID,
	})
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if got := gotHeaders.Get("Ugate-Token"); got != testUgate {
		t.Fatalf("Ugate-Token = %q, want trimmed %q", got, testUgate)
	}
	if got := gotHeaders.Get("X-OP-AUTH-USER"); got != testAuthUser {
		t.Fatalf("X-OP-AUTH-USER = %q, want trimmed %q", got, testAuthUser)
	}
}

func TestGetMessageSummaryTaskSuccessAndFailedStatus(t *testing.T) {
	t.Run("success summary", func(t *testing.T) {
		service := newSummaryTestService(t, func(w http.ResponseWriter, r *http.Request) {
			testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"bizCode":200,"bizMsg":"success","bizData":{"taskId":"`+testTaskID+`","status":"SUCCESS","result":{"summary":"line one\nline two\t tabbed"},"error":null,"expireAt":1767312000000}}}`)
		})
		result, err := service.GetTask(context.Background(), GetMessageSummaryTaskRequest{
			UgateToken: testUgate, TaskID: testTaskID,
		})
		if err != nil {
			t.Fatalf("GetTask() error = %v", err)
		}
		if result.Status != "SUCCESS" || result.Summary != "line one\nline two\t tabbed" || result.ErrorMsg != "" {
			t.Fatalf("result = %#v", result)
		}
		if !strings.Contains(string(result.RawJSON()), "SUCCESS") {
			t.Fatal("RawJSON() does not retain the response")
		}
	})
	t.Run("failed status keeps bizCode 200", func(t *testing.T) {
		service := newSummaryTestService(t, func(w http.ResponseWriter, r *http.Request) {
			testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"bizCode":200,"bizMsg":"success","bizData":{"taskId":"`+testTaskID+`","status":"FAILED","result":null,"error":{"errorMsg":"keyword search returned no results"},"expireAt":1767312000000}}}`)
		})
		result, err := service.GetTask(context.Background(), GetMessageSummaryTaskRequest{
			UgateToken: testUgate, TaskID: testTaskID,
		})
		if err != nil {
			t.Fatalf("GetTask() error = %v", err)
		}
		if result.Status != "FAILED" || result.Summary != "" || result.ErrorMsg != "keyword search returned no results" {
			t.Fatalf("result = %#v", result)
		}
	})
}

func TestGetMessageSummaryTaskBusinessError(t *testing.T) {
	service := newSummaryTestService(t, func(w http.ResponseWriter, r *http.Request) {
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"bizCode":730002,"bizMsg":"task not found or expired","bizData":null}}`)
	})
	_, err := service.GetTask(context.Background(), GetMessageSummaryTaskRequest{
		UgateToken: testUgate, TaskID: testTaskID,
	})
	assertSummaryBusinessError(t, err, "730002")
}

func TestGetMessageSummaryTaskValidationMakesNoRequest(t *testing.T) {
	tests := []struct {
		name    string
		request GetMessageSummaryTaskRequest
		ctxNil  bool
	}{
		{name: "nil context", request: GetMessageSummaryTaskRequest{UgateToken: testUgate, TaskID: testTaskID}, ctxNil: true},
		{name: "blank ugate token", request: GetMessageSummaryTaskRequest{UgateToken: " ", TaskID: testTaskID}},
		{name: "invalid corp id when provided", request: GetMessageSummaryTaskRequest{UgateToken: testUgate, CorpID: 100, TaskID: testTaskID}},
		{name: "negative agent id when provided", request: GetMessageSummaryTaskRequest{UgateToken: testUgate, AgentID: -3, TaskID: testTaskID}},
		{name: "empty task id", request: GetMessageSummaryTaskRequest{UgateToken: testUgate, TaskID: ""}},
		{name: "not a uuid", request: GetMessageSummaryTaskRequest{UgateToken: testUgate, TaskID: "not-a-uuid"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newSummaryTestService(t, func(w http.ResponseWriter, r *http.Request) {
				t.Fatalf("unexpected network request to %s", r.URL.Path)
			})
			var ctx context.Context = context.Background()
			if tt.ctxNil {
				ctx = nil
			}
			_, err := service.GetTask(ctx, tt.request)
			assertSummaryConfigError(t, err)
		})
	}
}
