package im

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dolfly/infoflow/internal/auth"
	internalruntime "github.com/dolfly/infoflow/internal/runtime"
	"github.com/dolfly/infoflow/internal/testkit"
	"github.com/dolfly/infoflow/internal/transport"
)

func TestSendGroupMessageLinkAndCommandRequestShapes(t *testing.T) {
	tests := []struct {
		name        string
		messageType string
		body        []GroupBody
		want        string
	}{
		{
			name:        "link",
			messageType: "TEXT",
			body:        []GroupBody{{Type: "LINK", Href: "https://example.test/docs", Label: "Open docs"}},
			want:        `{"message":{"header":{"toid":13222036,"totype":"GROUP","msgtype":"TEXT","clientmsgid":123,"role":"robot"},"body":[{"type":"LINK","href":"https://example.test/docs","label":"Open docs"}]}}`,
		},
		{
			name:        "command",
			messageType: "TEXT",
			body:        []GroupBody{{Type: "CMD", CommandName: "help", TargetAgentID: 2712}},
			want:        `{"message":{"header":{"toid":13222036,"totype":"GROUP","msgtype":"TEXT","clientmsgid":123,"role":"robot"},"body":[{"type":"CMD","commandname":"help","targetAgentId":2712}]}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requestBody []byte
			service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/robot/msg/groupmsgsend" {
					t.Fatalf("route = %s", r.URL.Path)
				}
				requestBody = testkit.ReadRequestBody(t, r)
				testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"data":{"messageid":4,"msgseqid":5,"ctime":6}}}`)
			})

			result, err := service.SendGroupMessage(context.Background(), SendGroupMessageRequest{
				GroupID:         13222036,
				Type:            tt.messageType,
				Body:            tt.body,
				ClientMessageID: 123,
			})
			if err != nil {
				t.Fatalf("SendGroupMessage() error = %v", err)
			}
			if result.MessageID != 4 || result.MessageSequenceID != 5 || result.CreatedAtMillis != 6 {
				t.Fatalf("result = %#v", result)
			}
			testkit.AssertJSONEqual(t, requestBody, tt.want)
		})
	}
}

func TestSendGroupMessageRejectsInvalidLinkAndCommandBodies(t *testing.T) {
	tests := []struct {
		name    string
		request SendGroupMessageRequest
	}{
		{
			name: "link without href",
			request: SendGroupMessageRequest{
				GroupID: 1,
				Type:    "TEXT",
				Body:    []GroupBody{{Type: "LINK", Label: "missing URL"}},
			},
		},
		{
			name: "command without name",
			request: SendGroupMessageRequest{
				GroupID: 1,
				Type:    "TEXT",
				Body:    []GroupBody{{Type: "CMD", TargetAgentID: 2712}},
			},
		},
		{
			name: "command without target",
			request: SendGroupMessageRequest{
				GroupID: 1,
				Type:    "TEXT",
				Body:    []GroupBody{{Type: "CMD", CommandName: "help"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
				t.Fatalf("unexpected network request to %s", r.URL.Path)
			})
			if _, err := service.SendGroupMessage(context.Background(), tt.request); err == nil {
				t.Fatal("SendGroupMessage() error = nil, want validation error")
			}
		})
	}
}

func newMessageOperationsTestService(t *testing.T, handler http.HandlerFunc) *MessageService {
	t.Helper()
	server := testkit.ExtendedServer(handler)
	t.Cleanup(server.Close)
	now := func() time.Time { return time.UnixMilli(1700000000000) }
	client := transport.New(server.URL+"/api/v1", time.Second, time.Second, server.Client(), "sdk")
	tokens := auth.NewTokenManager(client, testkit.AppKey, testkit.Secret, 0, now)
	returnService := NewMessageService(client, tokens, internalruntime.NewMessageIDGenerator(now))
	return &returnService
}

func TestMessageItemWorkCardNormalization(t *testing.T) {
	tests := []struct {
		name       string
		content    json.RawMessage
		wantOK     bool
		wantCardID string
	}{
		{
			name:       "work_card with cardId",
			content:    json.RawMessage(`{"type":"work_card","cardId":"1876020064808906752"}`),
			wantOK:     true,
			wantCardID: "1876020064808906752",
		},
		{
			name:       "work_card missing cardId",
			content:    json.RawMessage(`{"type":"work_card"}`),
			wantOK:     true,
			wantCardID: "",
		},
		{
			name:       "work_card null cardId",
			content:    json.RawMessage(`{"type":"work_card","cardId":null}`),
			wantOK:     true,
			wantCardID: "",
		},
		{
			name:       "work_card non-string cardId",
			content:    json.RawMessage(`{"type":"work_card","cardId":12345}`),
			wantOK:     true,
			wantCardID: "",
		},
		{
			name:    "non-work_card type is not recognized",
			content: json.RawMessage(`{"type":"text","text":"hello"}`),
			wantOK:  false,
		},
		{
			name:    "invalid content json is not recognized",
			content: json.RawMessage(`{`),
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := MessageItem{MessageType: "app_msg", SubType: "work_card", Content: tt.content}
			got, ok := m.WorkCard()
			if ok != tt.wantOK {
				t.Fatalf("WorkCard() ok = %v, want %v (content=%s)", ok, tt.wantOK, tt.content)
			}
			if ok && got.Type != "work_card" {
				t.Fatalf("WorkCard() Type = %q, want %q", got.Type, "work_card")
			}
			if got.CardID != tt.wantCardID {
				t.Fatalf("WorkCard() CardID = %q, want %q", got.CardID, tt.wantCardID)
			}
		})
	}
}

func TestMessageItemWorkCardText(t *testing.T) {
	tests := []struct {
		name    string
		content json.RawMessage
		want    string
	}{
		{
			name:    "with cardId",
			content: json.RawMessage(`{"type":"work_card","cardId":"1876020064808906752"}`),
			want:    "[工作卡片 cardId=1876020064808906752]",
		},
		{
			name:    "missing cardId",
			content: json.RawMessage(`{"type":"work_card"}`),
			want:    "[工作卡片]",
		},
		{
			name:    "non-string cardId",
			content: json.RawMessage(`{"type":"work_card","cardId":12345}`),
			want:    "[工作卡片]",
		},
		{
			name:    "not a work_card",
			content: json.RawMessage(`{"type":"text","text":"hello"}`),
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := MessageItem{MessageType: "app_msg", SubType: "work_card", Content: tt.content}
			if got := m.WorkCardText(); got != tt.want {
				t.Fatalf("WorkCardText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMessageItemWorkCardTextLine(t *testing.T) {
	tests := []struct {
		name string
		item MessageItem
		want string
	}{
		{
			name: "real fixture robot sender with agentId",
			item: MessageItem{
				MessageType: "app_msg",
				SubType:     "work_card",
				MessageID:   "1876020064977800299",
				Sender:      json.RawMessage(`{"userId":4100124985,"agentId":29172}`),
				SendTime:    1789112152846,
				DeletedFlag: 0,
				Content:     json.RawMessage(`{"type":"work_card","cardId":"1876020064808906752"}`),
			},
			want: "[2026-09-11 15:35:52] [msgId:1876020064977800299] bot#29172: [工作卡片 cardId=1876020064808906752]",
		},
		{
			name: "missing cardId falls back to [工作卡片]",
			item: MessageItem{
				MessageType: "app_msg",
				SubType:     "work_card",
				MessageID:   "1876020024558341223",
				Sender:      json.RawMessage(`{"userId":4100124985,"agentId":29172}`),
				SendTime:    1789112114293,
				DeletedFlag: 0,
				Content:     json.RawMessage(`{"type":"work_card"}`),
			},
			want: "[2026-09-11 15:35:14] [msgId:1876020024558341223] bot#29172: [工作卡片]",
		},
		{
			name: "human sender uses uuapId",
			item: MessageItem{
				MessageType: "app_msg",
				SubType:     "work_card",
				MessageID:   "m1",
				Sender:      json.RawMessage(`{"userId":4100124985,"uuapId":"zhangsan"}`),
				SendTime:    1789112152846,
				DeletedFlag: 0,
				Content:     json.RawMessage(`{"type":"work_card","cardId":"c1"}`),
			},
			want: "[2026-09-11 15:35:52] [msgId:m1] zhangsan: [工作卡片 cardId=c1]",
		},
		{
			name: "not a work_card renders no line",
			item: MessageItem{
				MessageType: "text",
				MessageID:   "m1",
				Content:     json.RawMessage(`{"type":"text","text":"hello"}`),
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.item.WorkCardTextLine(); got != tt.want {
				t.Fatalf("WorkCardTextLine() = %q, want %q", got, tt.want)
			}
		})
	}
}
