package im

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/dolfly/infoflow/internal/sdkerrors"
	"github.com/dolfly/infoflow/internal/testkit"
)

func TestStreamingCardSessionUserLifecycle(t *testing.T) {
	var createBody, updateBody []byte
	service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/msg/sender/interactivity_msg":
			createBody = testkit.ReadRequestBody(t, r)
			testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"modify_token":"user-token"}}`)
		case "/api/v1/msg/modifier/interactivity_personal_msg_content":
			updateBody = testkit.ReadRequestBody(t, r)
			testkit.WriteJSON(w, http.StatusOK, `{"code":"ok"}`)
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	})

	session, err := service.NewStreamingCardSession(StreamingCardSessionOptions{
		Target: StreamingCardTarget{Type: StreamingCardTargetUser, ID: "zhangsan"},
	})
	if err != nil {
		t.Fatalf("NewStreamingCardSession() error = %v", err)
	}
	if err := session.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if session.State().ModifyToken != "user-token" {
		t.Fatalf("ModifyToken = %q", session.State().ModifyToken)
	}
	testkit.AssertJSONEqual(t, createBody, `{
		"contents": {
			"card_init": {"type":"text","content":"1"},
			"answer_summary": {"type":"text","content":"收到啦，正在处理中～"},
			"ai_markdown": {"type":"text","content":""},
			"status_info": {"type":"text","content":"收到啦，正在处理中～"},
			"thinking_aio": {"type":"text","content":""},
			"think_star_img": {"type":"text","content":"ast/think_star_static.png"},
			"think_status_img": {"type":"text","content":"ast/thinking_yes.png"},
			"think_status_color": {"type":"text","content":"#5C6473"},
			"think_status_text": {"type":"text","content":"收到啦，正在处理中～"},
			"think_arrow_img": {"type":"text","content":"ast/arrow_down.png"},
			"think_layout_install": {"type":"text","content":"1"},
			"status_info_1_install": {"type":"text","content":"0"},
			"flex_item_status_info_1_install": {"type":"text","content":"0"},
			"dc_print_end": {"type":"text","content":"1"}
		},
		"meta": {
			"client_msg_id": 1700000000000,
			"client_send_time": 1700000000000,
			"interactivity_expire": 7776000,
			"interactivity_mode": "normal",
			"modify_expire": 7776000,
			"offline_notify_txt": "你收到一条卡片消息",
			"template": {"name":"streaming_render","version":30}
		},
		"type": 1,
		"content_id": "1700000000000",
		"receiver_id": "zhangsan",
		"receiver_type": "user",
		"scene": "IM-server",
		"user_msg": false
	}`)

	if err := session.AppendText(context.Background(), "Hello "); err != nil {
		t.Fatalf("AppendText() error = %v", err)
	}
	testkit.AssertJSONEqual(t, updateBody, `{
		"modify_token": "user-token",
		"new_personal_msg_content": [{
			"user_ids": ["zhangsan"],
			"personal_msg_content": {
				"ai_markdown": {"type":"text","content":"Hello "},
				"thinking_aio": {"type":"text","content":""},
				"status_info": {"type":"text","content":"思考中"},
				"think_status_text": {"type":"text","content":"思考中"},
				"dc_print_end": {"type":"text","content":"0"}
			},
			"notify_msg_content": null
		}]
	}`)

	if err := session.Complete(context.Background(), "分析完成"); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	testkit.AssertJSONEqual(t, updateBody, `{
		"modify_token": "user-token",
		"new_personal_msg_content": [{
			"user_ids": ["zhangsan"],
			"personal_msg_content": {
				"ai_markdown": {"type":"text","content":"Hello "},
				"thinking_aio": {"type":"text","content":""},
				"status_info": {"type":"text","content":"分析完成"},
				"think_status_text": {"type":"text","content":"分析完成"},
				"dc_print_end": {"type":"text","content":"1"}
			},
			"notify_msg_content": null
		}]
	}`)
	if !session.State().Done {
		t.Fatal("Done = false, want true after Complete")
	}
	if err := session.AppendText(context.Background(), "ignored"); err == nil {
		t.Fatal("AppendText() after Complete error = nil, want config error")
	}
}

func TestStreamingCardSessionGroupLifecycleAndFail(t *testing.T) {
	var createBody, updateBody []byte
	var updateCount int
	service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/msg/sender/interactivity_msg":
			createBody = testkit.ReadRequestBody(t, r)
			testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"receivers":[{"modify_token":"group-token"}]}}`)
		case "/api/v1/msg/modifier/dynamic_content":
			updateCount++
			updateBody = testkit.ReadRequestBody(t, r)
			testkit.WriteJSON(w, http.StatusOK, `{"code":"ok"}`)
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	})

	session, err := service.NewStreamingCardSession(StreamingCardSessionOptions{
		Target:      StreamingCardTarget{Type: StreamingCardTargetGroup, ID: "12345"},
		InitialText: "开始处理",
	})
	if err != nil {
		t.Fatalf("NewStreamingCardSession() error = %v", err)
	}
	if err := session.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	testkit.AssertJSONEqual(t, createBody, `{
		"contents": {
			"card_init": {"type":"text","content":"1"},
			"answer_summary": {"type":"text","content":"收到啦，正在处理中～"},
			"ai_markdown": {"type":"text","content":""},
			"status_info": {"type":"text","content":"收到啦，正在处理中～"},
			"thinking_aio": {"type":"text","content":""},
			"think_star_img": {"type":"text","content":"ast/think_star_static.png"},
			"think_status_img": {"type":"text","content":"ast/thinking_yes.png"},
			"think_status_color": {"type":"text","content":"#5C6473"},
			"think_status_text": {"type":"text","content":"收到啦，正在处理中～"},
			"think_arrow_img": {"type":"text","content":"ast/arrow_down.png"},
			"think_layout_install": {"type":"text","content":"1"},
			"status_info_1_install": {"type":"text","content":"0"},
			"flex_item_status_info_1_install": {"type":"text","content":"0"},
			"dc_print_end": {"type":"text","content":"1"}
		},
		"meta": {
			"client_msg_id": 1700000000000,
			"client_send_time": 1700000000000,
			"interactivity_expire": 7776000,
			"interactivity_mode": "normal",
			"modify_expire": 7776000,
			"offline_notify_txt": "你收到一条卡片消息",
			"template": {"name":"streaming_render","version":30}
		},
		"type": 1,
		"content_id": "1700000000000",
		"receiver_id": "12345",
		"receiver_type": "group",
		"scene": "IM-server",
		"user_msg": false
	}`)

	if err := session.AppendText(context.Background(), "世界"); err != nil {
		t.Fatalf("AppendText() error = %v", err)
	}
	if updateCount != 1 {
		t.Fatalf("update count = %d, want 1", updateCount)
	}
	testkit.AssertJSONEqual(t, updateBody, `{
		"modify_token": "group-token",
		"new_dynamic_msg_content": {
			"ai_markdown": {"type":"text","content":"开始处理世界"},
			"thinking_aio": {"type":"text","content":""},
			"status_info": {"type":"text","content":"思考中"},
			"think_status_text": {"type":"text","content":"思考中"},
			"dc_print_end": {"type":"text","content":"0"}
		},
		"version": 111,
		"notify_list": [{"to_type":2,"to_ids":["12345"]}]
	}`)

	if err := session.Fail(context.Background(), "服务暂时不可用"); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if updateCount != 2 {
		t.Fatalf("update count = %d, want 2", updateCount)
	}
	testkit.AssertJSONEqual(t, updateBody, `{
		"modify_token": "group-token",
		"new_dynamic_msg_content": {
			"ai_markdown": {"type":"text","content":"开始处理世界"},
			"thinking_aio": {"type":"text","content":""},
			"status_info": {"type":"text","content":"服务暂时不可用"},
			"think_status_color": {"type":"text","content":"#D03050"},
			"think_status_text": {"type":"text","content":"生成失败"},
			"dc_print_end": {"type":"text","content":"1"}
		},
		"version": 112,
		"notify_list": [{"to_type":2,"to_ids":["12345"]}]
	}`)
	state := session.State()
	if !state.Done || !state.Failed {
		t.Fatalf("state = %#v, want done and failed", state)
	}
}

func TestStreamingCardSessionValidation(t *testing.T) {
	service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/msg/sender/interactivity_msg" {
			testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{}}`)
			return
		}
		t.Fatalf("unexpected network request to %s", r.URL.Path)
	})
	if _, err := service.NewStreamingCardSession(StreamingCardSessionOptions{
		Target: StreamingCardTarget{Type: StreamingCardTargetType("channel"), ID: "x"},
	}); err == nil {
		t.Fatal("NewStreamingCardSession() invalid target error = nil")
	}
	if _, err := service.NewStreamingCardSession(StreamingCardSessionOptions{
		Target: StreamingCardTarget{Type: StreamingCardTargetUser, ID: ""},
	}); err == nil {
		t.Fatal("NewStreamingCardSession() blank target error = nil")
	}
	if _, err := service.NewStreamingCardSession(StreamingCardSessionOptions{
		Target:       StreamingCardTarget{Type: StreamingCardTargetUser, ID: "u"},
		AnswerFormat: "yaml",
	}); err == nil {
		t.Fatal("NewStreamingCardSession() invalid answer format error = nil")
	}

	session, err := service.NewStreamingCardSession(StreamingCardSessionOptions{
		Target: StreamingCardTarget{Type: StreamingCardTargetUser, ID: "u"},
	})
	if err != nil {
		t.Fatalf("NewStreamingCardSession() error = %v", err)
	}
	if err := session.AppendText(context.Background(), "before start"); err == nil {
		t.Fatal("AppendText() before Start error = nil, want config error")
	}
	if err := session.Start(context.Background()); err == nil {
		t.Fatal("Start() without create response error = nil, want malformed error")
	}
}

func TestStreamingCardSessionUpdateRejectsNonOK(t *testing.T) {
	service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/msg/sender/interactivity_msg":
			testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"modify_token":"t"}}`)
		case "/api/v1/msg/modifier/interactivity_personal_msg_content":
			testkit.WriteJSON(w, http.StatusOK, `{"code":"fail","message":"update rejected"}`)
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	})
	session, err := service.NewStreamingCardSession(StreamingCardSessionOptions{
		Target: StreamingCardTarget{Type: StreamingCardTargetUser, ID: "u"},
	})
	if err != nil {
		t.Fatalf("NewStreamingCardSession() error = %v", err)
	}
	if err := session.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	err = session.AppendText(context.Background(), "boom")
	var target *sdkerrors.BusinessError
	if err == nil || !errors.As(err, &target) {
		t.Fatalf("AppendText() error = %T %v, want *BusinessError", err, err)
	}
	if session.State().UpdateFails != 1 {
		t.Fatalf("UpdateFails = %d, want 1", session.State().UpdateFails)
	}
}
