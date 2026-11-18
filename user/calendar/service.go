// Package calendar owns calendar capabilities invoked with User identity.
package calendar

import "github.com/dolfly/infoflow/user/internal/core"

type Service struct{ core.UserCalendarService }

func NewService(service core.UserCalendarService) Service {
	return Service{UserCalendarService: service}
}

type RawResult = core.UserRawResult
type Request = core.CalendarRequest
type FreeBusyRequest = core.FreeBusyRequest
type MeetingListRequest = core.MeetingListRequest
