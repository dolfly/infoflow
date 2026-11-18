package dynamiccard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	dcinternal "github.com/dolfly/infoflow/internal/dynamiccard"
)

// Fixed wire values of the send-unified-msg route.
const (
	// SendToTypeSingle marks a single-chat target (wire header.toType).
	SendToTypeSingle = "SINGLE"
	// SendToTypeGroup marks a group-chat target (wire header.toType).
	SendToTypeGroup = "GROUP"
	// SendMsgTypeInteractive is the fixed header.msgType value.
	SendMsgTypeInteractive = "INTERACTIVE"
	// SendRoleRobot is the fixed header.role value.
	SendRoleRobot = "robot"
	// SendSessionIDKey is the header.extra key that carries sessionId.
	SendSessionIDKey = "sessionId"
)

// SendRequest is one sendUnifiedMsg request. Exactly one of ToUser and GroupID
// must be non-blank; the SDK derives header.toType and header.toId from it.
// SessionID is passed through verbatim to header.extra.sessionId and is omitted
// when empty. At declares the top-level message.body AT node (see AtOptions) and
// Reply adds quoted-message metadata to header.extra.reply; both are only
// observed on this first send. Content must be scheme "1" (empty is normalized
// to "1"). Raw is an escape hatch: it only supplements fields the SDK did not
// already set, so a raw known field can never override the SDK-built value
// (typed At/Reply win over the same raw path).
type SendRequest struct {
	ToUser    string
	GroupID   string
	SessionID string
	At        *AtOptions
	Reply     *ReplyOptions
	Content   *DynamicCardContent
	Raw       map[string]json.RawMessage
}

// AtOptions is the @ declaration of the first sendUnifiedMsg, matching the
// platform's AI dynamic message guide v37 contract. By default it is written to
// the top-level message.body as one
// {"type":"AT","atall":<bool>,"atusers":[...]} node, which is a sibling of
// contents and not contents.body. The markdown text must also contain
// "@<uuapId>" for each mentioned user, otherwise the client renders no
// highlight and no notification; the SDK reports that as the advisory
// WarningAtMentionTextMissing and can inject it when AutoMentionText is true.
//
// AT can only be set on send. Update and the streaming enable/append/finish
// inputs expose no AT field.
type AtOptions struct {
	// All mentions every group member (@全体成员). It is independent of Users:
	// v37 §8.3 officially combines atall=true with a non-empty atusers list, so
	// both are emitted in the same node.
	All bool
	// Users lists the group member uuapIds to mention. Elements must be
	// non-blank. Every non-empty list is emitted regardless of All; with All
	// false and an empty list no AT node is emitted.
	Users []string
	// AutoMentionText prepends "@<uuapId>" for every missing user to the first
	// markdown component. When false (the default) a missing mention only
	// produces the WarningAtMentionTextMissing advisory.
	AutoMentionText bool
	// Transport selects the wire position and defaults to AtTransportBody.
	// AtTransportHeader is the legacy opt-in that writes header.extra.at
	// {atAll, atUserIds} instead of the documented message.body node.
	Transport string
	// AtAgentIDs is deprecated and retained only for source compatibility.
	//
	// Deprecated: the v37 document does not define group-robot @ for dynamic
	// messages, so this field never produces wire output. Pass such nodes
	// through SendRequest.Raw instead.
	AtAgentIDs []string
}

// AT transport positions accepted by AtOptions.Transport.
const (
	// AtTransportHeader is the legacy opt-in that writes the undocumented
	// message.header.extra.at {atAll, atUserIds} object and no message.body node.
	AtTransportHeader = "header"
	// AtTransportBody writes the documented top-level message.body AT node. It
	// is the default.
	AtTransportBody = "body"
)

// MaxAtAgentIDs is the former limit of AtOptions.AtAgentIDs.
//
// Deprecated: the v37 document does not define group-robot @ for dynamic
// messages and the SDK no longer emits atagentids.
const MaxAtAgentIDs = 10

// ReplyOptions is the quoted-message metadata of the first sendUnifiedMsg. It is
// carried in message.header.extra.reply and has no effect on later
// enable/append/finish/update calls. MessageID is required and is always sent
// as a JSON string, so a 19-digit id never loses precision; pass the decimal
// string form. Digest and SenderName are omitted from the wire when empty.
type ReplyOptions struct {
	// MessageID is the replied message id. Use the decimal string form for a
	// 19-digit id; FormatReplyMessageID converts a safe numeric id.
	MessageID string
	// Digest is the quoted text preview; empty omits the key.
	Digest string
	// SenderName is the quoted sender display name; empty omits the key.
	SenderName string
}

// MaxSafeReplyMessageID is the largest reply message id that stays exact as an
// IEEE-754 double (2^53-1). Larger ids must be passed to ReplyOptions.MessageID
// as decimal strings.
const MaxSafeReplyMessageID int64 = 1<<53 - 1

// FormatReplyMessageID converts a numeric reply message id into the decimal
// string form accepted by ReplyOptions.MessageID. Negative values and values
// above MaxSafeReplyMessageID are rejected locally with a validation
// *DynamicCardError, so a numeric id can never be sent with lost digits; pass a
// 19-digit id as a string instead.
func FormatReplyMessageID(value int64) (string, error) {
	if value < 0 {
		return "", newValidationError("reply.messageId", "numeric id must not be negative")
	}
	if value > MaxSafeReplyMessageID {
		return "", newValidationError("reply.messageId",
			"numeric id exceeds the safe integer range; pass the 19-digit decimal string form")
	}
	return strconv.FormatInt(value, 10), nil
}

// CardRef is the normalized result of a successful send. All platform
// identifiers are strings so a 19-digit msgKey never loses precision. Secret is
// sensitive and must never be logged. Warnings carries advisory findings such as
// WarningAtMentionTextMissing; it never blocks the send.
type CardRef struct {
	CardID    string
	Secret    string
	MsgKey    string
	MsgSeqID  string
	MessageID string
	CTime     string
	Warnings  []Warning
	Raw       json.RawMessage
}

// RawJSON returns the original send response, defensively copied.
func (r CardRef) RawJSON() json.RawMessage {
	return json.RawMessage(append([]byte(nil), r.Raw...))
}

// Send posts one dynamic message to a single user or a group and returns the
// normalized CardRef. Local P0/P1 validation failures return a
// *DynamicCardError with CategoryValidation without sending a request. A
// gateway or business failure is returned as the unified error; send is never
// retried automatically, including for 6001 and 9001.
func (s Service) Send(ctx context.Context, request SendRequest) (CardRef, error) {
	payload, warnings, err := buildSendPayload(request)
	if err != nil {
		return CardRef{}, err
	}
	response, err := s.post(ctx, dcinternal.RouteSend, payload, OperationSend)
	if err != nil {
		return CardRef{}, err
	}
	ref, err := parseCardRef(response)
	if err != nil {
		return CardRef{}, err
	}
	ref.Warnings = warnings
	return ref, nil
}

func buildSendPayload(request SendRequest) (map[string]json.RawMessage, []Warning, error) {
	hasUser := strings.TrimSpace(request.ToUser) != ""
	hasGroup := strings.TrimSpace(request.GroupID) != ""
	switch {
	case hasUser && hasGroup:
		return nil, nil, newValidationError("target", "toUser and groupId are mutually exclusive")
	case !hasUser && !hasGroup:
		return nil, nil, newValidationError("target", "one of toUser or groupId is required")
	}
	if request.Content == nil {
		return nil, nil, newValidationError("content", "must not be nil")
	}
	if request.Content.Scheme != "" && request.Content.Scheme != SchemeV1 {
		return nil, nil, newValidationError("scheme", `must be "1"`)
	}
	if len(request.Content.Body.Children) == 0 {
		return nil, nil, newValidationError("content.body.children", "must not be empty")
	}

	at, err := normalizeSendAt(request.At)
	if err != nil {
		return nil, nil, err
	}
	children := request.Content.Body.Children
	var mentionWarnings []Warning
	if at != nil && len(at.users) > 0 {
		children, mentionWarnings = applyAtMention(children, at)
	}
	contents := DynamicCardContent{Scheme: SchemeV1, Body: request.Content.Body, Footer: request.Content.Footer}
	contents.Body.Children = children
	contentWarnings, err := ValidateContent(&contents)
	if err != nil {
		return nil, nil, err
	}
	warnings := make([]Warning, 0, len(contentWarnings)+len(mentionWarnings))
	warnings = append(warnings, contentWarnings...)
	warnings = append(warnings, mentionWarnings...)

	extra, err := buildSendExtra(request, at)
	if err != nil {
		return nil, nil, err
	}
	toType := SendToTypeSingle
	toID := map[string]string{"toUser": request.ToUser}
	if hasGroup {
		toType = SendToTypeGroup
		toID = map[string]string{"groupId": request.GroupID}
	}
	header := map[string]any{
		"toType":  toType,
		"toId":    toID,
		"msgType": SendMsgTypeInteractive,
		"role":    SendRoleRobot,
	}
	if extra != nil {
		header["extra"] = extra
	}
	message := map[string]any{
		"header":   header,
		"contents": contents,
	}
	if at != nil && at.transport == AtTransportBody && at.hasBodyNode() {
		body, err := buildSendMessageBody(request.Raw, at.bodyNode())
		if err != nil {
			return nil, nil, err
		}
		message["body"] = body
	}
	known := map[string]any{"message": message}
	encoded, err := json.Marshal(known)
	if err != nil {
		return nil, nil, newValidationError("content", "cannot be encoded: "+err.Error())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, nil, newValidationError("content", "cannot be encoded: "+err.Error())
	}
	merged, err := mergeUnknownFields(fields, request.Raw)
	if err != nil {
		return nil, nil, err
	}
	return merged, warnings, nil
}

// buildSendExtra builds header.extra from sessionId, at and reply. It returns
// nil when none is set so the request body stays byte-identical to the
// pre-At/Reply version. Strong-typed at/reply are written into the known map,
// so the raw merge can only supplement them, never override them. The header AT
// form is written only when the transport includes header.
func buildSendExtra(request SendRequest, at *sendAt) (map[string]any, error) {
	reply, err := buildSendReply(request.Reply)
	if err != nil {
		return nil, err
	}
	extra := make(map[string]any, 3)
	if request.SessionID != "" {
		extra[SendSessionIDKey] = request.SessionID
	}
	if at != nil && at.transport == AtTransportHeader {
		extra["at"] = at.headerNode()
	}
	if reply != nil {
		extra["reply"] = reply
	}
	if len(extra) == 0 {
		return nil, nil
	}
	return extra, nil
}

// sendAt is the validated, normalized form of AtOptions.
type sendAt struct {
	all       bool
	users     []string
	autoText  bool
	transport string
}

// hasBodyNode reports whether at least one documented AT node is declared.
// all=false with an empty user list must not emit an empty AT shell.
func (a *sendAt) hasBodyNode() bool {
	return a != nil && (a.all || len(a.users) > 0)
}

// normalizeSendAt validates AtOptions and resolves the default transport. Go's
// typed fields make a non-boolean all or a non-string element unrepresentable;
// blank elements and an unknown transport are rejected locally. All and Users
// are independent and may be combined. The legacy "both" transport is rejected
// with a migration hint.
func normalizeSendAt(options *AtOptions) (*sendAt, error) {
	if options == nil {
		return nil, nil
	}
	transport := options.Transport
	if transport == "" {
		transport = AtTransportBody
	}
	switch transport {
	case AtTransportHeader, AtTransportBody:
	case "both":
		return nil, newValidationError("at.transport",
			`the legacy "both" value is no longer supported; use "body" or "header"`)
	default:
		return nil, newValidationError("at.transport", `must be "header" or "body"`)
	}
	users, err := normalizeAtIDs(options.Users, "at.users")
	if err != nil {
		return nil, err
	}
	return &sendAt{
		all:       options.All,
		users:     users,
		autoText:  options.AutoMentionText,
		transport: transport,
	}, nil
}

func normalizeAtIDs(values []string, field string) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, newValidationError(field, "elements must not be blank")
		}
		result = append(result, value)
	}
	return result, nil
}

// headerNode renders the legacy message.header.extra.at object; both fields are
// always present and atUserIds is never null.
func (a *sendAt) headerNode() map[string]any {
	users := a.users
	if users == nil {
		users = []string{}
	}
	return map[string]any{
		"atAll":     a.all,
		"atUserIds": users,
	}
}

// bodyNode renders the documented message.body {"type":"AT"} node. atall is
// always present and every non-empty member list is emitted, independently of
// atall: the v37 §8.3 example is
// {"type":"AT","atall":true,"atusers":["a","b"]}, so @all plus named members is
// a supported combination and must not be split across nodes.
func (a *sendAt) bodyNode() map[string]any {
	node := map[string]any{"type": "AT", "atall": a.all}
	if len(a.users) > 0 {
		node["atusers"] = a.users
	}
	return node
}

// applyAtMention checks that the markdown text contains "@<uuapId>" for every
// mentioned user. With AutoMentionText an explicit copy of the content tree is
// returned with the missing mentions prepended to the first markdown component;
// otherwise each missing user produces WarningAtMentionTextMissing. The caller's
// component tree is never mutated.
func applyAtMention(children ComponentList, at *sendAt) (ComponentList, []Warning) {
	markdown := findFirstMarkdown(children)
	var missing []string
	for _, user := range at.users {
		if markdown == nil || !strings.Contains(markdown.Content, "@"+user) {
			missing = append(missing, user)
		}
	}
	if len(missing) == 0 {
		return children, nil
	}
	if at.autoText && markdown != nil {
		prefix := "@" + strings.Join(missing, " @") + " "
		if updated, ok := prependMention(children, prefix); ok {
			return updated, nil
		}
	}
	warnings := make([]Warning, 0, len(missing))
	for _, user := range missing {
		warnings = append(warnings, Warning{
			Code:    WarningAtMentionTextMissing,
			Message: fmt.Sprintf(`markdown text does not contain "@%s"; the client will not highlight or notify`, user),
		})
	}
	return children, warnings
}

// findFirstMarkdown returns the first markdown component in document order,
// looking through row and column containers.
func findFirstMarkdown(children ComponentList) *MarkdownComponent {
	for _, component := range children {
		switch typed := component.(type) {
		case *MarkdownComponent:
			return typed
		case *RowComponent:
			if found := findFirstMarkdown(typed.Children); found != nil {
				return found
			}
		case *ColumnComponent:
			if found := findFirstMarkdown(typed.Children); found != nil {
				return found
			}
		}
	}
	return nil
}

// prependMention returns a copy of children whose first markdown component has
// prefix prepended to its content, copying only the container path it touches.
func prependMention(children ComponentList, prefix string) (ComponentList, bool) {
	result := make(ComponentList, len(children))
	for index, component := range children {
		switch typed := component.(type) {
		case *MarkdownComponent:
			copied := *typed
			copied.Content = prefix + copied.Content
			result[index] = &copied
			return result, true
		case *RowComponent:
			if nested, ok := prependMention(typed.Children, prefix); ok {
				copied := *typed
				copied.Children = nested
				result[index] = &copied
				return result, true
			}
		case *ColumnComponent:
			if nested, ok := prependMention(typed.Children, prefix); ok {
				copied := *typed
				copied.Children = nested
				result[index] = &copied
				return result, true
			}
		}
		result[index] = component
	}
	return result, false
}

// buildSendMessageBody renders message.body. An existing caller-provided array
// under raw.message.body is preserved and the SDK AT node is appended after it;
// any other raw.message.body shape is rejected when the SDK must write AT.
func buildSendMessageBody(raw map[string]json.RawMessage, node map[string]any) ([]json.RawMessage, error) {
	nodeRaw, err := json.Marshal(node)
	if err != nil {
		return nil, newValidationError("at", "cannot be encoded: "+err.Error())
	}
	items := make([]json.RawMessage, 0, 2)
	if messageRaw, exists := raw["message"]; exists {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(bytes.TrimSpace(messageRaw), &fields); err == nil && fields != nil {
			if bodyRaw, ok := fields["body"]; ok {
				trimmed := bytes.TrimSpace(bodyRaw)
				if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
					var existing []json.RawMessage
					if err := json.Unmarshal(trimmed, &existing); err != nil {
						return nil, newValidationError("raw.message.body",
							"must be an array when at transport includes body")
					}
					items = append(items, existing...)
				}
			}
		}
	}
	items = append(items, nodeRaw)
	return items, nil
}

// buildSendReply validates ReplyOptions and renders header.extra.reply.
// messageId is required and always a JSON string; digest and senderName are
// omitted when empty.
func buildSendReply(reply *ReplyOptions) (map[string]any, error) {
	if reply == nil {
		return nil, nil
	}
	messageID := strings.TrimSpace(reply.MessageID)
	if messageID == "" {
		return nil, newValidationError("reply.messageId", "must not be blank")
	}
	fields := map[string]any{"messageId": messageID}
	if reply.Digest != "" {
		fields["digest"] = reply.Digest
	}
	if reply.SenderName != "" {
		fields["senderName"] = reply.SenderName
	}
	return fields, nil
}

// mergeUnknownFields adds raw fields that the SDK did not set. Known fields win
// at every level; when both sides are objects they are merged recursively so
// unknown nested fields survive. Invalid raw JSON is rejected locally.
func mergeUnknownFields(known, raw map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	for name, value := range raw {
		trimmed := bytes.TrimSpace(value)
		if len(trimmed) == 0 {
			continue
		}
		if !json.Valid(trimmed) {
			return nil, newValidationError("raw."+name, "must be valid JSON")
		}
		existing, exists := known[name]
		if !exists {
			known[name] = append(json.RawMessage(nil), trimmed...)
			continue
		}
		if merged, ok := mergeJSONObjects(existing, trimmed, name); ok {
			known[name] = merged
		}
	}
	return known, nil
}

// protectedRawField reports whether raw may never merge into a known field
// subtree. header.toId is fully derived from the validated target, so a raw
// value cannot inject the other target type or a second target.
func protectedRawField(path []string) bool {
	return len(path) == 3 && path[0] == "message" && path[1] == "header" && path[2] == "toId"
}

// mergeJSONObjects merges two JSON objects, keeping every known key. It reports
// false when either side is not an object, in which case the known value wins.
func mergeJSONObjects(known, raw json.RawMessage, path ...string) (json.RawMessage, bool) {
	if protectedRawField(path) {
		return known, false
	}
	var knownFields map[string]json.RawMessage
	if err := json.Unmarshal(known, &knownFields); err != nil || knownFields == nil {
		return nil, false
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawFields); err != nil || rawFields == nil {
		return nil, false
	}
	for name, value := range rawFields {
		trimmed := bytes.TrimSpace(value)
		if len(trimmed) == 0 {
			continue
		}
		existing, exists := knownFields[name]
		if !exists {
			knownFields[name] = append(json.RawMessage(nil), trimmed...)
			continue
		}
		if merged, ok := mergeJSONObjects(existing, trimmed, append(path, name)...); ok {
			knownFields[name] = merged
		}
	}
	encoded, err := json.Marshal(knownFields)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

func parseCardRef(response CardResponse) (CardRef, error) {
	if len(response.Payload) == 0 {
		return CardRef{}, missingResponseField("business payload", OperationSend, response)
	}
	type partialData struct {
		MessageID   json.RawMessage `json:"messageid"`
		MsgID       json.RawMessage `json:"msgid"`
		MsgIDAlt    json.RawMessage `json:"msg_id"`
		MsgSeqID    json.RawMessage `json:"msgseqid"`
		MsgSeqIDAlt json.RawMessage `json:"msg_seq_id"`
		CTime       json.RawMessage `json:"ctime"`
		CTimeAlt    json.RawMessage `json:"c_time"`
	}
	var payload struct {
		Data struct {
			Partial *struct {
				MsgKey json.RawMessage `json:"msgkey"`
				Data   *partialData    `json:"data"`
			} `json:"partial"`
			Extra        map[string]json.RawMessage `json:"extra"`
			CardID       json.RawMessage            `json:"card_id"`
			MessageID    json.RawMessage            `json:"messageid"`
			MessageIDAlt json.RawMessage            `json:"message_id"`
			MsgSeqID     json.RawMessage            `json:"msgseqid"`
			MsgSeqIDAlt  json.RawMessage            `json:"msg_seq_id"`
			CTime        json.RawMessage            `json:"ctime"`
			CTimeAlt     json.RawMessage            `json:"c_time"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Payload, &payload); err != nil {
		return CardRef{}, missingResponseField("business data", OperationSend, response)
	}
	cardID := scalarJSONValue(payload.Data.Extra["card_id"])
	if cardID == "" {
		cardID = scalarJSONValue(payload.Data.CardID)
	}
	if cardID == "" {
		return CardRef{}, missingResponseField("card_id", OperationSend, response)
	}
	var msgKey json.RawMessage
	var partial partialData
	if payload.Data.Partial != nil {
		msgKey = payload.Data.Partial.MsgKey
		if payload.Data.Partial.Data != nil {
			partial = *payload.Data.Partial.Data
		}
	}
	// The real response carries the message id at data.partial.data.messageid.
	// Fall back through the top-level aliases and the partial msgid, and only
	// then to the (possibly imprecise) partial.msgkey.
	messageID := firstScalarJSONValue(
		partial.MessageID,
		payload.Data.MessageID,
		payload.Data.MessageIDAlt,
		partial.MsgID,
		partial.MsgIDAlt,
		msgKey,
	)
	msgSeqID := firstScalarJSONValue(
		partial.MsgSeqID,
		partial.MsgSeqIDAlt,
		payload.Data.MsgSeqID,
		payload.Data.MsgSeqIDAlt,
	)
	cTime := firstScalarJSONValue(
		partial.CTime,
		partial.CTimeAlt,
		payload.Data.CTime,
		payload.Data.CTimeAlt,
	)
	return CardRef{
		CardID:    cardID,
		Secret:    scalarJSONValue(payload.Data.Extra["secret"]),
		MsgKey:    scalarJSONValue(msgKey),
		MsgSeqID:  msgSeqID,
		MessageID: messageID,
		CTime:     cTime,
		Raw:       response.RawJSON(),
	}, nil
}

// firstScalarJSONValue returns the first value that renders to a non-empty
// string. null/absent values render to "" and are skipped, so a null never
// becomes the literal "null".
func firstScalarJSONValue(values ...json.RawMessage) string {
	for _, value := range values {
		if text := scalarJSONValue(value); text != "" {
			return text
		}
	}
	return ""
}

// scalarJSONValue renders a scalar JSON value as text without losing precision.
// Strings are returned as-is, numbers keep their literal digits, and null or
// absent values become "".
func scalarJSONValue(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return ""
		}
		return text
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return ""
	}
	return string(trimmed)
}
