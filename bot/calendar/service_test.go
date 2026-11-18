package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/dolfly/infoflow/internal/auth"
	"github.com/dolfly/infoflow/internal/runtime"
	"github.com/dolfly/infoflow/internal/sdkerrors"
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

func freeBusyRequest() FreeBusyRequest {
	return FreeBusyRequest{StartTime: 1700000000, EndTime: 1700003600, Emails: []string{"a@example.com"}}
}

func TestGetFreeBusyParsesObjectForm(t *testing.T) {
	service := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workflow/api/calendar/qyOpenApi/v1/freeBusy" || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		body := testkit.ReadRequestBody(t, r)
		testkit.AssertJSONEqual(t, body, `{"startTime":1700000000,"endTime":1700003600,"emails":["a@example.com"]}`)
		testkit.WriteJSON(w, http.StatusOK, `{
			"code":"ok",
			"data":{"freeBusyList":[{
				"email":"a@example.com",
				"appointments":[{"start_time":1700000100,"end_time":1700000200}]
			}]}
		}`)
	})
	result, err := service.GetFreeBusy(context.Background(), freeBusyRequest())
	if err != nil {
		t.Fatalf("GetFreeBusy() error = %v", err)
	}
	if len(result.FreeBusyList) != 1 {
		t.Fatalf("FreeBusyList len = %d, want 1", len(result.FreeBusyList))
	}
	item := result.FreeBusyList[0]
	if item.Email != "a@example.com" {
		t.Fatalf("Email = %q, want a@example.com", item.Email)
	}
	if len(item.Appointments) != 1 || item.Appointments[0].StartTime != 1700000100 || item.Appointments[0].EndTime != 1700000200 {
		t.Fatalf("Appointments = %+v, want derived start_time/end_time item", item.Appointments)
	}
	if len(item.BusySlots) != 1 || item.BusySlots[0].Start != 1700000100 || item.BusySlots[0].End != 1700000200 {
		t.Fatalf("BusySlots = %+v, want derived from appointments", item.BusySlots)
	}
	if len(result.RawJSON()) == 0 {
		t.Fatal("raw JSON not retained")
	}
}

func TestGetFreeBusyParsesArrayForm(t *testing.T) {
	service := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workflow/api/calendar/qyOpenApi/v1/freeBusy" || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		testkit.WriteJSON(w, http.StatusOK, `{
			"code":"ok",
			"data":{"code":"ok","data":[
				{"email":"a@example.com","appointments":[{"start_time":1700000100,"end_time":1700000200}]},
				{"email":"b@example.com","busySlots":[{"start":1700000300,"end":1700000400}]}
			]}
		}`)
	})
	result, err := service.GetFreeBusy(context.Background(), freeBusyRequest())
	if err != nil {
		t.Fatalf("GetFreeBusy() error = %v", err)
	}
	if len(result.FreeBusyList) != 2 {
		t.Fatalf("FreeBusyList len = %d, want 2", len(result.FreeBusyList))
	}
	first := result.FreeBusyList[0]
	if first.Email != "a@example.com" {
		t.Fatalf("first Email = %q, want a@example.com", first.Email)
	}
	if len(first.BusySlots) != 1 || first.BusySlots[0].Start != 1700000100 || first.BusySlots[0].End != 1700000200 {
		t.Fatalf("first BusySlots = %+v, want derived from appointments", first.BusySlots)
	}
	second := result.FreeBusyList[1]
	if second.Email != "b@example.com" {
		t.Fatalf("second Email = %q, want b@example.com", second.Email)
	}
	if len(second.Appointments) != 0 {
		t.Fatalf("second Appointments = %+v, want none", second.Appointments)
	}
	if len(second.BusySlots) != 1 || second.BusySlots[0].Start != 1700000300 || second.BusySlots[0].End != 1700000400 {
		t.Fatalf("second BusySlots = %+v, want preserved platform busySlots", second.BusySlots)
	}
}

func TestGetFreeBusySnakeCaseItemFields(t *testing.T) {
	var item FreeBusyItem
	if err := json.Unmarshal([]byte(`{
		"email":"a@example.com",
		"appointments":[{"start_time":1700000100,"end_time":1700000200}],
		"busySlots":[{"start":1700000100,"end":1700000200}]
	}`), &item); err != nil {
		t.Fatalf("unmarshal snake_case item: %v", err)
	}
	if item.Email != "a@example.com" {
		t.Fatalf("Email = %q, want a@example.com", item.Email)
	}
	if len(item.Appointments) != 1 || item.Appointments[0].StartTime != 1700000100 || item.Appointments[0].EndTime != 1700000200 {
		t.Fatalf("Appointments = %+v, want snake_case start_time/end_time mapped", item.Appointments)
	}
	if len(item.BusySlots) != 1 || item.BusySlots[0].Start != 1700000100 || item.BusySlots[0].End != 1700000200 {
		t.Fatalf("BusySlots = %+v, want camelCase busySlots mapped", item.BusySlots)
	}
}

func TestGetFreeBusyBusinessFailure(t *testing.T) {
	service := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		testkit.WriteJSON(w, http.StatusOK, `{"code":"fail","message":"insufficient permission"}`)
	})
	_, err := service.GetFreeBusy(context.Background(), freeBusyRequest())
	if err == nil {
		t.Fatal("GetFreeBusy() error = nil, want business failure")
	}
	var business *sdkerrors.BusinessError
	if !errors.As(err, &business) {
		t.Fatalf("error = %T (%v), want *sdkerrors.BusinessError", err, err)
	}
	if business.Code != "fail" {
		t.Fatalf("business code = %q, want fail", business.Code)
	}
}

func TestGetFreeBusyValidatesInput(t *testing.T) {
	service := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("invalid request reached network")
	})
	if _, err := service.GetFreeBusy(context.Background(), FreeBusyRequest{StartTime: 1700000000, EndTime: 1700003600}); err == nil {
		t.Fatal("missing emails did not produce an error")
	}
	if _, err := service.GetFreeBusy(context.Background(), FreeBusyRequest{StartTime: 1700003600, EndTime: 1700000000, Emails: []string{"a@example.com"}}); err == nil {
		t.Fatal("inverted time window did not produce an error")
	}
}

// TestCreateEventDefaultsBodyTypeAndConference guards the qyOpenApi wire
// contract discovered in real-service smoke: the platform rejects a missing or
// empty "bodyType" (BodyType参数错误), a missing or null "conference"
// (conference不能为null), and a conference without "bzType":1, all with
// business code 410. The SDK must supply safe defaults so a caller using the
// natural zero value gets an accepted request.
func TestCreateEventDefaultsBodyTypeAndConference(t *testing.T) {
	service := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workflow/api/calendar/qyOpenApi/v1/createEvent" || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		body := testkit.ReadRequestBody(t, r)
		testkit.AssertJSONEqual(t, body, `{"subject":"s","start":1700000000,"startTimeZone":"Asia/Shanghai","end":1700000600,"endTimeZone":"Asia/Shanghai","attendees":[{"email":"a@example.com"}],"bodyType":"Text","type":0,"allDay":false,"reminderSet":false,"reminderMinutesBeforeStart":0,"conference":{"bzType":1}}`)
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"code":200,"msg":"ok","data":{"eventId":"e1"}}}`)
	})
	result, err := service.CreateEvent(context.Background(), EventRequest{
		CalendarID:    "primary",
		Subject:       "s",
		Start:         1700000000,
		End:           1700000600,
		StartTimeZone: "Asia/Shanghai",
		EndTimeZone:   "Asia/Shanghai",
		Attendees:     []Attendee{{Email: "a@example.com"}},
	})
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}
	if result.EventID != "e1" {
		t.Fatalf("EventID = %q, want e1", result.EventID)
	}
}

// TestCreateEventSerializesExplicitBodyTypeAndConference ensures the fix does
// not drop fields a caller intentionally sets.
func TestCreateEventSerializesExplicitBodyTypeAndConference(t *testing.T) {
	service := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		body := testkit.ReadRequestBody(t, r)
		testkit.AssertJSONEqual(t, body, `{"subject":"s","start":1700000000,"startTimeZone":"Asia/Shanghai","end":1700000600,"endTimeZone":"Asia/Shanghai","attendees":[{"email":"a@example.com"}],"bodyType":"Text","type":0,"allDay":false,"reminderSet":false,"reminderMinutesBeforeStart":0,"conference":{"bzType":1,"autoRecord":1,"autoAigc":1}}`)
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"code":200,"msg":"ok","data":{"eventId":"e2"}}}`)
	})
	result, err := service.CreateEvent(context.Background(), EventRequest{
		CalendarID:    "primary",
		Subject:       "s",
		Start:         1700000000,
		End:           1700000600,
		StartTimeZone: "Asia/Shanghai",
		EndTimeZone:   "Asia/Shanghai",
		Attendees:     []Attendee{{Email: "a@example.com"}},
		BodyType:      "Text",
		Conference:    &ConferenceConfig{BZType: 1, AutoRecord: 1, AutoAIGC: 1},
	})
	if err != nil {
		t.Fatalf("CreateEvent() error = %v", err)
	}
	if result.EventID != "e2" {
		t.Fatalf("EventID = %q, want e2", result.EventID)
	}
}
