// Package meeting owns meeting capabilities invoked with the Bot/App identity.
package meeting

import (
	"context"
	"encoding/json"
	"net/http"
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

type CallRequest struct {
	Subject     string   `json:"subject"`
	InviteUsers []string `json:"inviteUsers"`
	StartRecord bool     `json:"startRecord"`
	StartAIGC   bool     `json:"startAigc"`
}
type BatchInviteRequest struct {
	CID         string   `json:"cid"`
	InviteUsers []string `json:"inviteUsers"`
}
type JoinGroupRequest struct {
	GroupID                int64 `json:"groupId"`
	EnableSyncDoc          bool  `json:"enableSyncDoc"`
	EnableEventStartRemind bool  `json:"enableEventStartRemind"`
	EnableEventEndRemind   bool  `json:"enableEventEndRemind"`
}
type CIDRequest struct {
	CID string `json:"cid"`
}
type AIGCRequest struct {
	CID      string `json:"cid"`
	AIGCType int    `json:"aigcType"`
}

// FetchAigcByEventIDRequest carries the eventId created by the same
// application via createEvent with conference.autoAigc=1 enabled. It must not
// be replaced with a meeting cid.
type FetchAigcByEventIDRequest struct {
	EventID string `json:"eventId"`
}
type Result struct {
	CID string `json:"cid"`
	raw json.RawMessage
}

func (r Result) RawJSON() json.RawMessage { return append(json.RawMessage(nil), r.raw...) }

type BatchInviteResult struct {
	CID          string   `json:"cid"`
	InvitedUsers []string `json:"invitedUsers"`
	FailedUsers  []string `json:"failedUsers"`
	raw          json.RawMessage
}

func (r BatchInviteResult) RawJSON() json.RawMessage { return append(json.RawMessage(nil), r.raw...) }

type JoinGroupResult struct{ raw json.RawMessage }

func (r JoinGroupResult) RawJSON() json.RawMessage { return append(json.RawMessage(nil), r.raw...) }

type ASRResult struct {
	Items   []json.RawMessage `json:"items,omitempty"`
	ASRText string            `json:"asrText,omitempty"`
	raw     json.RawMessage
}

func (r ASRResult) RawJSON() json.RawMessage { return append(json.RawMessage(nil), r.raw...) }

type AIGCResult struct {
	Items    []json.RawMessage `json:"items,omitempty"`
	AIGCText string            `json:"aigcText,omitempty"`
	raw      json.RawMessage
}

func (r AIGCResult) RawJSON() json.RawMessage { return append(json.RawMessage(nil), r.raw...) }

type LinkInfoResult struct {
	Link       string          `json:"link,omitempty"`
	InviteLink string          `json:"inviteLink,omitempty"`
	MeetingNo  string          `json:"meetingNo,omitempty"`
	PIN        json.RawMessage `json:"pin,omitempty"`
	raw        json.RawMessage
}

func (r LinkInfoResult) RawJSON() json.RawMessage { return append(json.RawMessage(nil), r.raw...) }

func (s Service) post(ctx context.Context, route string, body any, empty bool) (json.RawMessage, json.RawMessage, error) {
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
	raw, err := s.transport.PostJSONWithHeaders(ctx, route, body, token, http.Header{"Content-Type": {"application/json; charset=utf-8"}, "LOGID": {formatLogID(s.logIDs)}})
	if err != nil {
		return nil, nil, err
	}
	data, err := envelope.DecodeLegacyBFlex(raw, empty)
	if err != nil {
		return nil, nil, err
	}
	return data, append(json.RawMessage(nil), raw...), nil
}
func formatLogID(g *runtime.MessageIDGenerator) string {
	if g == nil {
		return "1"
	}
	return fmtInt64(g.Next())
}
func fmtInt64(value int64) string {
	if value <= 0 {
		return "1"
	}
	return strconv.FormatInt(value, 10)
}
func (s Service) CallMeeting(ctx context.Context, r CallRequest) (Result, error) {
	if strings.TrimSpace(r.Subject) == "" || len(r.InviteUsers) == 0 {
		return Result{}, sdkerrors.NewConfig("call meeting", "subject and at least one inviteUser are required")
	}
	d, raw, err := s.post(ctx, routePrefix+"/callmeeting", r, false)
	return decodeResult(d, raw, err)
}
func (s Service) BatchInvite(ctx context.Context, r BatchInviteRequest) (BatchInviteResult, error) {
	if strings.TrimSpace(r.CID) == "" || len(r.InviteUsers) == 0 || len(r.InviteUsers) > 50 {
		return BatchInviteResult{}, sdkerrors.NewConfig("batch invite", "cid and 1-50 inviteUsers are required")
	}
	d, raw, err := s.post(ctx, routePrefix+"/batchInvite", r, false)
	if err != nil {
		return BatchInviteResult{}, err
	}
	var out BatchInviteResult
	if err = json.Unmarshal(d, &out); err != nil {
		return BatchInviteResult{}, sdkerrors.NewMalformed("batch invite result is invalid", raw, err)
	}
	out.raw = raw
	return out, nil
}
func (s Service) JoinGroup(ctx context.Context, r JoinGroupRequest) (JoinGroupResult, error) {
	if r.GroupID <= 0 {
		return JoinGroupResult{}, sdkerrors.NewConfig("groupId", "must be positive")
	}
	_, raw, err := s.post(ctx, routePrefix+"/joingroup", r, true)
	return JoinGroupResult{raw: raw}, err
}
func (s Service) FetchASR(ctx context.Context, r CIDRequest) (ASRResult, error) {
	if strings.TrimSpace(r.CID) == "" {
		return ASRResult{}, sdkerrors.NewConfig("cid", "is required")
	}
	d, raw, err := s.post(ctx, routePrefix+"/fetchasr", r, false)
	if err != nil {
		return ASRResult{}, err
	}
	var out ASRResult
	if err = json.Unmarshal(d, &out); err != nil {
		return ASRResult{}, sdkerrors.NewMalformed("ASR result is invalid", raw, err)
	}
	out.raw = raw
	return out, nil
}
func (s Service) FetchAIGC(ctx context.Context, r AIGCRequest) (AIGCResult, error) {
	if strings.TrimSpace(r.CID) == "" {
		return AIGCResult{}, sdkerrors.NewConfig("cid", "is required")
	}
	if r.AIGCType == 0 {
		r.AIGCType = 2
	}
	d, raw, err := s.post(ctx, routePrefix+"/fetchAigc", r, false)
	if err != nil {
		return AIGCResult{}, err
	}
	var out AIGCResult
	if err = json.Unmarshal(d, &out); err != nil {
		return AIGCResult{}, sdkerrors.NewMalformed("AIGC result is invalid", raw, err)
	}
	out.raw = raw
	return out, nil
}

// FetchAigcByEventId returns the meeting AIGC result for an eventId created by
// the same application. It mirrors the Node qyOpenApi contract route
// /workflow/api/calendar/qyOpenApi/v1/fetchAigcByEventId and shares the AIGC
// result shape with FetchAIGC.
func (s Service) FetchAigcByEventId(ctx context.Context, eventID string) (AIGCResult, error) {
	if strings.TrimSpace(eventID) == "" {
		return AIGCResult{}, sdkerrors.NewConfig("eventId", "is required")
	}
	d, raw, err := s.post(ctx, routePrefix+"/fetchAigcByEventId", FetchAigcByEventIDRequest{EventID: eventID}, false)
	if err != nil {
		return AIGCResult{}, err
	}
	var out AIGCResult
	if err = json.Unmarshal(d, &out); err != nil {
		return AIGCResult{}, sdkerrors.NewMalformed("AIGC by event ID result is invalid", raw, err)
	}
	out.raw = raw
	return out, nil
}
func (s Service) FetchLinkInfo(ctx context.Context, r CIDRequest) (LinkInfoResult, error) {
	if strings.TrimSpace(r.CID) == "" {
		return LinkInfoResult{}, sdkerrors.NewConfig("cid", "is required")
	}
	d, raw, err := s.post(ctx, routePrefix+"/fetchLinkInfo", r, false)
	if err != nil {
		return LinkInfoResult{}, err
	}
	var out LinkInfoResult
	if err = json.Unmarshal(d, &out); err != nil {
		return LinkInfoResult{}, sdkerrors.NewMalformed("meeting link result is invalid", raw, err)
	}
	out.raw = raw
	return out, nil
}
func decodeResult(data, raw json.RawMessage, err error) (Result, error) {
	if err != nil {
		return Result{}, err
	}
	out := Result{raw: raw}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			return Result{}, sdkerrors.NewMalformed("meeting result is invalid", raw, err)
		}
	}
	out.raw = raw
	return out, nil
}
