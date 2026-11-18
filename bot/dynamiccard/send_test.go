package dynamiccard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	dcinternal "github.com/dolfly/infoflow/internal/dynamiccard"
)

const sendSuccessResponse = `{
  "code": "ok",
  "data": {
    "code": 0,
    "errmsg": "success",
    "data": {
      "partial": {"msgkey": 1879549251012710400},
      "extra": {"card_id": "card_send_001", "secret": "s3cr3t"},
      "messageid": null,
      "msgseqid": null,
      "ctime": null
    }
  }
}`

type fakePostTransport struct {
	calls    int
	route    string
	payload  any
	response string
	status   int
	err      error
}

func (f *fakePostTransport) PostJSON(_ context.Context, route string, payload any) (dcinternal.Decision, error) {
	f.calls++
	f.route = route
	f.payload = payload
	if f.err != nil {
		return dcinternal.Decision{}, f.err
	}
	status := f.status
	if status == 0 {
		status = 200
	}
	return dcinternal.Decide([]byte(f.response)).WithHTTPStatus(status), nil
}

func newFakeService(response string) (Service, *fakePostTransport) {
	transport := &fakePostTransport{response: response}
	return Service{transport: transport}, transport
}

func decodePayload(t *testing.T, payload any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return decoded
}

func objectAt(t *testing.T, value any, path ...string) map[string]any {
	t.Helper()
	current, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value at %v is not an object: %T", path, value)
	}
	for _, key := range path {
		next, exists := current[key]
		if !exists {
			t.Fatalf("key %q missing at %v", key, path)
		}
		current, ok = next.(map[string]any)
		if !ok {
			t.Fatalf("value at %v is not an object: %T", path, next)
		}
	}
	return current
}

func TestSendSingleUserBuildsUnifiedMessage(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	ref, err := service.Send(context.Background(), SendRequest{
		ToUser:    "user_1001",
		SessionID: "6f1c2a9e-3b7d-4c8e-9a10-2d5f7b8c9e01",
		Content: NewContent(
			NewMarkdown("desc", "构建完成"),
			NewButton("ok", "确认"),
		),
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if transport.route != dcinternal.RouteSend {
		t.Fatalf("route = %q", transport.route)
	}
	if transport.calls != 1 {
		t.Fatalf("calls = %d", transport.calls)
	}
	payload := decodePayload(t, transport.payload)
	header := objectAt(t, payload, "message", "header")
	if header["toType"] != SendToTypeSingle {
		t.Fatalf("toType = %v", header["toType"])
	}
	toID := objectAt(t, header, "toId")
	if toID["toUser"] != "user_1001" {
		t.Fatalf("toUser = %v", toID["toUser"])
	}
	if _, exists := toID["groupId"]; exists {
		t.Fatalf("groupId must be absent: %v", toID)
	}
	if header["msgType"] != SendMsgTypeInteractive || header["role"] != SendRoleRobot {
		t.Fatalf("fixed header fields = %v", header)
	}
	extra := objectAt(t, header, "extra")
	if extra[SendSessionIDKey] != "6f1c2a9e-3b7d-4c8e-9a10-2d5f7b8c9e01" {
		t.Fatalf("sessionId = %v", extra[SendSessionIDKey])
	}
	contents := objectAt(t, payload, "message", "contents")
	if contents["scheme"] != "1" {
		t.Fatalf("scheme = %v", contents["scheme"])
	}
	children, ok := objectAt(t, contents, "body")["children"].([]any)
	if !ok || len(children) != 2 {
		t.Fatalf("children = %v", contents["body"])
	}
	if ref.CardID != "card_send_001" || ref.Secret != "s3cr3t" {
		t.Fatalf("card ref = %+v", ref)
	}
	if ref.MsgKey != "1879549251012710400" {
		t.Fatalf("msgKey = %q", ref.MsgKey)
	}
	if ref.MessageID != "1879549251012710400" {
		t.Fatalf("messageId must fall back to msgkey: %q", ref.MessageID)
	}
	if ref.MsgSeqID != "" || ref.CTime != "" {
		t.Fatalf("null platform ids must be omitted: %+v", ref)
	}
}

func TestSendGroupWithoutSessionOmitsExtra(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	if _, err := service.Send(context.Background(), SendRequest{
		GroupID: "group_2002",
		Content: NewContent(NewText("t1", "已通知群成员")),
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	payload := decodePayload(t, transport.payload)
	header := objectAt(t, payload, "message", "header")
	if header["toType"] != SendToTypeGroup {
		t.Fatalf("toType = %v", header["toType"])
	}
	toID := objectAt(t, header, "toId")
	if toID["groupId"] != "group_2002" {
		t.Fatalf("groupId = %v", toID["groupId"])
	}
	if _, exists := header["extra"]; exists {
		t.Fatalf("extra must be omitted without sessionId: %v", header)
	}
}

func TestSendRawCannotOverrideKnownFields(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	if _, err := service.Send(context.Background(), SendRequest{
		ToUser:    "user_1001",
		SessionID: "session-1",
		Content:   NewContent(NewMarkdown("desc", "x")),
		Raw: map[string]json.RawMessage{
			"future": json.RawMessage(`{"kept":true}`),
			"message": json.RawMessage(`{
			  "header": {
			    "msgType": "TEXT",
			    "role": "user",
			    "toType": "GROUP",
			    "toId": {"groupId": "group_evil"},
			    "extra": {"trace": "abc", "sessionId": "overridden"}
			  },
			  "contents": {"scheme": "2"},
			  "unknownTop": {"kept": 1}
			}`),
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	payload := decodePayload(t, transport.payload)
	if _, exists := payload["future"]; !exists {
		t.Fatalf("unknown raw root field was dropped: %v", payload)
	}
	message := objectAt(t, payload, "message")
	header := objectAt(t, message, "header")
	if header["msgType"] != SendMsgTypeInteractive || header["role"] != SendRoleRobot {
		t.Fatalf("raw overrode fixed header fields: %v", header)
	}
	if header["toType"] != SendToTypeSingle {
		t.Fatalf("raw overrode toType: %v", header["toType"])
	}
	toID := objectAt(t, header, "toId")
	if toID["toUser"] != "user_1001" {
		t.Fatalf("raw overrode toId: %v", toID)
	}
	if _, exists := toID["groupId"]; exists {
		t.Fatalf("raw injected groupId: %v", toID)
	}
	extra := objectAt(t, header, "extra")
	if extra[SendSessionIDKey] != "session-1" {
		t.Fatalf("raw overrode sessionId: %v", extra[SendSessionIDKey])
	}
	if extra["trace"] != "abc" {
		t.Fatalf("unknown raw header.extra field was dropped: %v", extra)
	}
	contents := objectAt(t, message, "contents")
	if contents["scheme"] != "1" {
		t.Fatalf("raw overrode scheme: %v", contents["scheme"])
	}
	if _, exists := message["unknownTop"]; !exists {
		t.Fatalf("unknown raw message field was dropped: %v", message)
	}
}

func TestSendLocalValidationDoesNotSend(t *testing.T) {
	validContent := NewContent(NewMarkdown("desc", "x"))
	cases := []struct {
		name    string
		request SendRequest
	}{
		{"both targets", SendRequest{ToUser: "u", GroupID: "g", Content: validContent}},
		{"no target", SendRequest{Content: validContent}},
		{"blank targets", SendRequest{ToUser: "   ", GroupID: "\t", Content: validContent}},
		{"nil content", SendRequest{ToUser: "u"}},
		{"empty children", SendRequest{ToUser: "u", Content: &DynamicCardContent{Scheme: "1"}}},
		{"bad scheme", SendRequest{ToUser: "u", Content: &DynamicCardContent{Scheme: "2", Body: validContent.Body}}},
		{"duplicate id", SendRequest{ToUser: "u", Content: NewContent(NewText("dup", "a"), NewText("dup", "b"))}},
		{"empty component type", SendRequest{ToUser: "u", Content: NewContent(NewRaw("", "id1", nil))}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, transport := newFakeService(sendSuccessResponse)
			if _, err := service.Send(context.Background(), testCase.request); err == nil {
				t.Fatal("expected validation error")
			} else {
				var failure *DynamicCardError
				if !errors.As(err, &failure) || !failure.IsValidation() {
					t.Fatalf("error = %v, want validation DynamicCardError", err)
				}
			}
			if transport.calls != 0 {
				t.Fatalf("validation sent %d requests", transport.calls)
			}
		})
	}
}

func TestSendOversizeContentRejectedLocally(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	if _, err := service.Send(context.Background(), SendRequest{
		ToUser:  "u",
		Content: NewContent(NewMarkdown("desc", string(make([]byte, MaxContentBytes)))),
	}); err == nil {
		t.Fatal("expected size validation error")
	}
	if transport.calls != 0 {
		t.Fatalf("oversize content sent %d requests", transport.calls)
	}
}

func TestSendBusinessAndGatewayErrors(t *testing.T) {
	cases := []struct {
		name     string
		response string
		check    func(*testing.T, *DynamicCardError)
	}{
		{
			name:     "business 1001",
			response: `{"code": 1001, "message": "content is invalid"}`,
			check: func(t *testing.T, failure *DynamicCardError) {
				if !failure.IsValidation() || failure.Retryable {
					t.Fatalf("classification = %+v", failure)
				}
				if failure.CodeString() != "1001" {
					t.Fatalf("code = %q", failure.CodeString())
				}
				if failure.Operation != OperationSend {
					t.Fatalf("operation = %q", failure.Operation)
				}
			},
		},
		{
			name:     "business 9001 not retried by sdk",
			response: `{"code": 9001, "message": "internal gateway error"}`,
			check: func(t *testing.T, failure *DynamicCardError) {
				if !failure.IsServer() {
					t.Fatalf("category = %q", failure.Category)
				}
			},
		},
		{
			name:     "gateway",
			response: `{"code": "request error", "msg": "gateway rejected"}`,
			check: func(t *testing.T, failure *DynamicCardError) {
				if !failure.IsGateway() {
					t.Fatalf("category = %q", failure.Category)
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, transport := newFakeService(testCase.response)
			_, err := service.Send(context.Background(), SendRequest{
				ToUser: "u", Content: NewContent(NewMarkdown("desc", "x")),
			})
			var failure *DynamicCardError
			if !errors.As(err, &failure) {
				t.Fatalf("error = %v, want DynamicCardError", err)
			}
			testCase.check(t, failure)
			if transport.calls != 1 {
				t.Fatalf("calls = %d, send must not retry", transport.calls)
			}
		})
	}
}

func TestSendMissingCardIDIsServerError(t *testing.T) {
	service, _ := newFakeService(`{"code":"ok","data":{"code":0,"data":{"partial":{"msgkey":1},"extra":{}}}}`)
	_, err := service.Send(context.Background(), SendRequest{
		ToUser: "u", Content: NewContent(NewMarkdown("desc", "x")),
	})
	var failure *DynamicCardError
	if !errors.As(err, &failure) || !failure.IsServer() {
		t.Fatalf("error = %v, want server DynamicCardError", err)
	}
	if len(failure.Raw) == 0 {
		t.Fatal("missing card_id error must retain the raw response")
	}
}

func stringList(t *testing.T, value any, path string) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%s is not an array: %T", path, value)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("%s contains a non-string value: %T", path, item)
		}
		result = append(result, text)
	}
	return result
}

// AT-01: users are declared in the top-level message.body AT node with the
// document's atusers field, and the default path writes no header.extra.at.
func TestSendGroupWithAtWritesBodyNode(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	if _, err := service.Send(context.Background(), SendRequest{
		GroupID: "group_2002",
		Content: NewContent(NewMarkdown("desc", "@wangyi71 @lisi 已通知")),
		At:      &AtOptions{Users: []string{"wangyi71", "lisi"}},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	payload := decodePayload(t, transport.payload)
	header := objectAt(t, payload, "message", "header")
	if _, exists := header["extra"]; exists {
		t.Fatalf("default transport must not write header.extra: %v", header)
	}
	nodes := bodyATNodes(t, payload)
	if len(nodes) != 1 {
		t.Fatalf("body AT nodes = %v", nodes)
	}
	node := nodes[0].(map[string]any)
	if node["type"] != "AT" || node["atall"] != false {
		t.Fatalf("body AT node = %v", node)
	}
	ids := stringList(t, node["atusers"], "atusers")
	if !reflect.DeepEqual(ids, []string{"wangyi71", "lisi"}) {
		t.Fatalf("atusers = %v", ids)
	}
	if len(node) != 3 {
		t.Fatalf("AT node must carry only type/atall/atusers: %v", node)
	}
}

// AT-02: the v37 default never emits the undocumented atuserids / atagentids
// names nor header.extra.at.
func TestSendAtUsesOnlyDocumentedFieldNames(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	if _, err := service.Send(context.Background(), SendRequest{
		GroupID: "group_2002",
		Content: NewContent(NewMarkdown("desc", "@wangyi71 hi")),
		At:      &AtOptions{Users: []string{"wangyi71"}, AtAgentIDs: []string{"1282"}},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	encoded, err := json.Marshal(transport.payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	for _, forbidden := range []string{"atuserids", "atagentids", "atUserIds", "atAgentIds"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("payload must not contain %q: %s", forbidden, encoded)
		}
	}
	payload := decodePayload(t, transport.payload)
	if _, exists := objectAt(t, payload, "message", "header")["extra"]; exists {
		t.Fatalf("header.extra must be absent: %v", payload)
	}
}

// AT-03: all=true with an empty user list writes only {type, atall}.
func TestSendAtAllBodyNodeOmitsUsers(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	ref, err := service.Send(context.Background(), SendRequest{
		GroupID: "group_2002",
		Content: NewContent(NewMarkdown("desc", "@all 开会了")),
		At:      &AtOptions{All: true},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	payload := decodePayload(t, transport.payload)
	nodes := bodyATNodes(t, payload)
	if len(nodes) != 1 {
		t.Fatalf("body AT nodes = %v", nodes)
	}
	node := nodes[0].(map[string]any)
	if len(node) != 2 || node["type"] != "AT" || node["atall"] != true {
		t.Fatalf("all body node = %v", node)
	}
	if len(ref.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", ref.Warnings)
	}
}

// AT-04: no at, or all=false with an empty user list, emits no message.body.
func TestSendAtEmptyUsersOmitsBodyNode(t *testing.T) {
	cases := map[string]*AtOptions{
		"nil at":      nil,
		"empty users": {Users: []string{}},
		"whitespace?": {All: false},
	}
	for name, at := range cases {
		t.Run(name, func(t *testing.T) {
			service, transport := newFakeService(sendSuccessResponse)
			if _, err := service.Send(context.Background(), SendRequest{
				GroupID: "group_2002",
				Content: NewContent(NewText("t1", "hi")),
				At:      at,
			}); err != nil {
				t.Fatalf("send: %v", err)
			}
			payload := decodePayload(t, transport.payload)
			if _, exists := objectAt(t, payload, "message")["body"]; exists {
				t.Fatalf("message.body must be absent: %v", payload)
			}
		})
	}
}

// AT-05: all=true and a non-empty user list are independent and are emitted in
// the same node, matching the v37 §8.3 example
// {"type":"AT","atall":true,"atusers":["a","b"]}.
func TestSendAtAllWithUsersEmitsBothFields(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	ref, err := service.Send(context.Background(), SendRequest{
		GroupID: "group_2002",
		Content: NewContent(NewMarkdown("desc", "@wangyi71 @lisi 开会了 @all")),
		At:      &AtOptions{All: true, Users: []string{"wangyi71", "lisi"}},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if transport.calls != 1 {
		t.Fatalf("calls = %d", transport.calls)
	}
	if len(ref.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", ref.Warnings)
	}
	payload := decodePayload(t, transport.payload)
	nodes := bodyATNodes(t, payload)
	if len(nodes) != 1 {
		t.Fatalf("body AT nodes = %v", nodes)
	}
	node := nodes[0].(map[string]any)
	if node["type"] != "AT" || node["atall"] != true {
		t.Fatalf("body AT node = %v", node)
	}
	ids := stringList(t, node["atusers"], "atusers")
	if !reflect.DeepEqual(ids, []string{"wangyi71", "lisi"}) {
		t.Fatalf("atusers = %v", ids)
	}
	if len(node) != 3 {
		t.Fatalf("AT node must carry only type/atall/atusers: %v", node)
	}
	header := objectAt(t, payload, "message", "header")
	if _, exists := header["extra"]; exists {
		t.Fatalf("default transport must not write header.extra: %v", header)
	}
}

// AT-06: a missing "@uuapId" is an advisory warning by default, and
// AutoMentionText injects it into the first markdown component.
func TestSendAtMentionTextWarningAndAutoFill(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	ref, err := service.Send(context.Background(), SendRequest{
		GroupID: "group_2002",
		Content: NewContent(NewMarkdown("desc", "处理完成")),
		At:      &AtOptions{Users: []string{"wangyi71"}},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(ref.Warnings) != 1 || ref.Warnings[0].Code != WarningAtMentionTextMissing {
		t.Fatalf("warnings = %v, want %s", ref.Warnings, WarningAtMentionTextMissing)
	}
	payload := decodePayload(t, transport.payload)
	children, ok := objectAt(t, payload, "message", "contents", "body")["children"].([]any)
	if !ok || len(children) != 1 {
		t.Fatalf("children = %v", children)
	}
	markdown := children[0].(map[string]any)
	if markdown["content"] != "处理完成" {
		t.Fatalf("content must not be mutated by default: %v", markdown["content"])
	}

	auto, autoTransport := newFakeService(sendSuccessResponse)
	autoRef, err := auto.Send(context.Background(), SendRequest{
		GroupID: "group_2002",
		Content: NewContent(NewMarkdown("desc", "处理完成")),
		At:      &AtOptions{Users: []string{"wangyi71"}, AutoMentionText: true},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(autoRef.Warnings) != 0 {
		t.Fatalf("auto mention must resolve the warning: %v", autoRef.Warnings)
	}
	autoPayload := decodePayload(t, autoTransport.payload)
	autoChildren := objectAt(t, autoPayload, "message", "contents", "body")["children"].([]any)
	if content := autoChildren[0].(map[string]any)["content"]; content != "@wangyi71 处理完成" {
		t.Fatalf("auto mention content = %v", content)
	}
}

// AT-07: transport=header is the legacy opt-in and does not affect the default
// body path.
func TestSendAtTransportHeaderLegacy(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	if _, err := service.Send(context.Background(), SendRequest{
		GroupID: "group_2002",
		Content: NewContent(NewMarkdown("desc", "@wangyi71 hi")),
		At:      &AtOptions{Users: []string{"wangyi71"}, Transport: AtTransportHeader},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	payload := decodePayload(t, transport.payload)
	at := objectAt(t, payload, "message", "header", "extra", "at")
	if at["atAll"] != false {
		t.Fatalf("atAll = %v", at["atAll"])
	}
	if ids := stringList(t, at["atUserIds"], "atUserIds"); !reflect.DeepEqual(ids, []string{"wangyi71"}) {
		t.Fatalf("atUserIds = %v", ids)
	}
	if _, exists := objectAt(t, payload, "message")["body"]; exists {
		t.Fatalf("transport=header must not write message.body: %v", payload)
	}
}

// The deprecated atAgentIds field never produces wire output, and the removed
// "both" transport is rejected with a migration error.
func TestSendAtLegacyInputs(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	if _, err := service.Send(context.Background(), SendRequest{
		GroupID: "group_2002",
		Content: NewContent(NewText("t1", "hi")),
		At:      &AtOptions{AtAgentIDs: []string{"1282"}},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	payload := decodePayload(t, transport.payload)
	if _, exists := objectAt(t, payload, "message")["body"]; exists {
		t.Fatalf("atAgentIds alone must emit no AT node: %v", payload)
	}

	both, bothTransport := newFakeService(sendSuccessResponse)
	_, err := both.Send(context.Background(), SendRequest{
		GroupID: "group_2002",
		Content: NewContent(NewMarkdown("desc", "x")),
		At:      &AtOptions{Users: []string{"wangyi71"}, Transport: "both"},
	})
	var failure *DynamicCardError
	if !errors.As(err, &failure) || !failure.IsValidation() {
		t.Fatalf("error = %v, want validation DynamicCardError", err)
	}
	if bothTransport.calls != 0 {
		t.Fatalf("validation sent %d requests", bothTransport.calls)
	}
}

func TestSendSingleWithReplyBuildsHeaderExtra(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	if _, err := service.Send(context.Background(), SendRequest{
		ToUser:  "user_1001",
		Content: NewContent(NewMarkdown("desc", "x")),
		Reply: &ReplyOptions{
			MessageID:  "1880207593545261334",
			Digest:     "请处理",
			SenderName: "wangyi71",
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	payload := decodePayload(t, transport.payload)
	reply := objectAt(t, payload, "message", "header", "extra", "reply")
	if reply["messageId"] != "1880207593545261334" {
		t.Fatalf("messageId = %v", reply["messageId"])
	}
	if reply["digest"] != "请处理" || reply["senderName"] != "wangyi71" {
		t.Fatalf("reply = %v", reply)
	}
	if len(reply) != 3 {
		t.Fatalf("unexpected reply fields: %v", reply)
	}
}

func TestSendReplyOmitsEmptyDigestAndSenderName(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	if _, err := service.Send(context.Background(), SendRequest{
		ToUser:  "user_1001",
		Content: NewContent(NewMarkdown("desc", "x")),
		Reply:   &ReplyOptions{MessageID: "m-1"},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	payload := decodePayload(t, transport.payload)
	reply := objectAt(t, payload, "message", "header", "extra", "reply")
	if len(reply) != 1 {
		t.Fatalf("optional reply fields must be omitted: %v", reply)
	}
	if reply["messageId"] != "m-1" {
		t.Fatalf("messageId = %v", reply["messageId"])
	}
}

func TestSendAtReplyAndSessionIDAllPreserved(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	if _, err := service.Send(context.Background(), SendRequest{
		GroupID:   "group_2002",
		SessionID: "session-1",
		Content:   NewContent(NewMarkdown("desc", "@wangyi71 hi")),
		At:        &AtOptions{Users: []string{"wangyi71"}},
		Reply:     &ReplyOptions{MessageID: "1880207593545261334"},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	payload := decodePayload(t, transport.payload)
	extra := objectAt(t, payload, "message", "header", "extra")
	if extra[SendSessionIDKey] != "session-1" {
		t.Fatalf("sessionId = %v", extra[SendSessionIDKey])
	}
	reply := objectAt(t, extra, "reply")
	if reply["messageId"] != "1880207593545261334" {
		t.Fatalf("messageId = %v", reply["messageId"])
	}
	if len(extra) != 2 {
		t.Fatalf("unexpected header.extra fields: %v", extra)
	}
	node := bodyATNodes(t, payload)[0].(map[string]any)
	if ids := stringList(t, node["atusers"], "atusers"); !reflect.DeepEqual(ids, []string{"wangyi71"}) {
		t.Fatalf("atusers = %v", ids)
	}
}

func TestSendWithoutAtReplyPayloadUnchanged(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	if _, err := service.Send(context.Background(), SendRequest{
		ToUser:    "user_1001",
		SessionID: "session-1",
		Content:   NewContent(NewMarkdown("desc", "x")),
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	got := decodePayload(t, transport.payload)
	want := map[string]any{
		"message": map[string]any{
			"header": map[string]any{
				"toType":  SendToTypeSingle,
				"toId":    map[string]any{"toUser": "user_1001"},
				"msgType": SendMsgTypeInteractive,
				"role":    SendRoleRobot,
				"extra":   map[string]any{SendSessionIDKey: "session-1"},
			},
			"contents": map[string]any{
				"scheme": SchemeV1,
				"body": map[string]any{
					"children": []any{
						map[string]any{"type": ComponentTypeMarkdown, "id": "desc", "content": "x"},
					},
				},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload changed without at/reply:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestSendAtReplyValidationDoesNotSend(t *testing.T) {
	validContent := NewContent(NewMarkdown("desc", "x"))
	cases := []struct {
		name    string
		request SendRequest
	}{
		{
			name:    "blank at user id",
			request: SendRequest{GroupID: "g", Content: validContent, At: &AtOptions{Users: []string{"ok", "  "}}},
		},
		{
			name:    "empty at user id",
			request: SendRequest{GroupID: "g", Content: validContent, At: &AtOptions{Users: []string{""}}},
		},
		{
			name:    "missing reply message id",
			request: SendRequest{ToUser: "u", Content: validContent, Reply: &ReplyOptions{Digest: "d"}},
		},
		{
			name:    "blank reply message id",
			request: SendRequest{ToUser: "u", Content: validContent, Reply: &ReplyOptions{MessageID: " \t "}},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, transport := newFakeService(sendSuccessResponse)
			if _, err := service.Send(context.Background(), testCase.request); err == nil {
				t.Fatal("expected validation error")
			} else {
				var failure *DynamicCardError
				if !errors.As(err, &failure) || !failure.IsValidation() {
					t.Fatalf("error = %v, want validation DynamicCardError", err)
				}
			}
			if transport.calls != 0 {
				t.Fatalf("validation sent %d requests", transport.calls)
			}
		})
	}
}

func TestFormatReplyMessageID(t *testing.T) {
	got, err := FormatReplyMessageID(42)
	if err != nil || got != "42" {
		t.Fatalf("FormatReplyMessageID(42) = %q, %v", got, err)
	}
	got, err = FormatReplyMessageID(MaxSafeReplyMessageID)
	if err != nil || got != "9007199254740991" {
		t.Fatalf("FormatReplyMessageID(max safe) = %q, %v", got, err)
	}
	for _, value := range []int64{-1, MaxSafeReplyMessageID + 1, 1880207593545261334} {
		text, err := FormatReplyMessageID(value)
		if err == nil {
			t.Fatalf("FormatReplyMessageID(%d) = %q, want local rejection", value, text)
		}
		var failure *DynamicCardError
		if !errors.As(err, &failure) || !failure.IsValidation() {
			t.Fatalf("error = %v, want validation DynamicCardError", err)
		}
	}
}

func TestSendRawAtReplyStrongTypedWins(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	if _, err := service.Send(context.Background(), SendRequest{
		ToUser:    "user_1001",
		SessionID: "session-1",
		Content:   NewContent(NewMarkdown("desc", "x")),
		At:        &AtOptions{Users: []string{"typed"}},
		Reply:     &ReplyOptions{MessageID: "typed-id", Digest: "typed-digest"},
		Raw: map[string]json.RawMessage{
			"message": json.RawMessage(`{
				"header": {"extra": {
					"at": {"atAll": true, "atUserIds": ["raw"], "rawAtFlag": true},
					"reply": {"messageId": "raw-id", "digest": "raw-digest", "senderName": "raw-sender"},
					"trace": "abc"
				}}
			}`),
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	payload := decodePayload(t, transport.payload)
	extra := objectAt(t, payload, "message", "header", "extra")
	if extra[SendSessionIDKey] != "session-1" {
		t.Fatalf("raw overrode sessionId: %v", extra[SendSessionIDKey])
	}
	if extra["trace"] != "abc" {
		t.Fatalf("unknown raw extra field was dropped: %v", extra)
	}
	// The typed AT wins the documented position: message.body carries it. A raw
	// header.extra.at is not a v37 field, so it survives only as raw passthrough.
	node := bodyATNodes(t, payload)[0].(map[string]any)
	if ids := stringList(t, node["atusers"], "atusers"); !reflect.DeepEqual(ids, []string{"typed"}) {
		t.Fatalf("raw overrode typed at users: %v", ids)
	}
	rawAt := objectAt(t, extra, "at")
	if rawAt["atAll"] != true || rawAt["rawAtFlag"] != true {
		t.Fatalf("raw header at passthrough was dropped: %v", rawAt)
	}
	reply := objectAt(t, extra, "reply")
	if reply["messageId"] != "typed-id" || reply["digest"] != "typed-digest" {
		t.Fatalf("raw overrode typed reply: %v", reply)
	}
	if reply["senderName"] != "raw-sender" {
		t.Fatalf("unknown raw reply field was dropped: %v", reply)
	}
}

func bodyATNodes(t *testing.T, payload map[string]any) []any {
	t.Helper()
	message := objectAt(t, payload, "message")
	body, ok := message["body"].([]any)
	if !ok {
		t.Fatalf("message.body is not an array: %T", message["body"])
	}
	return body
}

// AT-05: an unknown transport is rejected locally without a request.
func TestSendAtInvalidTransportRejected(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	_, err := service.Send(context.Background(), SendRequest{
		GroupID: "g",
		Content: NewContent(NewMarkdown("desc", "x")),
		At:      &AtOptions{Users: []string{"u"}, Transport: "query"},
	})
	var failure *DynamicCardError
	if !errors.As(err, &failure) || !failure.IsValidation() {
		t.Fatalf("error = %v, want validation DynamicCardError", err)
	}
	if transport.calls != 0 {
		t.Fatalf("validation sent %d requests", transport.calls)
	}
}

// AT-06: an existing raw body array is preserved and the AT node is appended;
// a non-array raw body is rejected when the SDK must write AT.
func TestSendAtTransportBodyAppendsRawBody(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	if _, err := service.Send(context.Background(), SendRequest{
		GroupID: "group_2002",
		Content: NewContent(NewText("t1", "hi")),
		At:      &AtOptions{Users: []string{"wangyi71"}},
		Raw: map[string]json.RawMessage{
			"message": json.RawMessage(`{"body":[{"type":"AT","atall":false,"atuserids":["raw-user"]}],"trace":"abc"}`),
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	payload := decodePayload(t, transport.payload)
	nodes := bodyATNodes(t, payload)
	if len(nodes) != 2 {
		t.Fatalf("body nodes = %v", nodes)
	}
	first := nodes[0].(map[string]any)
	if ids := stringList(t, first["atuserids"], "raw atuserids"); !reflect.DeepEqual(ids, []string{"raw-user"}) {
		t.Fatalf("raw node was not preserved first: %v", first)
	}
	second := nodes[1].(map[string]any)
	if ids := stringList(t, second["atusers"], "sdk atusers"); !reflect.DeepEqual(ids, []string{"wangyi71"}) {
		t.Fatalf("sdk AT node was not appended: %v", second)
	}
	if message := objectAt(t, payload, "message"); message["trace"] != "abc" {
		t.Fatalf("raw message field was dropped: %v", message)
	}

	for name, rawBody := range map[string]string{
		"object": `{"body":{}}`,
		"string": `{"body":"x"}`,
	} {
		t.Run("non-array "+name, func(t *testing.T) {
			service, transport := newFakeService(sendSuccessResponse)
			_, err := service.Send(context.Background(), SendRequest{
				GroupID: "group_2002",
				Content: NewContent(NewText("t1", "hi")),
				At:      &AtOptions{Users: []string{"wangyi71"}},
				Raw:     map[string]json.RawMessage{"message": json.RawMessage(rawBody)},
			})
			if err == nil {
				t.Fatal("expected validation error")
			}
			var failure *DynamicCardError
			if !errors.As(err, &failure) || !failure.IsValidation() {
				t.Fatalf("error = %v, want validation DynamicCardError", err)
			}
			if transport.calls != 0 {
				t.Fatalf("validation sent %d requests", transport.calls)
			}
		})
	}
}

// AT-07: no at and no raw body never emits message.body.
func TestSendWithoutAtOmitsMessageBody(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	if _, err := service.Send(context.Background(), SendRequest{
		ToUser:    "user_1001",
		SessionID: "session-1",
		Content:   NewContent(NewMarkdown("desc", "x")),
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	payload := decodePayload(t, transport.payload)
	if _, exists := objectAt(t, payload, "message")["body"]; exists {
		t.Fatalf("message.body must be absent without at: %v", payload)
	}
}

// RS-01: messageId comes from data.data.partial.data.messageid.
func TestSendParsesMessageIDFromPartialData(t *testing.T) {
	response := `{
	  "code": "ok",
	  "data": {
	    "code": 0,
	    "errmsg": "success",
	    "data": {
	      "messageid": null,
	      "msgseqid": null,
	      "ctime": null,
	      "partial": {
	        "errcode": 0,
	        "errmsg": "ok",
	        "data": {"messageid": "1880346648999218812", "msgseqid": 300011132, "ctime": 1793238305149}
	      },
	      "extra": {"card_id": "card_nested", "secret": "s"}
	    }
	  }
	}`
	service, _ := newFakeService(response)
	ref, err := service.Send(context.Background(), SendRequest{
		ToUser: "u", Content: NewContent(NewMarkdown("desc", "x")),
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if ref.MessageID != "1880346648999218812" {
		t.Fatalf("messageId = %q", ref.MessageID)
	}
	if ref.MsgSeqID != "300011132" || ref.CTime != "1793238305149" {
		t.Fatalf("seq/ctime = %q / %q", ref.MsgSeqID, ref.CTime)
	}
	if ref.MsgKey != "" {
		t.Fatalf("msgkey = %q", ref.MsgKey)
	}
}

// RS-02: top-level fallback, and null never renders "null".
func TestSendMessageIDFallbackAndNull(t *testing.T) {
	cases := []struct {
		name     string
		response string
		want     string
		wantSeq  string
		wantTime string
	}{
		{
			name:     "top-level messageid",
			response: `{"code":"ok","data":{"code":0,"data":{"partial":{"data":{"messageid":null}},"messageid":"1880000000000000001","msgseqid":7,"ctime":8,"extra":{"card_id":"c"}}}}`,
			want:     "1880000000000000001", wantSeq: "7", wantTime: "8",
		},
		{
			name:     "top-level message_id",
			response: `{"code":"ok","data":{"code":0,"data":{"partial":{"data":{"messageid":null}},"message_id":"1880000000000000002","msg_seq_id":9,"c_time":10,"extra":{"card_id":"c"}}}}`,
			want:     "1880000000000000002", wantSeq: "9", wantTime: "10",
		},
		{
			name:     "partial msgid",
			response: `{"code":"ok","data":{"code":0,"data":{"partial":{"data":{"msgid":"1880000000000000003","msg_id":"1880000000000000004"}},"messageid":null,"extra":{"card_id":"c"}}}}`,
			want:     "1880000000000000003",
		},
		{
			name:     "all null",
			response: `{"code":"ok","data":{"code":0,"data":{"partial":{"data":{"messageid":null}},"messageid":null,"message_id":null,"extra":{"card_id":"c","secret":null}}}}`,
			want:     "",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, _ := newFakeService(testCase.response)
			ref, err := service.Send(context.Background(), SendRequest{
				ToUser: "u", Content: NewContent(NewMarkdown("desc", "x")),
			})
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if ref.MessageID != testCase.want {
				t.Fatalf("messageId = %q, want %q", ref.MessageID, testCase.want)
			}
			if testCase.want == "" && ref.MessageID == "null" {
				t.Fatal("null messageId must not become the string \"null\"")
			}
			if ref.MsgSeqID != testCase.wantSeq || ref.CTime != testCase.wantTime {
				t.Fatalf("seq/ctime = %q / %q", ref.MsgSeqID, ref.CTime)
			}
		})
	}
}

// RS-03: only partial.msgkey exists -> stringified fallback without precision loss.
func TestSendMessageIDFallsBackToMsgKey(t *testing.T) {
	service, _ := newFakeService(`{
	  "code": "ok",
	  "data": {"code": 0, "data": {"partial": {"msgkey": 1879549251012710400}, "extra": {"card_id": "c"}}}
	}`)
	ref, err := service.Send(context.Background(), SendRequest{
		ToUser: "u", Content: NewContent(NewMarkdown("desc", "x")),
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if ref.MessageID != "1879549251012710400" || ref.MsgKey != "1879549251012710400" {
		t.Fatalf("messageId = %q, msgKey = %q", ref.MessageID, ref.MsgKey)
	}
}

// AB-03: a send carries the top-level contents.footer (a sibling of body) with
// the same component model, including action_bar.
func TestSendCarriesFooter(t *testing.T) {
	service, transport := newFakeService(sendSuccessResponse)
	content := NewContent(NewMarkdown("desc", "构建完成"))
	content.Footer = NewFooter(NewActionBar("ai_ops",
		ActionBarItem{Key: ActionKeyAITag, Title: map[string]string{"zh_cn": "AI 生成"}},
		ActionBarItem{Key: ActionKeyVoteUp, EventEnabled: Bool(true)},
	))
	if _, err := service.Send(context.Background(), SendRequest{
		GroupID: "group_2002",
		Content: content,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	payload := decodePayload(t, transport.payload)
	contents := objectAt(t, payload, "message", "contents")
	if _, exists := objectAt(t, payload, "message", "contents", "body")["footer"]; exists {
		t.Fatalf("footer must not be nested inside contents.body: %v", contents)
	}
	footer, ok := contents["footer"].(map[string]any)
	if !ok {
		t.Fatalf("contents.footer = %v", contents["footer"])
	}
	children, ok := footer["children"].([]any)
	if !ok || len(children) != 1 {
		t.Fatalf("footer children = %v", footer["children"])
	}
	bar := children[0].(map[string]any)
	if bar["type"] != ComponentTypeActionBar || bar["id"] != "ai_ops" {
		t.Fatalf("action_bar = %v", bar)
	}
	actions, ok := bar["actions"].([]any)
	if !ok || len(actions) != 2 || actions[1].(map[string]any)["key"] != ActionKeyVoteUp {
		t.Fatalf("actions = %v", bar["actions"])
	}
}
