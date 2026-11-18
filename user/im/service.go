// Package im owns instant-messaging capabilities invoked with User identity.
package im

import (
	"encoding/json"

	"github.com/dolfly/infoflow/user/internal/core"
)

type Service struct{ core.UserMessageService }

type Scope struct{ Message Service }

func NewService(service core.UserMessageService) Service { return Service{UserMessageService: service} }

type RawResult = core.UserRawResult
type LatestContactsRequest = core.LatestContactsRequest
type Contact = core.UserContact
type LatestContactsResult = core.LatestContactsResult
type CreateSummaryRequest = core.CreateSummaryRequest
type ImportSummaryRequest = core.ImportSummaryRequest
type SummaryResult = core.SummaryResult

func NewRawResult(data, raw json.RawMessage) RawResult { return core.NewRawResult(data, raw) }
