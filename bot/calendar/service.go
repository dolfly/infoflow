// Package calendar owns calendar capabilities invoked with the Bot/App identity.
package calendar

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dolfly/infoflow/internal/auth"
	"github.com/dolfly/infoflow/internal/envelope"
	"github.com/dolfly/infoflow/internal/runtime"
	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/transport"
)

const routePrefix = "/workflow/api/calendar/qyOpenApi/v1"

type Service struct {
	transport *transport.Client
	tokens    *auth.TokenManager
	logIDs    *runtime.MessageIDGenerator
}

func NewService(client *transport.Client, tokens *auth.TokenManager, logIDs *runtime.MessageIDGenerator) Service {
	return Service{transport: client, tokens: tokens, logIDs: logIDs}
}

type Attendee struct {
	Email       string `json:"email"`
	DisplayName string `json:"displayName,omitempty"`
	Name        string `json:"name,omitempty"`
	IMID        string `json:"imid,omitempty"`
}
type ConferenceConfig struct {
	BZType     int    `json:"bzType,omitempty"`
	BZContent  string `json:"bzContent,omitempty"`
	AutoRecord int    `json:"autoRecord,omitempty"`
	AutoAIGC   int    `json:"autoAigc,omitempty"`
}
type EventRequest struct {
	CalendarID                 string            `json:"-"`
	EventID                    string            `json:"-"`
	Subject                    string            `json:"subject"`
	Start                      int64             `json:"start"`
	StartTimeZone              string            `json:"startTimeZone,omitempty"`
	End                        int64             `json:"end"`
	EndTimeZone                string            `json:"endTimeZone,omitempty"`
	Attendees                  []Attendee        `json:"attendees,omitempty"`
	BodyType                   string            `json:"bodyType,omitempty"`
	Type                       int               `json:"type"`
	AllDay                     bool              `json:"allDay"`
	ReminderSet                bool              `json:"reminderSet"`
	ReminderMinutesBeforeStart int               `json:"reminderMinutesBeforeStart"`
	Conference                 *ConferenceConfig `json:"conference,omitempty"`
	Location                   string            `json:"location,omitempty"`
}

// normalizeEventRequest fills the platform-required fields that have safe
// defaults. Real-service smoke proved the qyOpenApi createEvent/updateEvent
// contract rejects both a missing/empty bodyType (`BodyType参数错误`) and a
// missing/null conference object (`conference不能为null`), and that
// `conference.bzType` must be `1`. The SDK supplies these defaults so callers
// can use the natural zero value instead of hitting business code 410.
func normalizeEventRequest(r EventRequest) EventRequest {
	if strings.TrimSpace(r.BodyType) == "" {
		r.BodyType = "Text"
	}
	if r.Conference == nil {
		r.Conference = &ConferenceConfig{}
	}
	if r.Conference.BZType == 0 {
		r.Conference.BZType = 1
	}
	return r
}

type FreeBusyRequest struct {
	StartTime int64    `json:"startTime"`
	EndTime   int64    `json:"endTime"`
	Emails    []string `json:"emails"`
}
type EventResult struct {
	EventID    string `json:"eventId"`
	CalendarID string `json:"calendarId,omitempty"`
	Subject    string `json:"subject,omitempty"`
	Updated    bool   `json:"updated,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
	raw        json.RawMessage
}

func (r EventResult) RawJSON() json.RawMessage { return append(json.RawMessage(nil), r.raw...) }

// FreeBusyAppointment is one raw busy appointment returned by the free/busy
// API. The platform emits snake_case wire names; start_time/end_time are Unix
// second timestamps.
type FreeBusyAppointment struct {
	StartTime int64 `json:"start_time"`
	EndTime   int64 `json:"end_time"`
}

// BusySlot is a normalized busy interval with camelCase wire names, aligned
// with the Node reference FreeBusyItem.busySlots.
type BusySlot struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// FreeBusyItem is one user's free/busy state, aligned with the Node reference
// FreeBusyItem. BusySlots is always populated: the platform may return it
// directly, otherwise it is derived from Appointments.
type FreeBusyItem struct {
	Email        string                `json:"email,omitempty"`
	Appointments []FreeBusyAppointment `json:"appointments,omitempty"`
	BusySlots    []BusySlot            `json:"busySlots,omitempty"`
}

// FreeBusyResult is the free/busy query result. The platform returns either an
// object ({ "freeBusyList": [...] }) or a bare array (the qyOpenApi nested
// data.data form); both shapes are normalized into FreeBusyList.
type FreeBusyResult struct {
	FreeBusyList []FreeBusyItem `json:"freeBusyList,omitempty"`
	raw          json.RawMessage
}

func (r FreeBusyResult) RawJSON() json.RawMessage { return append(json.RawMessage(nil), r.raw...) }

// UnmarshalJSON accepts both response shapes: an object carrying a
// freeBusyList field and a bare array of items (qyOpenApi nested data.data
// form). It then derives busySlots from appointments when the platform did not
// already return them.
func (r *FreeBusyResult) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	switch {
	case len(trimmed) > 0 && trimmed[0] == '[':
		var items []FreeBusyItem
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return err
		}
		r.FreeBusyList = items
	default:
		var wire struct {
			FreeBusyList []FreeBusyItem `json:"freeBusyList"`
		}
		if err := json.Unmarshal(trimmed, &wire); err != nil {
			return err
		}
		r.FreeBusyList = wire.FreeBusyList
	}
	r.normalizeBusySlots()
	return nil
}

// normalizeBusySlots guarantees a non-nil BusySlots on every item. When the
// platform returned busySlots they are preserved; otherwise busySlots is
// derived from appointments (start_time/end_time -> start/end), matching the
// Node reference normalizeFreeBusyItem.
func (r *FreeBusyResult) normalizeBusySlots() {
	for i := range r.FreeBusyList {
		item := &r.FreeBusyList[i]
		if item.BusySlots != nil {
			continue
		}
		if len(item.Appointments) == 0 {
			item.BusySlots = []BusySlot{}
			continue
		}
		slots := make([]BusySlot, 0, len(item.Appointments))
		for _, appointment := range item.Appointments {
			slots = append(slots, BusySlot{Start: appointment.StartTime, End: appointment.EndTime})
		}
		item.BusySlots = slots
	}
}

func (s Service) call(ctx context.Context, method, route string, query url.Values, body any, empty bool) (json.RawMessage, json.RawMessage, error) {
	if ctx == nil {
		return nil, nil, sdkerrors.NewConfig("context", "must not be nil")
	}
	if strings.TrimSpace(route) == "" {
		return nil, nil, sdkerrors.NewConfig("route", "must not be blank")
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return nil, nil, err
	}
	headers := http.Header{"Content-Type": {"application/json; charset=utf-8"}, "LOGID": {strconv.FormatInt(s.logIDs.Next(), 10)}}
	var raw []byte
	switch method {
	case http.MethodGet:
		raw, err = s.transport.GetWithHeaders(ctx, route, query, token, headers)
	case http.MethodPost:
		raw, err = s.transport.PostJSONWithHeaders(ctx, routeWithQuery(route, query), body, token, headers)
	case http.MethodPut:
		raw, err = s.transport.PutJSONWithHeaders(ctx, routeWithQuery(route, query), body, token, headers)
	case http.MethodDelete:
		raw, err = s.transport.DeleteWithHeaders(ctx, route, query, token, headers)
	}
	if err != nil {
		return nil, nil, err
	}
	data, err := envelope.DecodeLegacyBFlex(raw, empty)
	if err != nil {
		return nil, nil, err
	}
	return data, append(json.RawMessage(nil), raw...), nil
}
func routeWithQuery(route string, query url.Values) string {
	if len(query) == 0 {
		return route
	}
	return route + "?" + query.Encode()
}
func eventResult(data, raw json.RawMessage, err error) (EventResult, error) {
	if err != nil {
		return EventResult{}, err
	}
	result := EventResult{raw: raw}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &result); err != nil {
			return EventResult{}, sdkerrors.NewMalformed("calendar result is invalid", raw, err)
		}
	}
	result.raw = raw
	return result, nil
}
func (s Service) CreateEvent(ctx context.Context, r EventRequest) (EventResult, error) {
	if strings.TrimSpace(r.CalendarID) == "" || strings.TrimSpace(r.Subject) == "" || r.Start <= 0 || r.End <= r.Start {
		return EventResult{}, sdkerrors.NewConfig("calendar event", "calendarId, subject, and valid start/end are required")
	}
	r = normalizeEventRequest(r)
	d, raw, err := s.call(ctx, http.MethodPost, routePrefix+"/createEvent", url.Values{"calendarId": {r.CalendarID}}, r, false)
	return eventResult(d, raw, err)
}
func (s Service) UpdateEvent(ctx context.Context, r EventRequest) (EventResult, error) {
	if strings.TrimSpace(r.CalendarID) == "" || strings.TrimSpace(r.EventID) == "" || strings.TrimSpace(r.Subject) == "" || r.Start <= 0 || r.End <= r.Start {
		return EventResult{}, sdkerrors.NewConfig("calendar event", "calendarId, eventId, subject, and valid start/end are required")
	}
	r = normalizeEventRequest(r)
	d, raw, err := s.call(ctx, http.MethodPut, routePrefix+"/updateEvent", url.Values{"calendarId": {r.CalendarID}, "eventId": {r.EventID}}, r, false)
	return eventResult(d, raw, err)
}
func (s Service) DeleteEvent(ctx context.Context, r EventRequest) (EventResult, error) {
	if strings.TrimSpace(r.CalendarID) == "" || strings.TrimSpace(r.EventID) == "" {
		return EventResult{}, sdkerrors.NewConfig("calendar event", "calendarId and eventId are required")
	}
	d, raw, err := s.call(ctx, http.MethodDelete, routePrefix+"/deleteEvent", url.Values{"calendarId": {r.CalendarID}, "eventId": {r.EventID}}, nil, true)
	return eventResult(d, raw, err)
}
func (s Service) GetEvent(ctx context.Context, r EventRequest) (EventResult, error) {
	if strings.TrimSpace(r.CalendarID) == "" || strings.TrimSpace(r.EventID) == "" {
		return EventResult{}, sdkerrors.NewConfig("calendar event", "calendarId and eventId are required")
	}
	d, raw, err := s.call(ctx, http.MethodGet, routePrefix+"/getEvent", url.Values{"calendarId": {r.CalendarID}, "eventId": {r.EventID}}, nil, false)
	return eventResult(d, raw, err)
}
func (s Service) GetFreeBusy(ctx context.Context, r FreeBusyRequest) (FreeBusyResult, error) {
	if r.StartTime <= 0 || r.EndTime <= r.StartTime || len(r.Emails) == 0 {
		return FreeBusyResult{}, sdkerrors.NewConfig("free busy", "valid time window and emails are required")
	}
	d, raw, err := s.call(ctx, http.MethodPost, routePrefix+"/freeBusy", nil, r, false)
	if err != nil {
		return FreeBusyResult{}, err
	}
	var result FreeBusyResult
	if err := json.Unmarshal(d, &result); err != nil {
		return FreeBusyResult{}, sdkerrors.NewMalformed("free busy result is invalid", raw, err)
	}
	result.raw = raw
	return result, nil
}
