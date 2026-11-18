package im

// The IM methods in this file intentionally keep polymorphic message content
// as JSON. The platform documents several evolving content shapes; decoding a
// subset here would silently discard fields such as image downloadUrl.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dolfly/infoflow/internal/envelope"
	"github.com/dolfly/infoflow/internal/sdkerrors"
)

type UserReply struct {
	Content    string `json:"content"`
	UID        string `json:"uid"`
	MessageID  string `json:"msgid"`
	MessageID2 string `json:"msgid2"`
}
type RichTextNode struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Href  string `json:"href,omitempty"`
	Label string `json:"label,omitempty"`
}
type NewsArticle struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url"`
	PictureURL  string `json:"picurl,omitempty"`
}
type SendUserMessageRequest struct {
	ToUser   string         `json:"touser"`
	Type     string         `json:"msgtype"`
	Text     string         `json:"-"`
	Image    string         `json:"-"`
	RichText []RichTextNode `json:"-"`
	Articles []NewsArticle  `json:"-"`
	Reply    []UserReply    `json:"reply,omitempty"`
}

func (s *MessageService) SendUserMessage(ctx context.Context, request SendUserMessageRequest) (SendToUserResult, error) {
	if ctx == nil || strings.TrimSpace(request.ToUser) == "" || strings.TrimSpace(request.Type) == "" {
		return SendToUserResult{}, sdkerrors.NewConfig("message", "context, recipient, and type are required")
	}
	t := strings.ToLower(request.Type)
	body := map[string]any{"touser": request.ToUser, "msgtype": t}
	switch t {
	case "text", "md":
		if strings.TrimSpace(request.Text) == "" {
			return SendToUserResult{}, sdkerrors.NewConfig("content", "must not be blank")
		}
		body[t] = map[string]string{"content": request.Text}
	case "image":
		if strings.TrimSpace(request.Image) == "" {
			return SendToUserResult{}, sdkerrors.NewConfig("image", "must not be blank")
		}
		body[t] = map[string]string{"content": request.Image}
	case "richtext":
		if len(request.RichText) == 0 {
			return SendToUserResult{}, sdkerrors.NewConfig("richtext", "must not be empty")
		}
		for _, node := range request.RichText {
			if !validRichTextNode(node) {
				return SendToUserResult{}, sdkerrors.NewConfig("richtext", "contains an invalid content node")
			}
		}
		body[t] = map[string]any{"content": request.RichText}
	case "news":
		if len(request.Articles) == 0 {
			return SendToUserResult{}, sdkerrors.NewConfig("articles", "must not be empty")
		}
		for _, article := range request.Articles {
			if strings.TrimSpace(article.Title) == "" || strings.TrimSpace(article.URL) == "" {
				return SendToUserResult{}, sdkerrors.NewConfig("articles", "title and URL are required")
			}
		}
		body[t] = map[string]any{"articles": request.Articles}
	default:
		return SendToUserResult{}, sdkerrors.NewConfig("type", "unsupported user message type")
	}
	if len(request.Reply) > 0 {
		body["reply"] = request.Reply
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return SendToUserResult{}, err
	}
	raw, err := s.transport.PostJSON(ctx, "/app/message/send", body, token)
	if err != nil {
		return SendToUserResult{}, err
	}
	env, err := envelope.DecodeBusiness(raw)
	if err != nil {
		return SendToUserResult{}, err
	}
	var r userTextResponse
	if err := json.Unmarshal(env.Data, &r); err != nil || r.MessageKey == nil {
		return SendToUserResult{}, sdkerrors.NewMalformed("user message result is incomplete", raw, err)
	}
	result := SendToUserResult{MessageKey: *r.MessageKey, raw: append(json.RawMessage(nil), raw...)}
	if r.InvalidUser != nil {
		result.InvalidUser = *r.InvalidUser
	}
	return result, nil
}

func validRichTextNode(node RichTextNode) bool {
	switch node.Type {
	case "text":
		return strings.TrimSpace(node.Text) != ""
	case "a":
		return strings.TrimSpace(node.Href) != "" && strings.TrimSpace(node.Label) != ""
	default:
		return false
	}
}

type GroupBody struct {
	Type          string   `json:"type"`
	Content       string   `json:"content,omitempty"`
	Href          string   `json:"href,omitempty"`
	Label         string   `json:"label,omitempty"`
	AtAll         bool     `json:"atall,omitempty"`
	AtUserIDs     []string `json:"atuserids,omitempty"`
	AtAgentIDs    []int64  `json:"atagentids,omitempty"`
	CommandName   string   `json:"commandname,omitempty"`
	TargetAgentID int64    `json:"targetAgentId,omitempty"`
}
type GroupReply struct {
	MessageID string `json:"messageid"`
	Preview   string `json:"preview"`
	IMID      string `json:"imid"`
	ReplyType string `json:"replytype,omitempty"`
}
type SendGroupMessageRequest struct {
	GroupID         int64
	Type            string
	Body            []GroupBody
	Reply           *GroupReply
	ClientMessageID int64
}

func (s *MessageService) SendGroupMessage(ctx context.Context, request SendGroupMessageRequest) (SendToGroupResult, error) {
	if ctx == nil || request.GroupID <= 0 || !validGroupMessageType(request.Type) || len(request.Body) == 0 {
		return SendToGroupResult{}, sdkerrors.NewConfig("group message", "context, groupID, type, and body are required")
	}
	if request.ClientMessageID == 0 {
		request.ClientMessageID = s.messageIDs.Next()
	}
	if request.ClientMessageID < 0 {
		return SendToGroupResult{}, sdkerrors.NewConfig("client message ID", "must be positive")
	}
	for _, b := range request.Body {
		if !validGroupBody(b) {
			return SendToGroupResult{}, sdkerrors.NewConfig("body", "contains an invalid group message element")
		}
	}
	payload := map[string]any{"message": map[string]any{"header": map[string]any{"toid": request.GroupID, "totype": "GROUP", "msgtype": strings.ToUpper(request.Type), "clientmsgid": request.ClientMessageID, "role": "robot"}, "body": request.Body}}
	if request.Reply != nil {
		payload["message"].(map[string]any)["reply"] = request.Reply
	}
	return s.sendGroupPayload(ctx, payload)
}

func validGroupMessageType(messageType string) bool {
	switch strings.ToUpper(messageType) {
	case "TEXT", "IMAGE", "MD":
		return true
	default:
		return false
	}
}
func validGroupBody(b GroupBody) bool {
	switch b.Type {
	case "TEXT":
		return strings.TrimSpace(b.Content) != "" && len([]rune(b.Content)) <= 2000
	case "IMAGE":
		return strings.TrimSpace(b.Content) != ""
	case "MD":
		return strings.TrimSpace(b.Content) != "" && len([]rune(b.Content)) <= 2048
	case "LINK":
		return strings.TrimSpace(b.Href) != ""
	case "AT":
		return (b.AtAll || len(b.AtUserIDs) > 0 || len(b.AtAgentIDs) > 0) && len(b.AtUserIDs) <= 50 && len(b.AtAgentIDs) <= 10
	case "CMD":
		return strings.TrimSpace(b.CommandName) != "" && b.TargetAgentID > 0
	default:
		return false
	}
}
func (s *MessageService) sendGroupPayload(ctx context.Context, payload any) (SendToGroupResult, error) {
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return SendToGroupResult{}, err
	}
	raw, err := s.transport.PostJSON(ctx, "/robot/msg/groupmsgsend", payload, token)
	if err != nil {
		return SendToGroupResult{}, err
	}
	env, err := envelope.DecodeBusiness(raw)
	if err != nil {
		return SendToGroupResult{}, err
	}
	var r groupTextResponse
	if err := json.Unmarshal(env.Data, &r); err != nil || r.Data == nil || r.Data.MessageID == nil || r.Data.MessageSequenceID == nil || r.Data.CreatedAtMillis == nil {
		return SendToGroupResult{}, sdkerrors.NewMalformed("group message result is incomplete", raw, err)
	}
	return SendToGroupResult{MessageID: *r.Data.MessageID, MessageSequenceID: *r.Data.MessageSequenceID, CreatedAtMillis: *r.Data.CreatedAtMillis, raw: append(json.RawMessage(nil), raw...)}, nil
}

// GroupMarkdownMention describes the @ targets of a Markdown group message.
// AtAll short-circuits: when true, the user and robot lists are ignored.
type GroupMarkdownMention struct {
	AtAll      bool
	AtUserIDs  []string
	AtAgentIDs []int64
}

// BuildGroupMarkdownMentionBody 对 MD 群消息做 @ 匹配注入，返回匹配后的 content 与 body
// （AT 块在前、MD 块在后）。@ 目标以字面量包含判断：已在 Markdown 中出现的不重复追加，
// 缺失的目标按用户、机器人顺序补到内容末尾。名单不去重、保序。atall=true 时短路返回，
// 用户/机器人名单被忽略；此时 MD 内容需含字面 "@all" 标记，后端才在标记位置内联渲染
// "@全体成员"（与 @用户/@机器人 的匹配注入约定一致），内容未含 "@all" 时补到末尾。
func BuildGroupMarkdownMentionBody(markdown string, m GroupMarkdownMention) (string, []GroupBody) {
	if m.AtAll {
		content := markdown
		if !strings.Contains(markdown, "@all") {
			sep := ""
			if !strings.HasSuffix(markdown, " ") && !strings.HasSuffix(markdown, "\n") {
				sep = " "
			}
			content = markdown + sep + "@all"
		}
		return content, []GroupBody{
			{Type: "AT", AtAll: true},
			{Type: "MD", Content: content},
		}
	}
	appended := make([]string, 0, len(m.AtUserIDs)+len(m.AtAgentIDs))
	for _, u := range m.AtUserIDs {
		if !strings.Contains(markdown, "@"+u) {
			appended = append(appended, "@"+u)
		}
	}
	for _, b := range m.AtAgentIDs {
		if !strings.Contains(markdown, "@"+strconv.FormatInt(b, 10)) {
			appended = append(appended, "@"+strconv.FormatInt(b, 10))
		}
	}
	content := markdown
	if len(appended) > 0 {
		sep := ""
		if !strings.HasSuffix(markdown, " ") && !strings.HasSuffix(markdown, "\n") {
			sep = " "
		}
		content = markdown + sep + strings.Join(appended, " ")
	}
	body := make([]GroupBody, 0, 3)
	if len(m.AtUserIDs) > 0 {
		body = append(body, GroupBody{Type: "AT", AtUserIDs: m.AtUserIDs})
	}
	if len(m.AtAgentIDs) > 0 {
		body = append(body, GroupBody{Type: "AT", AtAgentIDs: m.AtAgentIDs})
	}
	body = append(body, GroupBody{Type: "MD", Content: content})
	return content, body
}

// SendGroupMarkdownMentionRequest is the input of SendGroupMarkdownWithMentions.
type SendGroupMarkdownMentionRequest struct {
	GroupID         int64
	Markdown        string
	Mentions        GroupMarkdownMention
	SelfAgentID     int64 // 0 = 未提供
	Reply           *GroupReply
	ClientMessageID int64
}

// SendGroupMarkdownWithMentions 对 MD 群消息做 @ 匹配注入后发送到群。参数校验：
// Markdown 非空；atall=false 时 AT 名单不超限（≤50 用户、≤10 机器人）且
// SelfAgentID 命中 AtAgentIDs 时拒绝（平台禁止 @ 自身）。atall=true 时名单被短路忽略，
// 不校验其数量或是否命中自身。组装后的 body 交由 SendGroupMessage 的现有校验与发送链路。
func (s *MessageService) SendGroupMarkdownWithMentions(ctx context.Context, request SendGroupMarkdownMentionRequest) (SendToGroupResult, error) {
	if ctx == nil || request.GroupID <= 0 || strings.TrimSpace(request.Markdown) == "" {
		return SendToGroupResult{}, sdkerrors.NewConfig("group markdown", "context, groupID, and markdown are required")
	}
	if !request.Mentions.AtAll {
		if len(request.Mentions.AtUserIDs) > 50 {
			return SendToGroupResult{}, sdkerrors.NewConfig("atUserIDs", "must contain at most 50 entries")
		}
		if len(request.Mentions.AtAgentIDs) > 10 {
			return SendToGroupResult{}, sdkerrors.NewConfig("atAgentIDs", "must contain at most 10 entries")
		}
		if request.SelfAgentID != 0 && containsAgentID(request.Mentions.AtAgentIDs, request.SelfAgentID) {
			return SendToGroupResult{}, sdkerrors.NewConfig("self agent ID", "must not mention the sending agent")
		}
	}
	_, body := BuildGroupMarkdownMentionBody(request.Markdown, request.Mentions)
	return s.SendGroupMessage(ctx, SendGroupMessageRequest{
		GroupID:         request.GroupID,
		Type:            "MD",
		Body:            body,
		Reply:           request.Reply,
		ClientMessageID: request.ClientMessageID,
	})
}

func containsAgentID(ids []int64, target int64) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

type ListGroupMessagesRequest struct {
	ReceiverID, StartTime, EndTime int64
	SortType                       string
	PageSize                       int
	PageToken                      string
}
type MessageItem struct {
	MessageType string          `json:"msgType"`
	SubType     string          `json:"subType"`
	MessageID   string          `json:"msgId"`
	Sender      json.RawMessage `json:"sender"`
	SendTime    int64           `json:"sendTime"`
	DeletedFlag int             `json:"deletedFlag"`
	Content     json.RawMessage `json:"content"`
	Ext         json.RawMessage `json:"ext"`
}

// WorkCardContent is the normalized first-class `app_msg` / `work_card` message
// content. The message read APIs only return the card ID: the card body
// (title, description, buttons) is created through the streaming-card / bubble
// workflow-card routes and is not available through the read interface.
type WorkCardContent struct {
	Type   string `json:"type"`
	CardID string `json:"cardId"`
}

// WorkCard returns the normalized work_card content and whether the message is
// recognized as a work_card. A work_card whose cardId is missing, null, or
// non-string is still recognized with an empty CardID so it renders as
// "[工作卡片]" instead of falling back to an unknown shape.
func (m MessageItem) WorkCard() (WorkCardContent, bool) {
	var c struct {
		Type   string          `json:"type"`
		CardID json.RawMessage `json:"cardId"`
	}
	if err := json.Unmarshal(m.Content, &c); err != nil {
		return WorkCardContent{}, false
	}
	if c.Type != "work_card" {
		return WorkCardContent{}, false
	}
	cardID := ""
	if len(c.CardID) > 0 {
		var s string
		if err := json.Unmarshal(c.CardID, &s); err == nil {
			cardID = s
		}
	}
	return WorkCardContent{Type: c.Type, CardID: cardID}, true
}

// WorkCardText renders the work_card placeholder text:
// "[工作卡片 cardId=<cardId>]", or "[工作卡片]" when the cardId is missing or
// invalid. It returns "" when the message is not a recognized work_card.
func (m MessageItem) WorkCardText() string {
	content, ok := m.WorkCard()
	if !ok {
		return ""
	}
	if content.CardID == "" {
		return "[工作卡片]"
	}
	return "[工作卡片 cardId=" + content.CardID + "]"
}

// workCardTimeLayout renders the message send time in Beijing time
// (Asia/Shanghai, UTC+8) as "2006-01-02 15:04:05", matching the
// Owner-confirmed transcript line format.
const workCardTimeLayout = "2006-01-02 15:04:05"

// beijingTimeZone is a fixed UTC+8 zone so rendering is deterministic and does
// not depend on the host's tzdata.
var beijingTimeZone = time.FixedZone("Asia/Shanghai", 8*60*60)

// workCardTime renders the message send time in Beijing time.
func (m MessageItem) workCardTime() string {
	return time.UnixMilli(m.SendTime).In(beijingTimeZone).Format(workCardTimeLayout)
}

// messageSenderLabel renders the transcript speaker label: a human sender uses
// its uuapId, a robot sender (agentId without uuapId) uses "bot#<agentId>", and
// anything else falls back to "user#<userId>".
func (m MessageItem) messageSenderLabel() string {
	var s struct {
		UUAPID  string `json:"uuapId"`
		AgentID int64  `json:"agentId"`
		UserID  int64  `json:"userId"`
	}
	// A malformed sender falls back to "user#0" instead of failing the render.
	_ = json.Unmarshal(m.Sender, &s)
	if s.UUAPID != "" {
		return s.UUAPID
	}
	if s.AgentID != 0 {
		return "bot#" + strconv.FormatInt(s.AgentID, 10)
	}
	return "user#" + strconv.FormatInt(s.UserID, 10)
}

// WorkCardTextLine renders the full transcript line for a work_card message in
// the Owner-confirmed format:
//
//	[<time>] [msgId:<msgId>] bot#<agentId>: [工作卡片 cardId=<cardId>]
//
// It returns "" when the message is not a recognized work_card.
func (m MessageItem) WorkCardTextLine() string {
	if _, ok := m.WorkCard(); !ok {
		return ""
	}
	return "[" + m.workCardTime() + "] [msgId:" + m.MessageID + "] " + m.messageSenderLabel() + ": " + m.WorkCardText()
}

type ListGroupMessagesResult struct {
	HasMore   bool
	PageToken string
	Items     []MessageItem
	raw       json.RawMessage
}

func (r ListGroupMessagesResult) RawJSON() json.RawMessage {
	return append(json.RawMessage(nil), r.raw...)
}
func (s *MessageService) ListGroupMessages(ctx context.Context, r ListGroupMessagesRequest) (ListGroupMessagesResult, error) {
	if ctx == nil || r.ReceiverID <= 0 || r.StartTime <= 0 || r.EndTime <= r.StartTime || r.EndTime-r.StartTime > 86400000 || r.PageSize < 0 || r.PageSize > 50 {
		return ListGroupMessagesResult{}, sdkerrors.NewConfig("list request", "invalid receiver, time range, or page size")
	}
	if r.PageSize == 0 {
		r.PageSize = 20
	}
	if r.SortType == "" {
		r.SortType = "desc"
	}
	if r.SortType != "asc" && r.SortType != "desc" {
		return ListGroupMessagesResult{}, sdkerrors.NewConfig("sortType", "must be asc or desc")
	}
	q := url.Values{"receiverId": {strconv.FormatInt(r.ReceiverID, 10)}, "startTime": {strconv.FormatInt(r.StartTime, 10)}, "endTime": {strconv.FormatInt(r.EndTime, 10)}, "sortType": {r.SortType}, "pageSize": {strconv.Itoa(r.PageSize)}}
	if r.PageToken != "" {
		q.Set("pageToken", r.PageToken)
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return ListGroupMessagesResult{}, err
	}
	raw, err := s.transport.Get(ctx, "/im/message/list", q, token)
	if err != nil {
		return ListGroupMessagesResult{}, err
	}
	var out struct {
		Code string `json:"code"`
		Data struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				HasMore   bool          `json:"hasMore"`
				PageToken string        `json:"pageToken"`
				Items     []MessageItem `json:"items"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return ListGroupMessagesResult{}, sdkerrors.NewMalformed("message list result is invalid", raw, err)
	}
	if out.Code != "ok" || out.Data.Code != 200 {
		return ListGroupMessagesResult{}, sdkerrors.NewBusiness(fmt.Sprint(out.Data.Code), out.Data.Message, raw)
	}
	if out.Data.Data.HasMore && (out.Data.Data.PageToken == "" || out.Data.Data.PageToken == r.PageToken) {
		return ListGroupMessagesResult{}, sdkerrors.NewMalformed("message list returned a non-progressing page token", raw, nil)
	}
	return ListGroupMessagesResult{out.Data.Data.HasMore, out.Data.Data.PageToken, out.Data.Data.Items, append(json.RawMessage(nil), raw...)}, nil
}

type GetMessageRequest struct {
	FromID                                       string
	ReceiverID                                   int64
	ReceiverType                                 int
	MessageID                                    int64
	InnerMessageID, InnerFromID, InnerReceiverID int64
	InnerReceiverType                            int
}

func (s *MessageService) GetMessage(ctx context.Context, r GetMessageRequest) ([]MessageItem, error) {
	if ctx == nil || strings.TrimSpace(r.FromID) == "" || r.ReceiverID <= 0 || r.MessageID <= 0 || (r.ReceiverType != 2 && r.ReceiverType != 7) {
		return nil, sdkerrors.NewConfig("message request", "invalid required message identifiers")
	}
	inner := r.InnerMessageID != 0 || r.InnerFromID != 0 || r.InnerReceiverID != 0 || r.InnerReceiverType != 0
	if inner && (r.InnerMessageID <= 0 || r.InnerFromID <= 0 || r.InnerReceiverID <= 0 || r.InnerReceiverType <= 0) {
		return nil, sdkerrors.NewConfig("inner message", "all four inner message parameters are required")
	}
	q := url.Values{"fromId": {r.FromID}, "receiverId": {strconv.FormatInt(r.ReceiverID, 10)}, "receiverType": {strconv.Itoa(r.ReceiverType)}, "msgId": {strconv.FormatInt(r.MessageID, 10)}}
	if inner {
		q.Set("innerMsgId", strconv.FormatInt(r.InnerMessageID, 10))
		q.Set("innerMsgFromId", strconv.FormatInt(r.InnerFromID, 10))
		q.Set("innerMsgReceiverId", strconv.FormatInt(r.InnerReceiverID, 10))
		q.Set("innerMsgReceiverType", strconv.Itoa(r.InnerReceiverType))
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := s.transport.Get(ctx, "/im/message/getSingleMsg", q, token)
	if err != nil {
		return nil, err
	}
	var o struct {
		Code string `json:"code"`
		Data struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Items []MessageItem `json:"items"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, sdkerrors.NewMalformed("message result is invalid", raw, err)
	}
	if o.Code != "ok" || o.Data.Code != 200 {
		return nil, sdkerrors.NewBusiness(fmt.Sprint(o.Data.Code), o.Data.Message, raw)
	}
	return o.Data.Data.Items, nil
}

type RevokeUserMessageRequest struct {
	MessageKey string `json:"msgkey"`
	AgentID    int64  `json:"agentid"`
}
type RevokeGroupMessageRequest struct {
	GroupID           int64 `json:"groupId"`
	MessageID         int64 `json:"messageid"`
	MessageSequenceID int64 `json:"msgseqid"`
}
type PinGroupMessageRequest struct {
	GroupID   int64 `json:"groupId"`
	Operation int   `json:"opType"`
	MessageID int64 `json:"msgId"`
}
type EmojiRequest struct {
	FromUID          string `json:"fromUid"`
	ChatType         int    `json:"chatType"`
	ChatID           int64  `json:"chatId,omitempty"`
	BaseMessageID    string `json:"baseMsgId"`
	MessageID2       string `json:"msgId2,omitempty"`
	ReplyContent     string `json:"replyContent,omitempty"`
	ReplyDescription string `json:"replyDesc,omitempty"`
}

func (s *MessageService) RevokeUserMessage(ctx context.Context, r RevokeUserMessageRequest) error {
	if strings.TrimSpace(r.MessageKey) == "" || r.AgentID <= 0 {
		return sdkerrors.NewConfig("revoke", "message key and agent ID are required")
	}
	return s.postFlat(ctx, "/app/message/revoke", r)
}
func (s *MessageService) RevokeGroupMessage(ctx context.Context, r RevokeGroupMessageRequest) error {
	if r.GroupID <= 0 || r.MessageID <= 0 || r.MessageSequenceID <= 0 {
		return sdkerrors.NewConfig("revoke", "group ID, message ID, and sequence ID are required")
	}
	return s.postFlat(ctx, "/robot/group/msgRecall", r)
}
func (s *MessageService) SetGroupMessagePin(ctx context.Context, r PinGroupMessageRequest) error {
	if r.GroupID <= 0 || r.MessageID <= 0 || (r.Operation != 1 && r.Operation != 2) {
		return sdkerrors.NewConfig("pin", "group ID, message ID, and operation are required")
	}
	return s.postFlat(ctx, "/robot/group/stickOnTop", r)
}
func (s *MessageService) AddEmoji(ctx context.Context, r EmojiRequest) error {
	return s.emoji(ctx, "/im/message/emoji/add", r, true)
}
func (s *MessageService) DeleteEmoji(ctx context.Context, r EmojiRequest) error {
	return s.emoji(ctx, "/im/message/emoji/del", r, false)
}
func (s *MessageService) emoji(ctx context.Context, path string, r EmojiRequest, add bool) error {
	if strings.TrimSpace(r.FromUID) == "" || strings.TrimSpace(r.BaseMessageID) == "" || (r.ChatType != 2 && r.ChatType != 7) || (add && (strings.TrimSpace(r.MessageID2) == "" || strings.TrimSpace(r.ReplyContent) == "")) {
		return sdkerrors.NewConfig("emoji", "missing required emoji fields")
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return err
	}
	raw, err := s.transport.PostJSON(ctx, path, r, token)
	if err != nil {
		return err
	}
	var o struct {
		Code string `json:"code"`
		Data struct {
			BizCode    int    `json:"bizCode"`
			BizMessage string `json:"bizMsg"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return sdkerrors.NewMalformed("emoji result is invalid", raw, err)
	}
	if o.Code != "ok" || o.Data.BizCode != 200 {
		return sdkerrors.NewBusiness(fmt.Sprint(o.Data.BizCode), o.Data.BizMessage, raw)
	}
	return nil
}
func (s *MessageService) postFlat(ctx context.Context, path string, p any) error {
	if ctx == nil {
		return sdkerrors.NewConfig("context", "must not be nil")
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return err
	}
	raw, err := s.transport.PostJSON(ctx, path, p, token)
	if err != nil {
		return err
	}
	// The message-management routes use the standard nested B envelope
	// ({code:"ok", data:{errcode:0}}). Accept the nested B form in addition to
	// the historical flat A form ({errcode:0}) so that a server-side success is
	// not misreported as a business failure and a nested business failure is not
	// silently accepted. This mirrors the group-side successWithNestedErrcode.
	return successWithNestedErrcode(raw, 0)
}

type VoiceTranscriptionResult struct {
	Code    int
	Content string
	raw     json.RawMessage
}

func (r VoiceTranscriptionResult) RawJSON() json.RawMessage {
	return append(json.RawMessage(nil), r.raw...)
}
func (s *MessageService) TranscribeVoiceMessage(ctx context.Context, md5 string) (VoiceTranscriptionResult, error) {
	md5 = strings.TrimSpace(md5)
	if ctx == nil || md5 == "" {
		return VoiceTranscriptionResult{}, sdkerrors.NewConfig("md5", "must not be blank")
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return VoiceTranscriptionResult{}, err
	}
	raw, err := s.transport.PostForm(ctx, "/va/queryASRResult", url.Values{"md5": {md5}}, token)
	if err != nil {
		return VoiceTranscriptionResult{}, err
	}
	var o struct {
		Code string `json:"code"`
		Data struct {
			Code         int    `json:"code"`
			Content      string `json:"content"`
			Message      string `json:"message"`
			Msg          string `json:"msg"`
			ErrorMessage string `json:"errmsg"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return VoiceTranscriptionResult{}, sdkerrors.NewMalformed("voice transcription result is invalid", raw, err)
	}
	if o.Code != "ok" || o.Data.Code != 200 {
		m := o.Data.Message
		if m == "" {
			m = o.Data.Msg
		}
		if m == "" {
			m = o.Data.ErrorMessage
		}
		return VoiceTranscriptionResult{}, sdkerrors.NewBusiness(strconv.Itoa(o.Data.Code), m, raw)
	}
	return VoiceTranscriptionResult{o.Data.Code, o.Data.Content, append(json.RawMessage(nil), raw...)}, nil
}

type InteractionAppContext struct {
	EventID string `json:"eventId,omitempty"`
}
type InteractionHeader struct {
	ToType        string `json:"totype"`
	ToID          int64  `json:"toid"`
	OfflineNotify string `json:"offlinenotify,omitempty"`
}

// InteractionBody is the documented, non-panel portion of a group interaction
// message. Panel event objects are intentionally raw because their nested
// callback/JS schemas are not formally specified.
type InteractionBody struct {
	Type      string   `json:"type"`
	Content   string   `json:"content,omitempty"`
	Label     string   `json:"label,omitempty"`
	Href      string   `json:"href,omitempty"`
	AtAll     bool     `json:"atall,omitempty"`
	AtUserIDs []string `json:"atuserids,omitempty"`
}
type InteractionMessageRequest struct {
	AppContext *InteractionAppContext `json:"appContext,omitempty"`
	Header     InteractionHeader      `json:"header"`
	Body       []InteractionBody      `json:"body"`
	Panel      json.RawMessage        `json:"panel,omitempty"`
}

func (s *MessageService) SendInteractionMessage(ctx context.Context, r InteractionMessageRequest) error {
	if ctx == nil || r.Header.ToType != "group" || r.Header.ToID <= 0 || len(r.Body) == 0 || (len(r.Panel) > 0 && !json.Valid(r.Panel)) {
		return sdkerrors.NewConfig("interaction message", "valid group header, non-empty body, and optional panel JSON are required")
	}
	images := 0
	for _, body := range r.Body {
		if !validInteractionBody(body) {
			return sdkerrors.NewConfig("interaction body", "contains an invalid body element")
		}
		if body.Type == "IMAGE" {
			images++
		}
	}
	if images > 5 {
		return sdkerrors.NewConfig("interaction body", "contains more than five images")
	}
	return s.postFlat(ctx, "/robot/group/sendInteractionMsg", r)
}

func validInteractionBody(body InteractionBody) bool {
	switch body.Type {
	case "TEXT":
		return strings.TrimSpace(body.Content) != "" && len([]rune(body.Content)) <= 1000
	case "IMAGE", "MD":
		return strings.TrimSpace(body.Content) != ""
	case "LINK":
		return strings.TrimSpace(body.Href) != ""
	case "AT":
		return (body.AtAll || len(body.AtUserIDs) > 0) && len(body.AtUserIDs) <= 5
	default:
		return false
	}
}

// Streaming cards are deliberately raw: their schemas and success envelopes
// are observed but not formally specified by the platform surface.
func (s *MessageService) CreateStreamingCard(ctx context.Context, p json.RawMessage) (json.RawMessage, error) {
	return s.streaming(ctx, "/msg/sender/interactivity_msg", p)
}
func (s *MessageService) UpdateUserStreamingCard(ctx context.Context, p json.RawMessage) (json.RawMessage, error) {
	return s.streaming(ctx, "/msg/modifier/interactivity_personal_msg_content", p)
}
func (s *MessageService) UpdateGroupStreamingCard(ctx context.Context, p json.RawMessage) (json.RawMessage, error) {
	return s.streaming(ctx, "/msg/modifier/dynamic_content", p)
}
func (s *MessageService) streaming(ctx context.Context, path string, p json.RawMessage) (json.RawMessage, error) {
	if ctx == nil || !json.Valid(p) {
		return nil, sdkerrors.NewConfig("streaming payload", "must be valid JSON")
	}
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(p, &v); err != nil {
		return nil, sdkerrors.NewConfig("streaming payload", "must be valid JSON")
	}
	headers := http.Header{
		"LOGID": []string{strconv.FormatInt(s.messageIDs.Next(), 10)},
	}
	raw, err := s.transport.PostJSONWithHeaders(ctx, path, v, token, headers)
	if err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), raw...), nil
}
