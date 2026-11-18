package im

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/testkit"
)

func bubbleWorkflowCardFixture() BubbleWorkflowCardRenderOptions {
	return BubbleWorkflowCardRenderOptions{
		CardID: "task-123-confirm",
		Caller: BubbleWorkflowCardCaller{
			CallerName: "sdk-test",
			RequestID:  "request-123",
		},
		Question: "请补充任务信息",
		Receiver: BubbleWorkflowCardReceiver{
			Type: BubbleWorkflowCardReceiverGroup,
			ID:   "13119627",
		},
		Card: BubbleWorkflowCardDefinition{
			Title: "请补充任务信息",
			Components: []BubbleWorkflowCardComponent{
				{ComponentID: "intro", Type: BubbleWorkflowCardComponentText, Text: "请选择研发方向。"},
				{ComponentID: "direction", Type: BubbleWorkflowCardComponentMultiSelect, Options: []BubbleWorkflowCardOption{{Value: "前端"}, {Value: "后端"}}},
			},
			Buttons: []BubbleWorkflowCardButton{{EventKey: "submit", Text: "提交"}},
		},
	}
}

func TestRenderBubbleWorkflowCardRequestShape(t *testing.T) {
	service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/robot/card/bubble-workflow-card/render" {
			t.Fatalf("route = %s", r.URL.Path)
		}
		testkit.AssertJSONEqual(t, testkit.ReadRequestBody(t, r), `{
			"card_id": "task-123-confirm",
			"card_type": "general_form",
			"caller": {"caller_name": "sdk-test", "request_id": "request-123"},
			"question": "请补充任务信息",
			"receiver": {"type": "group", "id": "13119627"},
			"card": {
				"title": "请补充任务信息",
				"components": [
					{"component_id": "intro", "type": "text", "text": "请选择研发方向。"},
					{"component_id": "direction", "type": "multi-select", "options": [{"value": "前端"}, {"value": "后端"}]}
				],
				"buttons": [{"event_key": "submit", "text": "提交"}]
			}
		}`)
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"errmsg":"ok","data":{"ok":true,"created":true,"caller":{"caller_name":"sdk-test","request_id":"request-123"},"receiver":{"type":"group","id":"13119627"},"card_info":{"card_id":"task-123-confirm","card_type":"general_form","revision":1},"recognized":true,"updated":true}}}`)
	})

	result, err := service.RenderBubbleWorkflowCard(context.Background(), bubbleWorkflowCardFixture())
	if err != nil {
		t.Fatalf("RenderBubbleWorkflowCard() error = %v", err)
	}
	if !result.OK || !result.Created || !result.Recognized || !result.Updated {
		t.Fatalf("result flags = %#v", result)
	}
	if result.Caller == nil || result.Caller.CallerName != "sdk-test" || result.Caller.RequestID != "request-123" {
		t.Fatalf("result.Caller = %#v", result.Caller)
	}
	if result.Receiver == nil || result.Receiver.Type != BubbleWorkflowCardReceiverGroup || result.Receiver.ID != "13119627" {
		t.Fatalf("result.Receiver = %#v", result.Receiver)
	}
	if result.CardInfo == nil || result.CardInfo.CardID != "task-123-confirm" || result.CardInfo.CardType != "general_form" || result.CardInfo.Revision == nil || *result.CardInfo.Revision != 1 {
		t.Fatalf("result.CardInfo = %#v", result.CardInfo)
	}
	assertFreshRaw(t, result.RawJSON, result.raw)
}

func TestRenderBubbleWorkflowCardOptionalAndRefreshState(t *testing.T) {
	service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
		testkit.AssertJSONEqual(t, testkit.ReadRequestBody(t, r), `{
			"card_id": "approval-42",
			"card_type": "general_form",
			"card_instance_id": "inst-7",
			"caller": {"caller_name": "workflow-bot"},
			"receiver": {"type": "user", "id": "zhangsan"},
			"card": {"components": [{"component_id": "reason", "type": "input", "placeholder_text": "请输入审批意见", "description": "desc"}]},
			"refresh_default_intermediate_state": true
		}`)
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"data":{"ok":true,"created":false,"recognized":true,"updated":true}}}`)
	})

	refresh := true
	options := BubbleWorkflowCardRenderOptions{
		CardID:                          "approval-42",
		CardInstanceID:                  "inst-7",
		Caller:                          BubbleWorkflowCardCaller{CallerName: "workflow-bot"},
		Receiver:                        BubbleWorkflowCardReceiver{Type: BubbleWorkflowCardReceiverUser, ID: "zhangsan"},
		Card:                            BubbleWorkflowCardDefinition{Components: []BubbleWorkflowCardComponent{{ComponentID: "reason", Type: BubbleWorkflowCardComponentInput, PlaceholderText: "请输入审批意见", Description: "desc"}}},
		RefreshDefaultIntermediateState: &refresh,
	}
	result, err := service.RenderBubbleWorkflowCard(context.Background(), options)
	if err != nil {
		t.Fatalf("RenderBubbleWorkflowCard() error = %v", err)
	}
	if result.Created {
		t.Fatal("created = true, want false refresh")
	}
}

func TestRenderBubbleWorkflowCardComponentMapping(t *testing.T) {
	service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
		testkit.AssertJSONEqual(t, testkit.ReadRequestBody(t, r), `{
			"card_id": "c",
			"card_type": "general_form",
			"caller": {"caller_name": "bot"},
			"receiver": {"type": "group", "id": "1"},
			"card": {"components": [
				{"component_id": "a", "type": "text", "text": "静态"},
				{"component_id": "b", "type": "dropdown-single", "placeholder_text": "请选择优先级", "options": [{"value": "高"}]},
				{"component_id": "c", "type": "single-select", "options": [{"value": "x"}]}
			]}
		}`)
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"data":{"ok":true,"created":true,"recognized":true,"updated":true}}}`)
	})

	_, err := service.RenderBubbleWorkflowCard(context.Background(), BubbleWorkflowCardRenderOptions{
		CardID:   "c",
		Caller:   BubbleWorkflowCardCaller{CallerName: "bot"},
		Receiver: BubbleWorkflowCardReceiver{Type: BubbleWorkflowCardReceiverGroup, ID: "1"},
		Card: BubbleWorkflowCardDefinition{Components: []BubbleWorkflowCardComponent{
			{ComponentID: "a", Type: BubbleWorkflowCardComponentText, Text: "静态"},
			{ComponentID: "b", Type: BubbleWorkflowCardComponentDropdownSingle, PlaceholderText: "请选择优先级", Options: []BubbleWorkflowCardOption{{Value: "高"}}},
			{ComponentID: "c", Type: BubbleWorkflowCardComponentSingleSelect, Options: []BubbleWorkflowCardOption{{Value: "x"}}},
		}},
	})
	if err != nil {
		t.Fatalf("RenderBubbleWorkflowCard() error = %v", err)
	}
}

func TestRenderBubbleWorkflowCardValidation(t *testing.T) {
	service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected network request to %s", r.URL.Path)
	})
	base := bubbleWorkflowCardFixture()
	tooManyComponents := make([]BubbleWorkflowCardComponent, 9)
	for i := range tooManyComponents {
		tooManyComponents[i] = BubbleWorkflowCardComponent{ComponentID: "c" + string(rune('a'+i)), Type: BubbleWorkflowCardComponentInput}
	}
	tooManyButtons := make([]BubbleWorkflowCardButton, 7)
	for i := range tooManyButtons {
		tooManyButtons[i] = BubbleWorkflowCardButton{EventKey: "e" + string(rune('a'+i)), Text: "t"}
	}
	options := func(mutate func(*BubbleWorkflowCardRenderOptions)) BubbleWorkflowCardRenderOptions {
		clone := base
		clone.Card.Components = append([]BubbleWorkflowCardComponent(nil), base.Card.Components...)
		clone.Card.Buttons = append([]BubbleWorkflowCardButton(nil), base.Card.Buttons...)
		mutate(&clone)
		return clone
	}
	cases := []func() error{
		func() error {
			o := options(func(o *BubbleWorkflowCardRenderOptions) { o.CardID = " " })
			_, e := service.RenderBubbleWorkflowCard(context.Background(), o)
			return e
		},
		func() error {
			o := options(func(o *BubbleWorkflowCardRenderOptions) { o.Caller.CallerName = "" })
			_, e := service.RenderBubbleWorkflowCard(context.Background(), o)
			return e
		},
		func() error {
			o := options(func(o *BubbleWorkflowCardRenderOptions) { o.Receiver.Type = BubbleWorkflowCardReceiverType("channel") })
			_, e := service.RenderBubbleWorkflowCard(context.Background(), o)
			return e
		},
		func() error {
			o := options(func(o *BubbleWorkflowCardRenderOptions) { o.Receiver.ID = " " })
			_, e := service.RenderBubbleWorkflowCard(context.Background(), o)
			return e
		},
		func() error {
			o := options(func(o *BubbleWorkflowCardRenderOptions) { o.CardInstanceID = " " })
			_, e := service.RenderBubbleWorkflowCard(context.Background(), o)
			return e
		},
		func() error {
			o := options(func(o *BubbleWorkflowCardRenderOptions) { o.Card.Components = tooManyComponents })
			_, e := service.RenderBubbleWorkflowCard(context.Background(), o)
			return e
		},
		func() error {
			o := options(func(o *BubbleWorkflowCardRenderOptions) { o.Card.Buttons = tooManyButtons })
			_, e := service.RenderBubbleWorkflowCard(context.Background(), o)
			return e
		},
		func() error {
			o := options(func(o *BubbleWorkflowCardRenderOptions) {
				o.Card.Components = append(o.Card.Components, BubbleWorkflowCardComponent{ComponentID: "intro", Type: BubbleWorkflowCardComponentInput})
			})
			_, e := service.RenderBubbleWorkflowCard(context.Background(), o)
			return e
		},
		func() error {
			o := options(func(o *BubbleWorkflowCardRenderOptions) {
				o.Card.Components = []BubbleWorkflowCardComponent{{ComponentID: "a", Type: BubbleWorkflowCardComponentText}}
			})
			_, e := service.RenderBubbleWorkflowCard(context.Background(), o)
			return e
		},
		func() error {
			o := options(func(o *BubbleWorkflowCardRenderOptions) {
				o.Card.Components = []BubbleWorkflowCardComponent{{ComponentID: "a", Type: BubbleWorkflowCardComponentMultiSelect}}
			})
			_, e := service.RenderBubbleWorkflowCard(context.Background(), o)
			return e
		},
		func() error {
			o := options(func(o *BubbleWorkflowCardRenderOptions) {
				o.Card.Components = []BubbleWorkflowCardComponent{{ComponentID: "a", Type: BubbleWorkflowCardComponentMultiSelect, Options: []BubbleWorkflowCardOption{{Value: " "}}}}
			})
			_, e := service.RenderBubbleWorkflowCard(context.Background(), o)
			return e
		},
		func() error {
			o := options(func(o *BubbleWorkflowCardRenderOptions) {
				o.Card.Buttons = []BubbleWorkflowCardButton{{EventKey: " ", Text: "提交"}}
			})
			_, e := service.RenderBubbleWorkflowCard(context.Background(), o)
			return e
		},
	}
	for _, call := range cases {
		assertConfigError(t, call())
	}
}

func TestRenderBubbleWorkflowCardResponseErrors(t *testing.T) {
	t.Run("nonzero business errcode becomes BusinessError", func(t *testing.T) {
		service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
			testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":40001,"errmsg":"invalid card"}}`)
		})
		_, err := service.RenderBubbleWorkflowCard(context.Background(), bubbleWorkflowCardFixture())
		var target *sdkerrors.BusinessError
		if err == nil || !errors.As(err, &target) {
			t.Fatalf("error = %T %v, want *BusinessError", err, err)
		}
	})
	t.Run("missing ok flag becomes MalformedResponseError", func(t *testing.T) {
		service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
			testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"data":{"created":true,"recognized":true,"updated":true}}}`)
		})
		_, err := service.RenderBubbleWorkflowCard(context.Background(), bubbleWorkflowCardFixture())
		var target *sdkerrors.MalformedResponseError
		if err == nil || !errors.As(err, &target) {
			t.Fatalf("error = %T %v, want *MalformedResponseError", err, err)
		}
	})
	t.Run("missing created flag becomes MalformedResponseError", func(t *testing.T) {
		service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
			testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"data":{"ok":true,"recognized":true,"updated":true}}}`)
		})
		_, err := service.RenderBubbleWorkflowCard(context.Background(), bubbleWorkflowCardFixture())
		var target *sdkerrors.MalformedResponseError
		if err == nil || !errors.As(err, &target) {
			t.Fatalf("error = %T %v, want *MalformedResponseError", err, err)
		}
	})
	t.Run("invalid JSON becomes MalformedResponseError", func(t *testing.T) {
		service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
			testkit.WriteJSON(w, http.StatusOK, `not-json`)
		})
		_, err := service.RenderBubbleWorkflowCard(context.Background(), bubbleWorkflowCardFixture())
		var target *sdkerrors.MalformedResponseError
		if err == nil || !errors.As(err, &target) {
			t.Fatalf("error = %T %v, want *MalformedResponseError", err, err)
		}
	})
}

func assertConfigError(t *testing.T, err error) {
	t.Helper()
	var target *sdkerrors.ConfigError
	if err == nil || !errors.As(err, &target) {
		t.Fatalf("error = %T %v, want *ConfigError", err, err)
	}
}

func assertFreshRaw(t *testing.T, accessor func() json.RawMessage, stored json.RawMessage) {
	t.Helper()
	got := accessor()
	if len(got) != len(stored) {
		t.Fatalf("raw length = %d, stored %d", len(got), len(stored))
	}
	for i := range got {
		if got[i] != stored[i] {
			t.Fatalf("raw is not a defensive copy at byte %d", i)
		}
	}
	got[0] ^= 0xff
	if len(stored) > 0 && got[0] == stored[0] {
		t.Fatal("raw bytes are shared with the stored buffer")
	}
}
