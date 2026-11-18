// Package meeting owns meeting capabilities invoked with User identity.
package meeting

import "github.com/dolfly/infoflow/user/internal/core"

type Service struct{ core.UserMeetingService }

func NewService(service core.UserMeetingService) Service { return Service{UserMeetingService: service} }

type RawResult = core.UserRawResult
type ListRequest = core.MeetingListRequest
type CallRequest = core.CallMeetingRequest
