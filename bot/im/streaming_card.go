package im

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/dolfly/infoflow/internal/envelope"
	"github.com/dolfly/infoflow/internal/sdkerrors"
)

// Streaming card session constants, aligned with the Node and Python SDKs.
const (
	streamingTemplateName          = "streaming_render"
	streamingTemplateVersion       = 30
	streamingExpireSeconds         = 7_776_000
	streamingGroupInitialVersion   = 111
	streamingMaxCardTextLength     = 6_000
	streamingMaxThinkingTextLength = 10_000
)

// StreamingCardTargetType is the conversation type that receives a streaming
// card session.
type StreamingCardTargetType string

const (
	StreamingCardTargetUser  StreamingCardTargetType = "user"
	StreamingCardTargetGroup StreamingCardTargetType = "group"
)

// StreamingCardTarget identifies the receiver of a streaming card session.
// For a user target the ID is the user login; for a group target the ID is
// the group ID.
type StreamingCardTarget struct {
	Type StreamingCardTargetType
	ID   string
}

// StreamingCardSessionOptions configures a new streaming card session.
type StreamingCardSessionOptions struct {
	// Target is where the card is delivered.
	Target StreamingCardTarget
	// UserIDs overrides the user list used by the user update route; it
	// defaults to the target ID for a user target.
	UserIDs []string
	// NotifyList overrides the group notify list used by the group update
	// route; it defaults to the target ID for a group target.
	NotifyList []map[string]any
	// AnswerFormat is "markdown" (default) or "text"; it selects the
	// ai_markdown or ai_text answer field.
	AnswerFormat string
	// InitialText seeds the answer area before the first flush.
	InitialText string
}

// StreamingCardSessionState is an immutable snapshot of a streaming card
// session.
type StreamingCardSessionState struct {
	Target        StreamingCardTarget
	ModifyToken   string
	Version       int
	Started       bool
	Done          bool
	Failed        bool
	UpdateFails   int
	AnswerText    string
	ReasoningText string
}

// StreamingCardSession wraps the raw streaming-card create/update routes with
// lifecycle semantics: create, start, append/update, complete, and an
// independent failure path. It mirrors the Node StreamingCardSession and the
// Python streaming_card session. A session must be started before it can be
// appended to or finalized.
type StreamingCardSession struct {
	service *MessageService

	target       StreamingCardTarget
	userIDs      []string
	notifyList   []map[string]any
	answerFormat string

	modifyToken  string
	groupVersion int
	started      bool
	done         bool
	failed       bool
	updateFails  int
	answerText   string
	reasoning    string
}

// NewStreamingCardSession creates a lifecycle-wrapped streaming card session
// for one receiver. It performs no network request; call Start to send the
// initial placeholder card.
func (s *MessageService) NewStreamingCardSession(options StreamingCardSessionOptions) (*StreamingCardSession, error) {
	target := options.Target
	if target.Type != StreamingCardTargetUser && target.Type != StreamingCardTargetGroup {
		return nil, sdkerrors.NewConfig("streaming card target.type", "must be user or group")
	}
	if strings.TrimSpace(target.ID) == "" {
		return nil, sdkerrors.NewConfig("streaming card target.id", "must not be blank")
	}
	answerFormat := strings.ToLower(strings.TrimSpace(options.AnswerFormat))
	if answerFormat == "" {
		answerFormat = "markdown"
	}
	if answerFormat != "markdown" && answerFormat != "text" {
		return nil, sdkerrors.NewConfig("streaming card answer format", "must be markdown or text")
	}
	userIDs := append([]string(nil), options.UserIDs...)
	if target.Type == StreamingCardTargetUser && len(userIDs) == 0 {
		userIDs = []string{target.ID}
	}
	notifyList := cloneNotifyList(options.NotifyList)
	if target.Type == StreamingCardTargetGroup && len(notifyList) == 0 {
		notifyList = []map[string]any{{"to_type": 2, "to_ids": []any{target.ID}}}
	}
	return &StreamingCardSession{
		service:      s,
		target:       target,
		userIDs:      userIDs,
		notifyList:   notifyList,
		answerFormat: answerFormat,
		groupVersion: streamingGroupInitialVersion,
		answerText:   options.InitialText,
	}, nil
}

// Start sends the initial placeholder card and obtains the modify token used
// by subsequent updates. Calling Start twice returns a configuration error.
func (sc *StreamingCardSession) Start(ctx context.Context) error {
	if ctx == nil {
		return sdkerrors.NewConfig("context", "must not be nil")
	}
	if sc.started {
		return sdkerrors.NewConfig("streaming card session", "start may only be called once")
	}
	if sc.done {
		return sdkerrors.NewConfig("streaming card session", "session is already complete or failed")
	}
	sc.started = true

	clientMsgID := sc.service.messageIDs.Next()
	payload := map[string]any{
		"contents": buildStreamingInitialContents(),
		"meta": map[string]any{
			"client_msg_id":        clientMsgID,
			"client_send_time":     clientMsgID,
			"interactivity_expire": streamingExpireSeconds,
			"interactivity_mode":   "normal",
			"modify_expire":        streamingExpireSeconds,
			"offline_notify_txt":   "你收到一条卡片消息",
			"template":             map[string]any{"name": streamingTemplateName, "version": streamingTemplateVersion},
		},
		"type":          1,
		"content_id":    strconv.FormatInt(clientMsgID, 10),
		"receiver_id":   sc.target.ID,
		"receiver_type": string(sc.target.Type),
		"scene":         "IM-server",
		"user_msg":      false,
	}
	raw, err := sc.service.CreateStreamingCard(ctx, mustJSON(payload))
	if err != nil {
		return err
	}
	token := extractStreamingModifyToken(raw)
	if token == "" {
		return sdkerrors.NewMalformed("streaming card create did not return a modify token", raw, nil)
	}
	sc.modifyToken = token
	return nil
}

// AppendText appends a delta to the answer area and flushes the card.
func (sc *StreamingCardSession) AppendText(ctx context.Context, delta string) error {
	if strings.TrimSpace(delta) == "" {
		return nil
	}
	sc.answerText += delta
	return sc.flush(ctx, "streaming", "")
}

// AppendReasoning appends a delta to the reasoning / thinking area and flushes
// the card.
func (sc *StreamingCardSession) AppendReasoning(ctx context.Context, delta string) error {
	if strings.TrimSpace(delta) == "" {
		return nil
	}
	sc.reasoning += delta
	return sc.flush(ctx, "streaming", "")
}

// NotifyToolStart records a tool-call start in the reasoning area and flushes
// the card.
func (sc *StreamingCardSession) NotifyToolStart(ctx context.Context, name, phase string) error {
	label := "**[工具调用]**"
	if strings.TrimSpace(name) != "" {
		label = "**[工具] " + name + "**"
	}
	if strings.TrimSpace(phase) != "" {
		label += " — " + phase
	}
	sc.reasoning += "\n" + label + "\n"
	return sc.flush(ctx, "streaming", "")
}

// NotifyToolResult records a tool result in the reasoning area and flushes the
// card.
func (sc *StreamingCardSession) NotifyToolResult(ctx context.Context, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	sc.reasoning += "\n" + text + "\n"
	return sc.flush(ctx, "streaming", "")
}

// Complete finalizes the card with a completion label and stops further
// updates. The default label is "思考完成".
func (sc *StreamingCardSession) Complete(ctx context.Context, doneLabel string) error {
	if strings.TrimSpace(doneLabel) == "" {
		doneLabel = "思考完成"
	}
	if err := sc.flush(ctx, "complete", doneLabel); err != nil {
		return err
	}
	sc.done = true
	return nil
}

// Fail finalizes the card with an error state and stops further updates. This
// is the independent failure path: the card shows the failure reason and the
// caller can fall back to a normal message.
func (sc *StreamingCardSession) Fail(ctx context.Context, message string) error {
	if strings.TrimSpace(message) == "" {
		message = "生成失败"
	}
	if err := sc.flush(ctx, "failed", message); err != nil {
		return err
	}
	sc.done = true
	sc.failed = true
	return nil
}

// State returns an immutable snapshot of the session.
func (sc *StreamingCardSession) State() StreamingCardSessionState {
	return StreamingCardSessionState{
		Target:        sc.target,
		ModifyToken:   sc.modifyToken,
		Version:       sc.version(),
		Started:       sc.started,
		Done:          sc.done,
		Failed:        sc.failed,
		UpdateFails:   sc.updateFails,
		AnswerText:    sc.answerText,
		ReasoningText: sc.reasoning,
	}
}

func (sc *StreamingCardSession) version() int {
	if sc.target.Type == StreamingCardTargetGroup {
		return sc.groupVersion - 1
	}
	return sc.groupVersion
}

func (sc *StreamingCardSession) flush(ctx context.Context, status, label string) error {
	if ctx == nil {
		return sdkerrors.NewConfig("context", "must not be nil")
	}
	if sc.done {
		return sdkerrors.NewConfig("streaming card session", "session is already complete or failed")
	}
	if !sc.started || sc.modifyToken == "" {
		return sdkerrors.NewConfig("streaming card session", "start must be called before updates")
	}
	contents := buildStreamingCardContents(sc.answerText, sc.reasoning, status, sc.answerFormat, label)
	raw, err := sc.update(ctx, contents)
	if err != nil {
		sc.updateFails++
		return err
	}
	env, err := envelope.Decode(raw)
	if err != nil {
		sc.updateFails++
		return err
	}
	if env.Code != "ok" {
		sc.updateFails++
		return sdkerrors.NewBusiness(env.Code, env.Message, raw)
	}
	sc.updateFails = 0
	return nil
}

func (sc *StreamingCardSession) update(ctx context.Context, contents map[string]any) ([]byte, error) {
	if sc.target.Type == StreamingCardTargetGroup {
		payload := map[string]any{
			"modify_token":            sc.modifyToken,
			"new_dynamic_msg_content": contents,
			"version":                 sc.groupVersion,
			"notify_list":             sc.notifyList,
		}
		sc.groupVersion++
		return sc.service.UpdateGroupStreamingCard(ctx, mustJSON(payload))
	}
	payload := map[string]any{
		"modify_token": sc.modifyToken,
		"new_personal_msg_content": []any{
			map[string]any{
				"user_ids":             sc.userIDs,
				"personal_msg_content": contents,
				"notify_msg_content":   nil,
			},
		},
	}
	return sc.service.UpdateUserStreamingCard(ctx, mustJSON(payload))
}

func buildStreamingInitialContents() map[string]any {
	return map[string]any{
		"card_init":                       streamingNode("1"),
		"answer_summary":                  streamingNode("收到啦，正在处理中～"),
		"ai_markdown":                     streamingNode(""),
		"status_info":                     streamingNode("收到啦，正在处理中～"),
		"thinking_aio":                    streamingNode(""),
		"think_star_img":                  streamingNode("ast/think_star_static.png"),
		"think_status_img":                streamingNode("ast/thinking_yes.png"),
		"think_status_color":              streamingNode("#5C6473"),
		"think_status_text":               streamingNode("收到啦，正在处理中～"),
		"think_arrow_img":                 streamingNode("ast/arrow_down.png"),
		"think_layout_install":            streamingNode("1"),
		"status_info_1_install":           streamingNode("0"),
		"flex_item_status_info_1_install": streamingNode("0"),
		"dc_print_end":                    streamingNode("1"),
	}
}

func buildStreamingCardContents(answerText, reasoningText, status, answerFormat, label string) map[string]any {
	answerField := "ai_markdown"
	if answerFormat == "text" {
		answerField = "ai_text"
	}
	contents := map[string]any{
		answerField:    streamingNode(truncateTail(answerText, streamingMaxCardTextLength)),
		"ai_markdown":  streamingNode(truncateTail(answerText, streamingMaxCardTextLength)),
		"thinking_aio": streamingNode(truncateTail(reasoningText, streamingMaxThinkingTextLength)),
	}
	switch status {
	case "failed":
		contents["status_info"] = streamingNode(label)
		contents["think_status_color"] = streamingNode("#D03050")
		contents["think_status_text"] = streamingNode("生成失败")
		contents["dc_print_end"] = streamingNode("1")
	case "complete":
		contents["status_info"] = streamingNode(label)
		contents["think_status_text"] = streamingNode(label)
		contents["dc_print_end"] = streamingNode("1")
	default:
		contents["status_info"] = streamingNode("思考中")
		contents["think_status_text"] = streamingNode("思考中")
		contents["dc_print_end"] = streamingNode("0")
	}
	return contents
}

func streamingNode(content string) map[string]string {
	return map[string]string{"type": "text", "content": content}
}

func truncateTail(text string, maxLength int) string {
	if len(text) <= maxLength {
		return text
	}
	return text[len(text)-maxLength:]
}

func extractStreamingModifyToken(raw []byte) string {
	env, err := envelope.Decode(raw)
	if err != nil || env.Code != "ok" {
		return ""
	}
	return streamingTokenIn(env.Data)
}

func streamingTokenIn(data json.RawMessage) string {
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil || value == nil {
		return ""
	}
	for _, key := range []string{"modify_token", "modifyToken", "modify_token_str"} {
		if token, ok := value[key].(string); ok && strings.TrimSpace(token) != "" {
			return token
		}
	}
	if receivers, ok := value["receivers"].([]any); ok {
		for _, receiver := range receivers {
			if item, ok := receiver.(map[string]any); ok {
				if token := firstStreamingToken(item); token != "" {
					return token
				}
			}
		}
	}
	if nested, ok := value["data"].(map[string]any); ok {
		return streamingTokenIn(mustJSON(nested))
	}
	return ""
}

func firstStreamingToken(item map[string]any) string {
	for _, key := range []string{"modify_token", "modifyToken", "modify_token_str"} {
		if token, ok := item[key].(string); ok && strings.TrimSpace(token) != "" {
			return token
		}
	}
	return ""
}

func cloneNotifyList(source []map[string]any) []map[string]any {
	if len(source) == 0 {
		return nil
	}
	cloned := make([]map[string]any, 0, len(source))
	for _, item := range source {
		cloned = append(cloned, cloneMap(item))
	}
	return cloned
}

func cloneMap(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return raw
}
