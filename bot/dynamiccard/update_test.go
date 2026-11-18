package dynamiccard

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	dcinternal "github.com/dolfly/infoflow/internal/dynamiccard"
)

const updateFullSuccessResponse = `{
  "code": "ok",
  "data": {"code": 0, "card_id": "card_update_001", "sequence": 4, "message": "success"}
}`

const updateElementSuccessResponse = `{
  "code": "ok",
  "data": {"code": 0, "card_id": "card_update_001", "sequence": 5}
}`

func TestUpdateFullBuildsPayload(t *testing.T) {
	service, transport := newFakeService(updateFullSuccessResponse)
	result, err := service.Update(context.Background(), UpdateRequest{
		CardID:     "card_update_001",
		UpdateType: UpdateTypeFull,
		CardContent: NewContent(
			NewMarkdown("desc", "全量替换后的内容"),
			NewButton("ok", "确认"),
		),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if transport.route != dcinternal.RouteUpdate {
		t.Fatalf("route = %q", transport.route)
	}
	if transport.calls != 1 {
		t.Fatalf("calls = %d", transport.calls)
	}
	payload := decodePayload(t, transport.payload)
	if payload["card_id"] != "card_update_001" || payload["update_type"] != string(UpdateTypeFull) {
		t.Fatalf("payload = %v", payload)
	}
	if _, exists := payload["updates"]; exists {
		t.Fatalf("full update must not carry updates: %v", payload)
	}
	if _, exists := payload["sequence"]; exists {
		t.Fatalf("update must never carry sequence: %v", payload)
	}
	content := objectAt(t, payload, "card_content")
	if content["scheme"] != SchemeV1 {
		t.Fatalf("scheme = %v", content["scheme"])
	}
	children, ok := objectAt(t, content, "body")["children"].([]any)
	if !ok || len(children) != 2 {
		t.Fatalf("children = %v", content["body"])
	}
	if result.CardID != "card_update_001" || result.Sequence != 4 {
		t.Fatalf("result = %+v", result)
	}
	if result.Message != "success" {
		t.Fatalf("message = %q", result.Message)
	}
	if len(result.RawJSON()) == 0 {
		t.Fatal("raw response must be retained")
	}
}

func TestUpdateElementBuildsPayload(t *testing.T) {
	service, transport := newFakeService(updateElementSuccessResponse)
	result, err := service.Update(context.Background(), UpdateRequest{
		CardID:     "card_update_001",
		UpdateType: UpdateTypeElement,
		Updates: ComponentList{
			NewMarkdown("desc", "局部替换后的内容"),
			NewButton("ok", "已完成"),
		},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if transport.route != dcinternal.RouteUpdate || transport.calls != 1 {
		t.Fatalf("route = %q, calls = %d", transport.route, transport.calls)
	}
	payload := decodePayload(t, transport.payload)
	if payload["update_type"] != string(UpdateTypeElement) {
		t.Fatalf("update_type = %v", payload["update_type"])
	}
	if _, exists := payload["card_content"]; exists {
		t.Fatalf("element update must not carry card_content: %v", payload)
	}
	if _, exists := payload["sequence"]; exists {
		t.Fatalf("update must never carry sequence: %v", payload)
	}
	updates, ok := payload["updates"].([]any)
	if !ok || len(updates) != 2 {
		t.Fatalf("updates = %v", payload["updates"])
	}
	first, _ := updates[0].(map[string]any)
	if first["type"] != ComponentTypeMarkdown || first["id"] != "desc" {
		t.Fatalf("first update = %v", first)
	}
	if result.Sequence != 5 {
		t.Fatalf("sequence = %d", result.Sequence)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
}

func TestUpdateLocalValidationDoesNotSend(t *testing.T) {
	validContent := NewContent(NewMarkdown("desc", "x"))
	validUpdates := ComponentList{NewText("t1", "x")}
	cases := []struct {
		name    string
		request UpdateRequest
	}{
		{"blank card id", UpdateRequest{CardID: "  ", UpdateType: UpdateTypeFull, CardContent: validContent}},
		{"missing update type", UpdateRequest{CardID: "c", CardContent: validContent}},
		{"unknown update type", UpdateRequest{CardID: "c", UpdateType: "partial", CardContent: validContent}},
		{"full without content", UpdateRequest{CardID: "c", UpdateType: UpdateTypeFull}},
		{"full with updates", UpdateRequest{CardID: "c", UpdateType: UpdateTypeFull, CardContent: validContent, Updates: validUpdates}},
		{"full bad scheme", UpdateRequest{CardID: "c", UpdateType: UpdateTypeFull,
			CardContent: &DynamicCardContent{Scheme: "2", Body: validContent.Body}}},
		{"full empty children", UpdateRequest{CardID: "c", UpdateType: UpdateTypeFull,
			CardContent: &DynamicCardContent{Scheme: SchemeV1}}},
		{"full oversize", UpdateRequest{CardID: "c", UpdateType: UpdateTypeFull,
			CardContent: NewContent(NewMarkdown("desc", string(make([]byte, MaxContentBytes))))}},
		{"element without updates", UpdateRequest{CardID: "c", UpdateType: UpdateTypeElement}},
		{"element with content", UpdateRequest{CardID: "c", UpdateType: UpdateTypeElement,
			CardContent: validContent, Updates: validUpdates}},
		{"element nil component", UpdateRequest{CardID: "c", UpdateType: UpdateTypeElement,
			Updates: ComponentList{nil}}},
		{"element missing type", UpdateRequest{CardID: "c", UpdateType: UpdateTypeElement,
			Updates: ComponentList{NewRaw("", "id1", nil)}}},
		{"element missing id", UpdateRequest{CardID: "c", UpdateType: UpdateTypeElement,
			Updates: ComponentList{NewRaw(ComponentTypeText, "", nil)}}},
		{"element duplicate id", UpdateRequest{CardID: "c", UpdateType: UpdateTypeElement,
			Updates: ComponentList{NewText("t1", "a"), NewText("t1", "b")}}},
		{"element duplicate nested id", UpdateRequest{CardID: "c", UpdateType: UpdateTypeElement,
			Updates: ComponentList{
				NewRow("r1", NewText("t1", "a")),
				NewRow("r2", NewText("t1", "b")),
			}}},
		{"element nested missing type", UpdateRequest{CardID: "c", UpdateType: UpdateTypeElement,
			Updates: ComponentList{NewRow("r1", NewRaw("", "t1", nil))}}},
		{"element ancestor and descendant", UpdateRequest{CardID: "c", UpdateType: UpdateTypeElement,
			Updates: ComponentList{
				NewRow("r1", NewButton("b1", "x")),
				NewButton("b1", "y"),
			}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, transport := newFakeService(updateFullSuccessResponse)
			if _, err := service.Update(context.Background(), testCase.request); err == nil {
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

func TestUpdateBaselineValidation(t *testing.T) {
	baseline := NewContent(
		NewRow("r1", NewButton("b1", "x")),
		NewText("t1", "a"),
	)
	cases := []struct {
		name    string
		updates ComponentList
	}{
		{"unknown target", ComponentList{NewText("missing", "x")}},
		{"type changed", ComponentList{NewButton("t1", "x")}},
		{"baseline ancestor and descendant", ComponentList{NewRow("r1", NewText("t2", "x")), NewButton("b1", "y")}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, transport := newFakeService(updateElementSuccessResponse)
			_, err := service.Update(context.Background(), UpdateRequest{
				CardID:         "card_update_001",
				UpdateType:     UpdateTypeElement,
				Updates:        testCase.updates,
				CurrentContent: baseline,
			})
			var failure *DynamicCardError
			if !errors.As(err, &failure) || !failure.IsValidation() {
				t.Fatalf("error = %v, want validation DynamicCardError", err)
			}
			if transport.calls != 0 {
				t.Fatalf("baseline validation sent %d requests", transport.calls)
			}
		})
	}
}

func TestUpdateContainerChildrenMissingWarnsButSends(t *testing.T) {
	baseline := NewContent(NewRow("r1", NewButton("b1", "x")))
	service, transport := newFakeService(updateElementSuccessResponse)
	result, err := service.Update(context.Background(), UpdateRequest{
		CardID:         "card_update_001",
		UpdateType:     UpdateTypeElement,
		Updates:        ComponentList{NewRow("r1")},
		CurrentContent: baseline,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if transport.calls != 1 {
		t.Fatalf("calls = %d", transport.calls)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != WarningContainerChildrenMissing {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
}

func TestUpdateStreamStateIsFinishFirst(t *testing.T) {
	response := `{
	  "code": 1010,
	  "message": "stream is opened now",
	  "current_sequence": 6,
	  "current_msg_content": {"scheme": "1", "body": {"children": []}}
	}`
	service, transport := newFakeService(response)
	_, err := service.Update(context.Background(), UpdateRequest{
		CardID:      "card_update_001",
		UpdateType:  UpdateTypeFull,
		CardContent: NewContent(NewMarkdown("desc", "x")),
	})
	var failure *DynamicCardError
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want DynamicCardError", err)
	}
	if !failure.IsStreamState() {
		t.Fatalf("category = %q", failure.Category)
	}
	if failure.Retryable {
		t.Fatal("stream_state must not be retryable")
	}
	if !strings.Contains(failure.Message, "请先 finish 流式消息") {
		t.Fatalf("message = %q", failure.Message)
	}
	if failure.CurrentSequence == nil || *failure.CurrentSequence != 6 {
		t.Fatalf("current sequence = %v", failure.CurrentSequence)
	}
	if failure.CurrentContent == nil || failure.CurrentContent.Scheme != SchemeV1 {
		t.Fatalf("current content = %+v", failure.CurrentContent)
	}
	if transport.calls != 1 {
		t.Fatalf("calls = %d, update must not retry", transport.calls)
	}
}

func TestUpdateBusinessErrors(t *testing.T) {
	cases := []struct {
		name     string
		response string
		check    func(*testing.T, *DynamicCardError)
	}{
		{
			name:     "1001 validation",
			response: `{"code": 1001, "message": "sequence is not supported"}`,
			check: func(t *testing.T, failure *DynamicCardError) {
				if !failure.IsValidation() || failure.Retryable {
					t.Fatalf("classification = %+v", failure)
				}
				if failure.Operation != OperationUpdate {
					t.Fatalf("operation = %q", failure.Operation)
				}
			},
		},
		{
			name:     "3003 ownership",
			response: `{"code": 3003, "message": "message does not belong to this robot"}`,
			check: func(t *testing.T, failure *DynamicCardError) {
				if !failure.IsOwnership() || failure.Retryable {
					t.Fatalf("classification = %+v", failure)
				}
			},
		},
		{
			name:     "9001 server without auto retry",
			response: `{"code": 9001, "message": "internal error"}`,
			check: func(t *testing.T, failure *DynamicCardError) {
				if !failure.IsServer() || !failure.Retryable {
					t.Fatalf("classification = %+v", failure)
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, transport := newFakeService(testCase.response)
			_, err := service.Update(context.Background(), UpdateRequest{
				CardID:      "card_update_001",
				UpdateType:  UpdateTypeFull,
				CardContent: NewContent(NewMarkdown("desc", "x")),
			})
			var failure *DynamicCardError
			if !errors.As(err, &failure) {
				t.Fatalf("error = %v, want DynamicCardError", err)
			}
			testCase.check(t, failure)
			if transport.calls != 1 {
				t.Fatalf("calls = %d, update must not retry", transport.calls)
			}
		})
	}
}

func TestUpdateResponseFallbacks(t *testing.T) {
	service, _ := newFakeService(`{"code":"ok","data":{"code":0}}`)
	result, err := service.Update(context.Background(), UpdateRequest{
		CardID:      "card_update_001",
		UpdateType:  UpdateTypeFull,
		CardContent: NewContent(NewMarkdown("desc", "x")),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if result.CardID != "card_update_001" {
		t.Fatalf("card id fallback = %q", result.CardID)
	}
	if result.Sequence != 0 {
		t.Fatalf("sequence = %d", result.Sequence)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != WarningSequenceMissing {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
}

func TestUpdateInvalidSequenceIsServerError(t *testing.T) {
	service, _ := newFakeService(`{"code":"ok","data":{"code":0,"card_id":"c","sequence":"abc"}}`)
	_, err := service.Update(context.Background(), UpdateRequest{
		CardID:      "c",
		UpdateType:  UpdateTypeFull,
		CardContent: NewContent(NewMarkdown("desc", "x")),
	})
	var failure *DynamicCardError
	if !errors.As(err, &failure) || !failure.IsServer() {
		t.Fatalf("error = %v, want server DynamicCardError", err)
	}
	if len(failure.Raw) == 0 {
		t.Fatal("invalid sequence error must retain the raw response")
	}
}

func TestUpdatePayloadHasNoSequenceField(t *testing.T) {
	raw, err := json.Marshal(updatePayload{
		CardID: "c", UpdateType: UpdateTypeElement,
		Updates: ComponentList{NewText("t1", "x")},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, exists := decoded["sequence"]; exists {
		t.Fatalf("payload carries sequence: %s", raw)
	}
	if _, exists := decoded["card_content"]; exists {
		t.Fatalf("element payload carries card_content: %s", raw)
	}
}

// AB-03: a full update carries the top-level footer exactly like a send, and
// the footer stays a sibling of body on the wire.
func TestUpdateFullCarriesFooter(t *testing.T) {
	service, transport := newFakeService(updateFullSuccessResponse)
	content := NewContent(NewMarkdown("desc", "更新后的内容"))
	content.Footer = NewFooter(NewActionBar("ai_ops",
		ActionBarItem{Key: ActionKeyVoteUp, EventEnabled: Bool(true)},
	))
	result, err := service.Update(context.Background(), UpdateRequest{
		CardID:      "card_update_001",
		UpdateType:  UpdateTypeFull,
		CardContent: content,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if result.Sequence != 4 {
		t.Fatalf("sequence = %d", result.Sequence)
	}
	payload := decodePayload(t, transport.payload)
	cardContent := objectAt(t, payload, "card_content")
	if _, exists := objectAt(t, cardContent, "body")["footer"]; exists {
		t.Fatal("footer must not be nested inside body")
	}
	footer, ok := cardContent["footer"].(map[string]any)
	if !ok {
		t.Fatalf("footer = %v", cardContent["footer"])
	}
	children, ok := footer["children"].([]any)
	if !ok || len(children) != 1 {
		t.Fatalf("footer children = %v", footer["children"])
	}
	bar := children[0].(map[string]any)
	if bar["type"] != ComponentTypeActionBar || bar["id"] != "ai_ops" {
		t.Fatalf("action_bar = %v", bar)
	}
	actions := bar["actions"].([]any)
	if actions[0].(map[string]any)["key"] != ActionKeyVoteUp {
		t.Fatalf("actions = %v", actions)
	}
}

// AB-03 / IF-02: an element update can replace an action_bar that lives in the
// footer, and the baseline index includes footer children so a wrong target id
// is rejected locally.
func TestUpdateElementInFooter(t *testing.T) {
	bar := NewActionBar("ai_ops", ActionBarItem{Key: ActionKeyVoteUp, EventEnabled: Bool(true)})
	baseline := NewContent(NewMarkdown("desc", "x"))
	baseline.Footer = NewFooter(bar)
	service, transport := newFakeService(updateElementSuccessResponse)
	result, err := service.Update(context.Background(), UpdateRequest{
		CardID:     "card_update_001",
		UpdateType: UpdateTypeElement,
		Updates: ComponentList{
			NewActionBar("ai_ops",
				ActionBarItem{Key: ActionKeyAITag, Title: map[string]string{"zh_cn": "AI"}},
				ActionBarItem{Key: ActionKeyVoteUp, EventEnabled: Bool(true)},
				ActionBarItem{Key: ActionKeyVoteDown, EventEnabled: Bool(true)},
			),
		},
		CurrentContent: baseline,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if result.Sequence != 5 {
		t.Fatalf("sequence = %d", result.Sequence)
	}
	payload := decodePayload(t, transport.payload)
	if _, exists := payload["sequence"]; exists {
		t.Fatalf("update must never carry sequence: %v", payload)
	}
	updates, ok := payload["updates"].([]any)
	if !ok || len(updates) != 1 {
		t.Fatalf("updates = %v", payload["updates"])
	}
	update := updates[0].(map[string]any)
	if update["type"] != ComponentTypeActionBar || update["id"] != "ai_ops" {
		t.Fatalf("update = %v", update)
	}
	actions, ok := update["actions"].([]any)
	if !ok || len(actions) != 3 {
		t.Fatalf("actions = %v", update["actions"])
	}

	_, err = service.Update(context.Background(), UpdateRequest{
		CardID:         "card_update_001",
		UpdateType:     UpdateTypeElement,
		Updates:        ComponentList{NewText("missing_in_footer", "x")},
		CurrentContent: baseline,
	})
	var failure *DynamicCardError
	if !errors.As(err, &failure) || !failure.IsValidation() {
		t.Fatalf("error = %v, want validation for an unknown footer target", err)
	}
}

// AT-08: @ can only be declared on send. The update request and the streaming
// payloads expose no AT field, so AT cannot be reintroduced on a later call.
func TestUpdateAndStreamingTypesExposeNoAT(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(UpdateRequest{}),
		reflect.TypeOf(streamEnablePayload{}),
		reflect.TypeOf(streamAppendPayload{}),
		reflect.TypeOf(streamFinishPayload{}),
		reflect.TypeOf(StreamBody{}),
	}
	for _, typ := range types {
		for index := 0; index < typ.NumField(); index++ {
			name := strings.ToLower(typ.Field(index).Name)
			if name == "at" || name == "atusers" || strings.Contains(name, "mention") {
				t.Fatalf("%s exposes AT field %q", typ.Name(), typ.Field(index).Name)
			}
		}
	}
}
