package dynamiccard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	dcinternal "github.com/dolfly/infoflow/internal/dynamiccard"
)

const ackSuccessResponse = `{
  "code": "ok",
  "data": {
    "code": 0,
    "card_id": "card_evt_001",
    "event_id": "evt_0001",
    "sequence": 5,
    "message": "ok"
  }
}`

const ackDuplicateResponse = `{
  "code": "ok",
  "data": {
    "code": 0,
    "card_id": "card_evt_001",
    "event_id": "evt_0001",
    "message": "ack already processed"
  }
}`

func ackFlatFailure(code string, message string) string {
	body, err := json.Marshal(map[string]any{
		"code":     json.Number(code),
		"card_id":  "card_evt_001",
		"event_id": "evt_0001",
		"message":  message,
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func syncAckData() AckData {
	button := NewButton("ok", "已提交")
	button.Enabled = Bool(false)
	return AckData{
		Code:  0,
		Mode:  AckModeSync,
		Toast: &AckToast{Level: AckToastLevelSuccess, Content: "已提交"},
		CardUpdate: &CardUpdate{
			UpdateType: UpdateTypeElement,
			Updates:    ComponentList{NewMarkdown("desc", "已提交"), button},
		},
	}
}

func TestAckSyncBuildsPayload(t *testing.T) {
	service, transport := newFakeService(ackSuccessResponse)
	result, err := service.Ack(context.Background(), AckRequest{
		CardID:  "card_evt_001",
		EventID: "evt_0001",
		AckData: syncAckData(),
	})
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	if transport.route != dcinternal.RouteCardEventAck {
		t.Fatalf("route = %q", transport.route)
	}
	if transport.calls != 1 {
		t.Fatalf("calls = %d", transport.calls)
	}
	payload := decodePayload(t, transport.payload)
	if payload["card_id"] != "card_evt_001" || payload["event_id"] != "evt_0001" {
		t.Fatalf("payload = %v", payload)
	}
	if _, exists := payload["sequence"]; exists {
		t.Fatalf("ack must never carry sequence: %v", payload)
	}
	data := objectAt(t, payload, "ack_data")
	if data["code"] != float64(0) || data["mode"] != string(AckModeSync) {
		t.Fatalf("ack_data = %v", data)
	}
	toast := objectAt(t, data, "toast")
	if toast["level"] != AckToastLevelSuccess || toast["content"] != "已提交" {
		t.Fatalf("toast = %v", toast)
	}
	update := objectAt(t, data, "card_update")
	if update["update_type"] != string(UpdateTypeElement) {
		t.Fatalf("card_update = %v", update)
	}
	if _, exists := update["card_id"]; exists {
		t.Fatalf("card_update must not carry card_id: %v", update)
	}
	if _, exists := update["sequence"]; exists {
		t.Fatalf("card_update must not carry sequence: %v", update)
	}
	updates, ok := update["updates"].([]any)
	if !ok || len(updates) != 2 {
		t.Fatalf("updates = %v", update["updates"])
	}
	first, ok := updates[0].(map[string]any)
	if !ok || first["type"] != "markdown" || first["id"] != "desc" || first["content"] != "已提交" {
		t.Fatalf("updates[0] = %v", updates[0])
	}
	second, ok := updates[1].(map[string]any)
	if !ok || second["type"] != "button" || second["id"] != "ok" || second["enabled"] != false {
		t.Fatalf("updates[1] = %v", updates[1])
	}
	if result.Accepted != true || result.AlreadyProcessed {
		t.Fatalf("result = %+v", result)
	}
	if result.CardID != "card_evt_001" || result.EventID != "evt_0001" {
		t.Fatalf("identifiers = %+v", result)
	}
	if result.Sequence == nil || *result.Sequence != 5 {
		t.Fatalf("sequence = %v", result.Sequence)
	}
	if result.Message != "ok" {
		t.Fatalf("message = %q", result.Message)
	}
	if result.Mode != AckModeSync {
		t.Fatalf("mode = %q", result.Mode)
	}
	if result.ProceedWithBusiness {
		t.Fatal("sync ack must never ask the caller to run the business logic")
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings = %v", result.Warnings)
	}
	if len(result.RawJSON()) == 0 {
		t.Fatal("raw response must be retained")
	}
}

func TestAckAsyncAccepted(t *testing.T) {
	service, transport := newFakeService(ackSuccessResponse)
	result, err := service.Ack(context.Background(), AckRequest{
		CardID:  "card_evt_001",
		EventID: "evt_0001",
		AckData: AckData{Mode: AckModeAsync},
	})
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	data := objectAt(t, decodePayload(t, transport.payload), "ack_data")
	if data["code"] != float64(0) || data["mode"] != string(AckModeAsync) {
		t.Fatalf("ack_data = %v", data)
	}
	for _, key := range []string{"toast", "card_update"} {
		if _, exists := data[key]; exists {
			t.Fatalf("async ack_data must omit %q: %v", key, data)
		}
	}
	if !result.Accepted || !result.ProceedWithBusiness {
		t.Fatalf("result = %+v", result)
	}
}

func TestAckValidationMatrix(t *testing.T) {
	type testCase struct {
		name    string
		request AckRequest
		field   string
	}
	fullUpdate := &CardUpdate{
		UpdateType:  UpdateTypeFull,
		CardContent: NewContent(NewMarkdown("desc", "ok")),
	}
	cases := []testCase{
		{
			name:    "blank card id",
			request: AckRequest{CardID: "  ", EventID: "evt_0001", AckData: AckData{Mode: AckModeSync}},
			field:   "cardId",
		},
		{
			name:    "blank event id",
			request: AckRequest{CardID: "card_evt_001", AckData: AckData{Mode: AckModeSync}},
			field:   "eventId",
		},
		{
			name:    "missing ack data mode",
			request: AckRequest{CardID: "card_evt_001", EventID: "evt_0001"},
			field:   "ackData.mode",
		},
		{
			name: "unknown mode",
			request: AckRequest{CardID: "card_evt_001", EventID: "evt_0001",
				AckData: AckData{Mode: AckMode("later")}},
			field: "ackData.mode",
		},
		{
			name: "code non zero with async",
			request: AckRequest{CardID: "card_evt_001", EventID: "evt_0001",
				AckData: AckData{Code: 1001, Mode: AckModeAsync, Toast: &AckToast{Content: "失败"}}},
			field: "ackData.code",
		},
		{
			name: "code non zero without toast",
			request: AckRequest{CardID: "card_evt_001", EventID: "evt_0001",
				AckData: AckData{Code: 1001, Mode: AckModeSync}},
			field: "ackData.toast",
		},
		{
			name: "code non zero with card update",
			request: AckRequest{CardID: "card_evt_001", EventID: "evt_0001",
				AckData: AckData{Code: 1001, Mode: AckModeSync, Toast: &AckToast{Content: "失败"},
					CardUpdate: fullUpdate}},
			field: "ackData.cardUpdate",
		},
		{
			name: "async with card update",
			request: AckRequest{CardID: "card_evt_001", EventID: "evt_0001",
				AckData: AckData{Mode: AckModeAsync, CardUpdate: fullUpdate}},
			field: "ackData.cardUpdate",
		},
		{
			name: "toast without content",
			request: AckRequest{CardID: "card_evt_001", EventID: "evt_0001",
				AckData: AckData{Mode: AckModeSync, Toast: &AckToast{Content: "   "}}},
			field: "ackData.toast.content",
		},
		{
			name: "toast with unknown level",
			request: AckRequest{CardID: "card_evt_001", EventID: "evt_0001",
				AckData: AckData{Mode: AckModeSync, Toast: &AckToast{Level: "blink", Content: "ok"}}},
			field: "ackData.toast.level",
		},
		{
			name: "card update without update type",
			request: AckRequest{CardID: "card_evt_001", EventID: "evt_0001",
				AckData: AckData{Mode: AckModeSync, CardUpdate: &CardUpdate{}}},
			field: "ackData.cardUpdate.updateType",
		},
		{
			name: "full update without card content",
			request: AckRequest{CardID: "card_evt_001", EventID: "evt_0001",
				AckData: AckData{Mode: AckModeSync, CardUpdate: &CardUpdate{UpdateType: UpdateTypeFull}}},
			field: "ackData.cardUpdate.cardContent",
		},
		{
			name: "full update with updates",
			request: AckRequest{CardID: "card_evt_001", EventID: "evt_0001",
				AckData: AckData{Mode: AckModeSync, CardUpdate: &CardUpdate{
					UpdateType:  UpdateTypeFull,
					CardContent: NewContent(NewMarkdown("desc", "ok")),
					Updates:     ComponentList{NewMarkdown("other", "extra")},
				}}},
			field: "ackData.cardUpdate.updates",
		},
		{
			name: "full update with wrong scheme",
			request: AckRequest{CardID: "card_evt_001", EventID: "evt_0001",
				AckData: AckData{Mode: AckModeSync, CardUpdate: &CardUpdate{
					UpdateType: UpdateTypeFull,
					CardContent: &DynamicCardContent{
						Scheme: "2",
						Body:   CardBody{Children: ComponentList{NewMarkdown("desc", "ok")}},
					},
				}}},
			field: "ackData.cardUpdate.cardContent.scheme",
		},
		{
			name: "element update with card content",
			request: AckRequest{CardID: "card_evt_001", EventID: "evt_0001",
				AckData: AckData{Mode: AckModeSync, CardUpdate: &CardUpdate{
					UpdateType:  UpdateTypeElement,
					CardContent: NewContent(NewMarkdown("desc", "ok")),
					Updates:     ComponentList{NewMarkdown("desc", "ok")},
				}}},
			field: "ackData.cardUpdate.cardContent",
		},
		{
			name: "element update without updates",
			request: AckRequest{CardID: "card_evt_001", EventID: "evt_0001",
				AckData: AckData{Mode: AckModeSync, CardUpdate: &CardUpdate{UpdateType: UpdateTypeElement}}},
			field: "ackData.cardUpdate.updates",
		},
		{
			name: "element item without id",
			request: AckRequest{CardID: "card_evt_001", EventID: "evt_0001",
				AckData: AckData{Mode: AckModeSync, CardUpdate: &CardUpdate{
					UpdateType: UpdateTypeElement,
					Updates:    ComponentList{NewMarkdown("", "ok")},
				}}},
			field: "ackData.cardUpdate.updates[0].id",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, transport := newFakeService(ackSuccessResponse)
			result, err := service.Ack(context.Background(), testCase.request)
			if err == nil {
				t.Fatalf("expected a validation failure, got %+v", result)
			}
			var failure *DynamicCardError
			if !errors.As(err, &failure) {
				t.Fatalf("error type = %T", err)
			}
			if !failure.IsValidation() {
				t.Fatalf("category = %q", failure.Category)
			}
			if !strings.Contains(failure.Message, testCase.field) {
				t.Fatalf("message = %q, want it to mention %q", failure.Message, testCase.field)
			}
			if transport.calls != 0 {
				t.Fatalf("local rejection must not send a request: calls = %d", transport.calls)
			}
			if result.Accepted {
				t.Fatalf("result must not be accepted: %+v", result)
			}
		})
	}
}

func TestAckSyncFailureCode(t *testing.T) {
	service, transport := newFakeService(ackSuccessResponse)
	result, err := service.Ack(context.Background(), AckRequest{
		CardID:  "card_evt_001",
		EventID: "evt_0001",
		AckData: AckData{
			Code:  1,
			Mode:  AckModeSync,
			Toast: &AckToast{Level: AckToastLevelError, Content: "处理失败"},
		},
	})
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	data := objectAt(t, decodePayload(t, transport.payload), "ack_data")
	if data["code"] != float64(1) {
		t.Fatalf("ack_data = %v", data)
	}
	if _, exists := data["card_update"]; exists {
		t.Fatalf("a failed ack must not carry a card_update: %v", data)
	}
	if result.ProceedWithBusiness {
		t.Fatal("sync ack never runs the business logic")
	}
}

func TestAckFullCardUpdate(t *testing.T) {
	service, transport := newFakeService(ackSuccessResponse)
	_, err := service.Ack(context.Background(), AckRequest{
		CardID:  "card_evt_001",
		EventID: "evt_0001",
		AckData: AckData{
			Mode: AckModeSync,
			CardUpdate: &CardUpdate{
				UpdateType:  UpdateTypeFull,
				CardContent: NewContent(NewMarkdown("desc", "全量结果"), NewButton("ok", "完成")),
			},
		},
	})
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	update := objectAt(t, decodePayload(t, transport.payload), "ack_data", "card_update")
	if update["update_type"] != string(UpdateTypeFull) {
		t.Fatalf("card_update = %v", update)
	}
	content := objectAt(t, update, "card_content")
	if content["scheme"] != SchemeV1 {
		t.Fatalf("scheme = %v", content["scheme"])
	}
	children, ok := objectAt(t, content, "body")["children"].([]any)
	if !ok || len(children) != 2 {
		t.Fatalf("children = %v", content["body"])
	}
	if _, exists := update["updates"]; exists {
		t.Fatalf("a full card_update must not carry updates: %v", update)
	}
}

func TestCardUpdateNeverSerializesCardIDOrSequence(t *testing.T) {
	cases := []CardUpdate{
		{UpdateType: UpdateTypeFull, CardContent: NewContent(NewMarkdown("desc", "ok"))},
		{UpdateType: UpdateTypeElement, Updates: ComponentList{NewMarkdown("desc", "ok")}},
	}
	for _, update := range cases {
		raw, err := json.Marshal(update)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for _, forbidden := range []string{"card_id", "sequence"} {
			if _, exists := fields[forbidden]; exists {
				t.Fatalf("%s must never be serialized: %s", forbidden, raw)
			}
		}
	}
}

func TestAckSyncWindowWarning(t *testing.T) {
	service, transport := newFakeService(ackSuccessResponse)
	result, err := service.Ack(context.Background(), AckRequest{
		CardID:     "card_evt_001",
		EventID:    "evt_0001",
		AckData:    syncAckData(),
		ReceivedAt: time.Now().Add(-6 * time.Second),
	})
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	if transport.calls != 1 {
		t.Fatalf("a late sync ack is still sent: calls = %d", transport.calls)
	}
	if result.AgeMs < AckWindowMs {
		t.Fatalf("ageMs = %d", result.AgeMs)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != WarningAckWindowExceeded {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

func TestAckAsyncLateDefaultsToRequest(t *testing.T) {
	service, transport := newFakeService(ackFlatFailure("1011", "card event ack window closed: ack timeout"))
	_, err := service.Ack(context.Background(), AckRequest{
		CardID:     "card_evt_001",
		EventID:    "evt_0001",
		AckData:    AckData{Mode: AckModeAsync},
		ReceivedAt: time.Now().Add(-6 * time.Second),
	})
	if err == nil {
		t.Fatal("expected ack_expired")
	}
	if transport.calls != 1 {
		t.Fatalf("a late async ack is still sent by default: calls = %d", transport.calls)
	}
	var failure *DynamicCardError
	if !errors.As(err, &failure) || !failure.IsAckExpired() {
		t.Fatalf("error = %v", err)
	}
}

func TestAckAsyncStrictWindowRejectsLocally(t *testing.T) {
	service, transport := newFakeService(ackSuccessResponse)
	result, err := service.Ack(context.Background(), AckRequest{
		CardID:       "card_evt_001",
		EventID:      "evt_0001",
		AckData:      AckData{Mode: AckModeAsync},
		ReceivedAt:   time.Now().Add(-6 * time.Second),
		StrictWindow: true,
	})
	if err == nil {
		t.Fatal("expected ack_expired")
	}
	var failure *DynamicCardError
	if !errors.As(err, &failure) || !failure.IsAckExpired() {
		t.Fatalf("error = %v", err)
	}
	if transport.calls != 0 {
		t.Fatalf("strict window must reject locally: calls = %d", transport.calls)
	}
	if result.ProceedWithBusiness {
		t.Fatal("a rejected async ack must not run the business logic")
	}
}

func TestAckEventTTLRejectsLocally(t *testing.T) {
	service, transport := newFakeService(ackSuccessResponse)
	result, err := service.Ack(context.Background(), AckRequest{
		CardID:     "card_evt_001",
		EventID:    "evt_0001",
		AckData:    syncAckData(),
		ReceivedAt: time.Now().Add(-11 * time.Minute),
	})
	if err == nil {
		t.Fatal("expected ack_expired")
	}
	var failure *DynamicCardError
	if !errors.As(err, &failure) || !failure.IsAckExpired() {
		t.Fatalf("error = %v", err)
	}
	if transport.calls != 0 {
		t.Fatalf("an expired event must not be sent: calls = %d", transport.calls)
	}
	if result.CardID != "card_evt_001" || result.EventID != "evt_0001" {
		t.Fatalf("result should still identify the event: %+v", result)
	}
}

func TestAckErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		response string
		status   int
		assert   func(*testing.T, *DynamicCardError)
	}{
		{
			name:     "1001 validation",
			response: ackFlatFailure("1001", "参数错误"),
			assert: func(t *testing.T, failure *DynamicCardError) {
				if !failure.IsValidation() {
					t.Fatalf("category = %q", failure.Category)
				}
				if failure.Code != int64(1001) {
					t.Fatalf("code = %v", failure.Code)
				}
				if failure.Message != "参数错误" {
					t.Fatalf("message = %q", failure.Message)
				}
			},
		},
		{
			name:     "1010 stream state",
			response: ackFlatFailure("1010", "card is streaming"),
			assert: func(t *testing.T, failure *DynamicCardError) {
				if !failure.IsStreamState() {
					t.Fatalf("category = %q", failure.Category)
				}
				if failure.CurrentSequence != nil || failure.CurrentContent != nil {
					t.Fatalf("ack 1010 must not carry current_*: %+v", failure)
				}
				if strings.Contains(failure.Message, "请先 finish") {
					t.Fatalf("ack 1010 must not reuse the update hint: %q", failure.Message)
				}
			},
		},
		{
			name: "1011 timeout",
			response: ackFlatFailure("1011",
				"card event ack window closed: ack timeout"),
			assert: func(t *testing.T, failure *DynamicCardError) {
				if !failure.IsAckExpired() {
					t.Fatalf("category = %q", failure.Category)
				}
				if failure.Message != "card event ack window closed: ack timeout" {
					t.Fatalf("message = %q", failure.Message)
				}
			},
		},
		{
			name: "1011 event record absent",
			response: ackFlatFailure("1011",
				"card event ack window closed: event record absent: unknown event_id or later than record ttl"),
			assert: func(t *testing.T, failure *DynamicCardError) {
				if !failure.IsAckExpired() {
					t.Fatalf("category = %q", failure.Category)
				}
				if !strings.Contains(failure.Message, "event record absent") {
					t.Fatalf("message = %q", failure.Message)
				}
			},
		},
		{
			name:     "3003 ownership",
			response: ackFlatFailure("3003", "card not owned"),
			assert: func(t *testing.T, failure *DynamicCardError) {
				if !failure.IsOwnership() {
					t.Fatalf("category = %q", failure.Category)
				}
			},
		},
		{
			name:     "9001 server",
			response: ackFlatFailure("9001", "internal error"),
			assert: func(t *testing.T, failure *DynamicCardError) {
				if !failure.IsServer() {
					t.Fatalf("category = %q", failure.Category)
				}
			},
		},
		{
			name:     "401 auth",
			response: `{"code": 401, "message": "invalid token"}`,
			status:   401,
			assert: func(t *testing.T, failure *DynamicCardError) {
				if !failure.IsAuth() {
					t.Fatalf("category = %q", failure.Category)
				}
			},
		},
		{
			name:     "gateway string code",
			response: `{"code": "request error", "message": "gateway rejected"}`,
			assert: func(t *testing.T, failure *DynamicCardError) {
				if !failure.IsGateway() {
					t.Fatalf("category = %q", failure.Category)
				}
				if failure.Code != "request error" {
					t.Fatalf("code = %v", failure.Code)
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, transport := newFakeService(testCase.response)
			transport.status = testCase.status
			result, err := service.Ack(context.Background(), AckRequest{
				CardID:  "card_evt_001",
				EventID: "evt_0001",
				AckData: AckData{Mode: AckModeAsync},
			})
			if err == nil {
				t.Fatal("expected a failure")
			}
			var failure *DynamicCardError
			if !errors.As(err, &failure) {
				t.Fatalf("error type = %T", err)
			}
			if failure.Operation != OperationAck {
				t.Fatalf("operation = %q", failure.Operation)
			}
			if failure.CardID != "card_evt_001" || failure.EventID != "evt_0001" {
				t.Fatalf("failure identity = %+v", failure)
			}
			testCase.assert(t, failure)
			if transport.calls != 1 {
				t.Fatalf("ack must not retry: calls = %d", transport.calls)
			}
			if result.Accepted || result.ProceedWithBusiness {
				t.Fatalf("result = %+v", result)
			}
			if result.Mode != AckModeAsync {
				t.Fatalf("mode = %q", result.Mode)
			}
			if len(result.RawJSON()) == 0 {
				t.Fatal("failure must retain the raw response")
			}
			if result.Message == "" {
				t.Fatal("failure must keep the platform message")
			}
		})
	}
}

func TestAckAlreadyProcessedIsSuccess(t *testing.T) {
	service, transport := newFakeService(ackDuplicateResponse)
	result, err := service.Ack(context.Background(), AckRequest{
		CardID:  "card_evt_001",
		EventID: "evt_0001",
		AckData: syncAckData(),
	})
	if err != nil {
		t.Fatalf("duplicate ack must not fail: %v", err)
	}
	if transport.calls != 1 {
		t.Fatalf("calls = %d", transport.calls)
	}
	if !result.Accepted || !result.AlreadyProcessed {
		t.Fatalf("result = %+v", result)
	}
	if result.Sequence != nil {
		t.Fatalf("a duplicate ack carries no applied update: %v", result.Sequence)
	}
	if result.Message != "ack already processed" {
		t.Fatalf("message = %q", result.Message)
	}
}

func TestAckAsyncAlreadyProcessedProceeds(t *testing.T) {
	service, _ := newFakeService(ackDuplicateResponse)
	result, err := service.Ack(context.Background(), AckRequest{
		CardID:  "card_evt_001",
		EventID: "evt_0001",
		AckData: AckData{Mode: AckModeAsync},
	})
	if err != nil {
		t.Fatalf("stub: %v", err)
	}
	if !result.AlreadyProcessed || !result.ProceedWithBusiness {
		t.Fatalf("result = %+v", result)
	}
}

func TestAckEventHelper(t *testing.T) {
	service, transport := newFakeService(ackSuccessResponse)
	event := CardEvent{
		EventID:         "evt_0001",
		CardID:          "card_evt_001",
		CurrentSequence: 9,
		CurrentContent:  NewContent(NewMarkdown("desc", "平台当前内容")),
	}
	result, err := service.AckEvent(context.Background(), event, AckData{
		Mode:  AckModeSync,
		Toast: &AckToast{Level: AckToastLevelInfo, Content: "收到"},
	})
	if err != nil {
		t.Fatalf("ack event: %v", err)
	}
	if transport.route != dcinternal.RouteCardEventAck {
		t.Fatalf("route = %q", transport.route)
	}
	payload := decodePayload(t, transport.payload)
	if payload["card_id"] != "card_evt_001" || payload["event_id"] != "evt_0001" {
		t.Fatalf("payload = %v", payload)
	}
	data := objectAt(t, payload, "ack_data")
	if data["mode"] != string(AckModeSync) {
		t.Fatalf("ack_data = %v", data)
	}
	if _, exists := data["card_update"]; exists {
		t.Fatalf("AckEvent must not derive a card_update from the event: %v", data)
	}
	if result.CardID != "card_evt_001" || result.EventID != "evt_0001" {
		t.Fatalf("result = %+v", result)
	}
}

func TestAckWithoutSequenceIsAccepted(t *testing.T) {
	service, _ := newFakeService(`{
		"code": "ok",
		"data": {"code": 0, "card_id": "card_evt_001", "event_id": "evt_0001", "message": "ok"}
	}`)
	result, err := service.Ack(context.Background(), AckRequest{
		CardID:  "card_evt_001",
		EventID: "evt_0001",
		AckData: syncAckData(),
	})
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	if result.Sequence != nil {
		t.Fatalf("sequence = %v", result.Sequence)
	}
	if !result.Accepted {
		t.Fatalf("result = %+v", result)
	}
}

func TestAckInvalidResponseSequence(t *testing.T) {
	service, _ := newFakeService(`{
		"code": "ok",
		"data": {"code": 0, "sequence": "abc", "message": "ok"}
	}`)
	_, err := service.Ack(context.Background(), AckRequest{
		CardID:  "card_evt_001",
		EventID: "evt_0001",
		AckData: syncAckData(),
	})
	var failure *DynamicCardError
	if !errors.As(err, &failure) || !failure.IsServer() {
		t.Fatalf("error = %v", err)
	}
}

func TestAckMissingPayload(t *testing.T) {
	service, _ := newFakeService(`{"code": "ok"}`)
	_, err := service.Ack(context.Background(), AckRequest{
		CardID:  "card_evt_001",
		EventID: "evt_0001",
		AckData: syncAckData(),
	})
	var failure *DynamicCardError
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v", err)
	}
	if !failure.IsUnknown() && !failure.IsServer() {
		t.Fatalf("category = %q", failure.Category)
	}
}

func TestAckNilContext(t *testing.T) {
	service, transport := newFakeService(ackSuccessResponse)
	//nolint:staticcheck // a nil context is rejected before any request.
	_, err := service.Ack(nil, AckRequest{
		CardID:  "card_evt_001",
		EventID: "evt_0001",
		AckData: syncAckData(),
	})
	var failure *DynamicCardError
	if !errors.As(err, &failure) || !failure.IsValidation() {
		t.Fatalf("error = %v", err)
	}
	if transport.calls != 0 {
		t.Fatalf("calls = %d", transport.calls)
	}
}
