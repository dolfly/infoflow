package im

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/dolfly/infoflow/internal/auth"
	"github.com/dolfly/infoflow/internal/envelope"
	internalruntime "github.com/dolfly/infoflow/internal/runtime"
	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/transport"
)

// MessageService sends messages through the shared auth and transport layers.
type MessageService struct {
	transport  *transport.Client
	tokens     *auth.TokenManager
	messageIDs *internalruntime.MessageIDGenerator
}

func NewMessageService(client *transport.Client, tokens *auth.TokenManager, messageIDs *internalruntime.MessageIDGenerator) MessageService {
	return MessageService{transport: client, tokens: tokens, messageIDs: messageIDs}
}

// SendToUserResult is the typed result of a user text send.
type SendToUserResult struct {
	MessageKey  int64
	InvalidUser string
	raw         json.RawMessage
}

func (r SendToUserResult) RawJSON() json.RawMessage {
	return append(json.RawMessage(nil), r.raw...)
}

// SendToGroupResult is the typed result of a group text send.
type SendToGroupResult struct {
	MessageID         int64
	MessageSequenceID int64
	CreatedAtMillis   int64
	raw               json.RawMessage
}

func (r SendToGroupResult) RawJSON() json.RawMessage {
	return append(json.RawMessage(nil), r.raw...)
}

type userTextRequest struct {
	ToUser  string          `json:"touser"`
	MsgType string          `json:"msgtype"`
	Text    userTextContent `json:"text"`
}

type userTextContent struct {
	Content string `json:"content"`
}

type userTextResponse struct {
	MessageKey  *int64  `json:"msgkey"`
	InvalidUser *string `json:"invaliduser"`
}

func (s *MessageService) SendToUser(ctx context.Context, userID, text string) (SendToUserResult, error) {
	if strings.TrimSpace(userID) == "" {
		return SendToUserResult{}, sdkerrors.NewConfig("userID", "must not be blank")
	}
	if strings.TrimSpace(text) == "" {
		return SendToUserResult{}, sdkerrors.NewConfig("text", "must not be blank")
	}
	if ctx == nil {
		return SendToUserResult{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return SendToUserResult{}, err
	}
	raw, err := s.transport.PostJSON(ctx, "/app/message/send", userTextRequest{
		ToUser:  userID,
		MsgType: "text",
		Text:    userTextContent{Content: text},
	}, token)
	if err != nil {
		return SendToUserResult{}, err
	}
	env, err := envelope.DecodeBusiness(raw)
	if err != nil {
		return SendToUserResult{}, err
	}
	var response userTextResponse
	if err := json.Unmarshal(env.Data, &response); err != nil {
		return SendToUserResult{}, sdkerrors.NewMalformed("user message result is invalid", raw, err)
	}
	if response.MessageKey == nil {
		return SendToUserResult{}, sdkerrors.NewMalformed("user message result is incomplete", raw, nil)
	}
	invalidUser := ""
	if response.InvalidUser != nil {
		invalidUser = *response.InvalidUser
	}
	return SendToUserResult{
		MessageKey:  *response.MessageKey,
		InvalidUser: invalidUser,
		raw:         append(json.RawMessage(nil), raw...),
	}, nil
}

type groupTextRequest struct {
	Message groupMessage `json:"message"`
}

type groupMessage struct {
	Header groupHeader `json:"header"`
	Body   []groupBody `json:"body"`
}

type groupHeader struct {
	ToID            int64  `json:"toid"`
	ToType          string `json:"totype"`
	MsgType         string `json:"msgtype"`
	ClientMessageID int64  `json:"clientmsgid"`
	Role            string `json:"role"`
}

type groupBody struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

type groupTextResponse struct {
	Data *groupTextResult `json:"data"`
}

type groupTextResult struct {
	MessageID         *int64 `json:"messageid"`
	MessageSequenceID *int64 `json:"msgseqid"`
	CreatedAtMillis   *int64 `json:"ctime"`
}

func (s *MessageService) SendToGroup(ctx context.Context, groupID int64, text string) (SendToGroupResult, error) {
	if groupID <= 0 {
		return SendToGroupResult{}, sdkerrors.NewConfig("groupID", "must be positive")
	}
	if strings.TrimSpace(text) == "" {
		return SendToGroupResult{}, sdkerrors.NewConfig("text", "must not be blank")
	}
	if ctx == nil {
		return SendToGroupResult{}, sdkerrors.NewConfig("context", "must not be nil")
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return SendToGroupResult{}, err
	}
	raw, err := s.transport.PostJSON(ctx, "/robot/msg/groupmsgsend", groupTextRequest{
		Message: groupMessage{
			Header: groupHeader{
				ToID:            groupID,
				ToType:          "GROUP",
				MsgType:         "TEXT",
				ClientMessageID: s.messageIDs.Next(),
				Role:            "robot",
			},
			Body: []groupBody{{Type: "TEXT", Content: text}},
		},
	}, token)
	if err != nil {
		return SendToGroupResult{}, err
	}
	env, err := envelope.DecodeBusiness(raw)
	if err != nil {
		return SendToGroupResult{}, err
	}
	var response groupTextResponse
	if err := json.Unmarshal(env.Data, &response); err != nil {
		return SendToGroupResult{}, sdkerrors.NewMalformed("group message result is invalid", raw, err)
	}
	if response.Data == nil || response.Data.MessageID == nil ||
		response.Data.MessageSequenceID == nil || response.Data.CreatedAtMillis == nil {
		return SendToGroupResult{}, sdkerrors.NewMalformed("group message result is incomplete", raw, nil)
	}
	return SendToGroupResult{
		MessageID:         *response.Data.MessageID,
		MessageSequenceID: *response.Data.MessageSequenceID,
		CreatedAtMillis:   *response.Data.CreatedAtMillis,
		raw:               append(json.RawMessage(nil), raw...),
	}, nil
}
