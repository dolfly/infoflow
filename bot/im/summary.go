package im

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dolfly/infoflow/internal/auth"
	"github.com/dolfly/infoflow/internal/envelope"
	internalruntime "github.com/dolfly/infoflow/internal/runtime"
	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/transport"
)

// messageSummaryRoute is the generic message summary task endpoint shared by
// creation (POST) and query (GET).
const messageSummaryRoute = "/im/messageSummaryTasks"

const (
	messageSummaryMaxTimestamp = 8796093022 // documented Unix-seconds cap
	messageSummaryClockSkew    = 60 * time.Second
	messageSummaryCorpBaidu    = 1
	messageSummaryCorpMin      = 2100000000
)

var taskIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// MessageSummaryService submits and polls asynchronous generic message summary
// tasks through the shared auth, transport, envelope, and error layers.
type MessageSummaryService struct {
	transport *transport.Client
	tokens    *auth.TokenManager
	logIDs    *internalruntime.MessageIDGenerator
	now       func() time.Time
}

// NewMessageSummaryService assembles the generic message summary service. The
// now function is used only for local input validation and LOGID generation.
func NewMessageSummaryService(client *transport.Client, tokens *auth.TokenManager, logIDs *internalruntime.MessageIDGenerator, now func() time.Time) MessageSummaryService {
	if now == nil {
		now = time.Now
	}
	return MessageSummaryService{transport: client, tokens: tokens, logIDs: logIDs, now: now}
}

// CreateMessageSummaryTaskRequest submits a generic message summary task.
// UgateToken is the required user identity carrier and is sent as the
// Ugate-Token header for both POST and GET (real-service verified 2026-09-07).
// CorpID, AgentID, and AuthUser are legacy identity fields kept only for
// backward compatibility: they are optional and are NOT sent by default. When
// explicitly provided they are emitted as X-OP-* headers, but the server does
// not validate them. The remaining fields form the JSON body. StartTime and
// EndTime are Unix seconds (not milliseconds).
type CreateMessageSummaryTaskRequest struct {
	UgateToken string `json:"-"`

	// Deprecated: optional and only sent as X-OP-CORP-ID when non-zero. The
	// server does not validate this header.
	CorpID int64 `json:"-"`
	// Deprecated: optional and only sent as X-OP-AGENT-ID when non-zero. The
	// server does not validate this header.
	AgentID int64 `json:"-"`
	// Deprecated: optional and only sent as X-OP-AUTH-USER when non-blank. The
	// server does not validate this header.
	AuthUser string `json:"-"`

	Intent            string   `json:"intent"`
	StartTime         int64    `json:"startTime,omitempty"`
	EndTime           int64    `json:"endTime,omitempty"`
	Keyword           string   `json:"keyword,omitempty"`
	GroupIDs          []int64  `json:"groupIds,omitempty"`
	DoubleChatUserIDs []string `json:"doubleChatUserIds,omitempty"`
	SpeakerUserID     string   `json:"speakerUserId,omitempty"`
}

// CreateMessageSummaryTaskResult is the acceptance response. Status and
// ExpireAt are zero when AcceptanceUnknown is true (bizCode 730004): the task
// may have been created, so callers must confirm it with GetTask before
// resubmitting.
type CreateMessageSummaryTaskResult struct {
	TaskID            string
	Status            string
	ExpireAt          int64
	AcceptanceUnknown bool
	raw               json.RawMessage
}

// RawJSON returns the complete original response defensively copied.
func (r CreateMessageSummaryTaskResult) RawJSON() json.RawMessage {
	return append(json.RawMessage(nil), r.raw...)
}

// GetMessageSummaryTaskRequest queries one task by ID. UgateToken is the
// required user identity carrier for GET (missing it returns 50013/52002).
// CorpID, AgentID, and AuthUser are legacy optional identity fields kept only
// for backward compatibility; they are not sent by default.
type GetMessageSummaryTaskRequest struct {
	UgateToken string

	// Deprecated: optional and only sent as X-OP-CORP-ID when non-zero.
	CorpID int64
	// Deprecated: optional and only sent as X-OP-AGENT-ID when non-zero.
	AgentID int64
	// Deprecated: optional and only sent as X-OP-AUTH-USER when non-blank.
	AuthUser string

	TaskID string
}

// GetMessageSummaryTaskResult is one polled task snapshot. Summary is present
// for SUCCESS/PARTIAL_SUCCESS and ErrorMsg for FAILED. Terminal statuses are
// SUCCESS, PARTIAL_SUCCESS, and FAILED.
type GetMessageSummaryTaskResult struct {
	TaskID   string
	Status   string
	Summary  string
	ErrorMsg string
	ExpireAt int64
	raw      json.RawMessage
}

// RawJSON returns the complete original response defensively copied.
func (r GetMessageSummaryTaskResult) RawJSON() json.RawMessage {
	return append(json.RawMessage(nil), r.raw...)
}

type createSummaryTaskWire struct {
	TaskID   string `json:"taskId"`
	Status   string `json:"status"`
	ExpireAt int64  `json:"expireAt"`
}

type getSummaryTaskWire struct {
	TaskID   string          `json:"taskId"`
	Status   string          `json:"status"`
	Result   *summaryResult  `json:"result"`
	Error    *summaryTaskErr `json:"error"`
	ExpireAt int64           `json:"expireAt"`
}

type summaryResult struct {
	Summary string `json:"summary"`
}

type summaryTaskErr struct {
	ErrorMsg string `json:"errorMsg"`
}

// CreateTask submits an asynchronous generic message summary task and returns
// its task ID for polling. Local input validation runs without any network I/O.
func (s MessageSummaryService) CreateTask(ctx context.Context, request CreateMessageSummaryTaskRequest) (CreateMessageSummaryTaskResult, error) {
	if ctx == nil {
		return CreateMessageSummaryTaskResult{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	if err := s.validateCreate(request); err != nil {
		return CreateMessageSummaryTaskResult{}, err
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return CreateMessageSummaryTaskResult{}, err
	}
	raw, err := s.transport.PostJSONWithHeaders(ctx, messageSummaryRoute, request, token, s.createHeaders(request))
	if err != nil {
		return CreateMessageSummaryTaskResult{}, err
	}
	env, err := envelope.DecodeEnvelopeC(raw)
	if err != nil {
		return CreateMessageSummaryTaskResult{}, err
	}
	var wire createSummaryTaskWire
	switch env.BizCode {
	case "200":
		if err := json.Unmarshal(env.BizData, &wire); err != nil || wire.TaskID == "" || wire.Status == "" {
			return CreateMessageSummaryTaskResult{}, sdkerrors.NewMalformed("summary task acceptance is incomplete", raw, err)
		}
		return CreateMessageSummaryTaskResult{
			TaskID:   wire.TaskID,
			Status:   wire.Status,
			ExpireAt: wire.ExpireAt,
			raw:      append(json.RawMessage(nil), raw...),
		}, nil
	case "730004":
		if err := json.Unmarshal(env.BizData, &wire); err != nil || wire.TaskID == "" {
			return CreateMessageSummaryTaskResult{}, sdkerrors.NewMalformed("summary task acceptance is unknown", raw, err)
		}
		return CreateMessageSummaryTaskResult{
			TaskID:            wire.TaskID,
			AcceptanceUnknown: true,
			raw:               append(json.RawMessage(nil), raw...),
		}, nil
	default:
		return CreateMessageSummaryTaskResult{}, sdkerrors.NewBusiness(env.BizCode, env.BizMsg, raw)
	}
}

// GetTask polls one summary task by ID. Business failures such as 730001,
// 730002, 730003, and 730004 are propagated as typed BusinessError values.
func (s MessageSummaryService) GetTask(ctx context.Context, request GetMessageSummaryTaskRequest) (GetMessageSummaryTaskResult, error) {
	if ctx == nil {
		return GetMessageSummaryTaskResult{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	if err := s.validateGet(request); err != nil {
		return GetMessageSummaryTaskResult{}, err
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return GetMessageSummaryTaskResult{}, err
	}
	query := url.Values{"taskId": {request.TaskID}}
	raw, err := s.transport.GetWithHeaders(ctx, messageSummaryRoute, query, token, s.getHeaders(request))
	if err != nil {
		return GetMessageSummaryTaskResult{}, err
	}
	env, err := envelope.DecodeEnvelopeC(raw)
	if err != nil {
		return GetMessageSummaryTaskResult{}, err
	}
	if env.BizCode != "200" {
		return GetMessageSummaryTaskResult{}, sdkerrors.NewBusiness(env.BizCode, env.BizMsg, raw)
	}
	var wire getSummaryTaskWire
	if err := json.Unmarshal(env.BizData, &wire); err != nil || wire.TaskID == "" || wire.Status == "" {
		return GetMessageSummaryTaskResult{}, sdkerrors.NewMalformed("summary task result is invalid", raw, err)
	}
	result := GetMessageSummaryTaskResult{
		TaskID:   wire.TaskID,
		Status:   wire.Status,
		ExpireAt: wire.ExpireAt,
		raw:      append(json.RawMessage(nil), raw...),
	}
	if wire.Result != nil {
		result.Summary = wire.Result.Summary
	}
	if wire.Error != nil {
		result.ErrorMsg = wire.Error.ErrorMsg
	}
	return result, nil
}

func (s MessageSummaryService) createHeaders(request CreateMessageSummaryTaskRequest) http.Header {
	headers := http.Header{"LOGID": {strconv.FormatInt(s.logIDs.Next(), 10)}}
	if token := strings.TrimSpace(request.UgateToken); token != "" {
		headers.Set("Ugate-Token", token)
	}
	// X-OP-* headers are only emitted for backward compatibility when the
	// caller explicitly supplies the legacy identity fields; they are not
	// required and the server does not validate them.
	if request.CorpID != 0 {
		headers.Set("X-OP-CORP-ID", strconv.FormatInt(request.CorpID, 10))
	}
	if request.AgentID != 0 {
		headers.Set("X-OP-AGENT-ID", strconv.FormatInt(request.AgentID, 10))
	}
	if user := strings.TrimSpace(request.AuthUser); user != "" {
		headers.Set("X-OP-AUTH-USER", user)
	}
	return headers
}

func (s MessageSummaryService) getHeaders(request GetMessageSummaryTaskRequest) http.Header {
	headers := http.Header{"LOGID": {strconv.FormatInt(s.logIDs.Next(), 10)}}
	if token := strings.TrimSpace(request.UgateToken); token != "" {
		headers.Set("Ugate-Token", token)
	}
	if request.CorpID != 0 {
		headers.Set("X-OP-CORP-ID", strconv.FormatInt(request.CorpID, 10))
	}
	if request.AgentID != 0 {
		headers.Set("X-OP-AGENT-ID", strconv.FormatInt(request.AgentID, 10))
	}
	if user := strings.TrimSpace(request.AuthUser); user != "" {
		headers.Set("X-OP-AUTH-USER", user)
	}
	return headers
}

func (s MessageSummaryService) validateCreate(request CreateMessageSummaryTaskRequest) error {
	if strings.TrimSpace(request.UgateToken) == "" {
		return sdkerrors.NewConfig("UgateToken", "must not be blank")
	}
	if request.CorpID != 0 && !validCorpID(request.CorpID) {
		return sdkerrors.NewConfig("CorpID", "when provided must be 1 or at least 2100000000")
	}
	if request.AgentID != 0 && request.AgentID < 1 {
		return sdkerrors.NewConfig("AgentID", "when provided must be positive")
	}
	authUser := strings.TrimSpace(request.AuthUser)
	if strings.TrimSpace(request.Intent) == "" {
		return sdkerrors.NewConfig("Intent", "must not be blank")
	}
	if utf8.RuneCountInString(request.Intent) > 500 {
		return sdkerrors.NewConfig("Intent", "must not exceed 500 characters")
	}
	now := s.now()
	if request.StartTime != 0 {
		if request.StartTime < 1 || request.StartTime > messageSummaryMaxTimestamp {
			return sdkerrors.NewConfig("StartTime", "must be a positive Unix second within the documented range")
		}
		start := time.Unix(request.StartTime, 0)
		if start.Before(now.AddDate(-3, 0, 0)) {
			return sdkerrors.NewConfig("StartTime", "must not be earlier than 3 years ago")
		}
		if start.After(now.Add(messageSummaryClockSkew)) {
			return sdkerrors.NewConfig("StartTime", "must not be in the future")
		}
	}
	if request.EndTime != 0 {
		if request.EndTime < 1 || request.EndTime > messageSummaryMaxTimestamp {
			return sdkerrors.NewConfig("EndTime", "must be a positive Unix second within the documented range")
		}
		if time.Unix(request.EndTime, 0).After(now.Add(messageSummaryClockSkew)) {
			return sdkerrors.NewConfig("EndTime", "must not be in the future")
		}
	}
	if request.StartTime != 0 && request.EndTime != 0 && request.StartTime >= request.EndTime {
		return sdkerrors.NewConfig("time window", "endTime must be greater than startTime")
	}
	if utf8.RuneCountInString(request.Keyword) > 20 {
		return sdkerrors.NewConfig("Keyword", "must not exceed 20 characters")
	}
	if len(request.GroupIDs) > 100 {
		return sdkerrors.NewConfig("GroupIDs", "must not exceed 100 items")
	}
	for _, id := range request.GroupIDs {
		if id < 1 {
			return sdkerrors.NewConfig("GroupIDs", "must contain only positive IDs")
		}
	}
	if len(request.DoubleChatUserIDs) > 100 {
		return sdkerrors.NewConfig("DoubleChatUserIDs", "must not exceed 100 items")
	}
	for _, user := range request.DoubleChatUserIDs {
		length := utf8.RuneCountInString(user)
		if length < 1 || length > 128 {
			return sdkerrors.NewConfig("DoubleChatUserIDs", "each item must be 1-128 characters")
		}
		if strings.TrimSpace(user) == authUser {
			return sdkerrors.NewConfig("DoubleChatUserIDs", "must not contain the authenticated user")
		}
	}
	if utf8.RuneCountInString(request.SpeakerUserID) > 128 {
		return sdkerrors.NewConfig("SpeakerUserID", "must not exceed 128 characters")
	}
	return nil
}

func validCorpID(corpID int64) bool {
	return corpID == messageSummaryCorpBaidu || corpID >= messageSummaryCorpMin
}

func (s MessageSummaryService) validateGet(request GetMessageSummaryTaskRequest) error {
	if strings.TrimSpace(request.UgateToken) == "" {
		return sdkerrors.NewConfig("UgateToken", "must not be blank")
	}
	if request.CorpID != 0 && !validCorpID(request.CorpID) {
		return sdkerrors.NewConfig("CorpID", "when provided must be 1 or at least 2100000000")
	}
	if request.AgentID != 0 && request.AgentID < 1 {
		return sdkerrors.NewConfig("AgentID", "when provided must be positive")
	}
	if !taskIDPattern.MatchString(request.TaskID) {
		return sdkerrors.NewConfig("TaskID", "must be a valid UUID")
	}
	return nil
}
