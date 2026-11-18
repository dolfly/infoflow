package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	infoflow "github.com/dolfly/infoflow"
	botim "github.com/dolfly/infoflow/bot/im"
)

func TestSendUserMessageRequestShapes(t *testing.T) {
	tests := []struct {
		name    string
		request botim.SendUserMessageRequest
		check   func(*testing.T, map[string]any)
	}{
		{"text", botim.SendUserMessageRequest{ToUser: "u", Type: "text", Text: "hello"}, func(t *testing.T, body map[string]any) { assertContent(t, body, "text", "hello") }},
		{"markdown with reply", botim.SendUserMessageRequest{ToUser: "u", Type: "md", Text: "# heading", Reply: []botim.UserReply{{Content: "old", UID: "u", MessageID: "1", MessageID2: "2"}}}, func(t *testing.T, body map[string]any) {
			assertContent(t, body, "md", "# heading")
			if len(body["reply"].([]any)) != 1 {
				t.Fatal("reply was not serialized")
			}
		}},
		{"image", botim.SendUserMessageRequest{ToUser: "u", Type: "image", Image: "base64"}, func(t *testing.T, body map[string]any) { assertContent(t, body, "image", "base64") }},
		{"rich text", botim.SendUserMessageRequest{ToUser: "u", Type: "richtext", RichText: []botim.RichTextNode{{Type: "text", Text: "hello"}, {Type: "a", Href: "https://example.test", Label: "link"}}}, func(t *testing.T, body map[string]any) {
			if len(body["richtext"].(map[string]any)["content"].([]any)) != 2 {
				t.Fatal("rich text nodes were not serialized")
			}
		}},
		{"news", botim.SendUserMessageRequest{ToUser: "u", Type: "news", Articles: []botim.NewsArticle{{Title: "title", URL: "https://example.test"}}}, func(t *testing.T, body map[string]any) {
			if len(body["news"].(map[string]any)["articles"].([]any)) != 1 {
				t.Fatal("news article was not serialized")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/app/message/send" {
					t.Fatalf("route = %s", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["touser"] != "u" || body["msgtype"] != tt.request.Type {
					t.Fatalf("body = %#v", body)
				}
				tt.check(t, body)
				writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"msgkey":4}}`)
			})
			defer server.Close()
			result, err := newFixtureClient(t, server, time.Now).Bot.IM.Message.SendUserMessage(context.Background(), tt.request)
			if err != nil || result.MessageKey != 4 {
				t.Fatalf("result = %#v, err = %v", result, err)
			}
		})
	}
}

func TestSendGroupMessageRequestAndEnvelope(t *testing.T) {
	server := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/robot/msg/groupmsgsend" {
			t.Fatalf("route = %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		message := body["message"].(map[string]any)
		header := message["header"].(map[string]any)
		if header["toid"] != float64(7) || header["totype"] != "GROUP" || header["msgtype"] != "MD" || header["role"] != "robot" {
			t.Fatalf("header = %#v", header)
		}
		if header["clientmsgid"].(float64) <= 0 {
			t.Fatal("client message ID must be generated positively")
		}
		if len(message["body"].([]any)) != 2 || message["reply"].(map[string]any)["messageid"] != "m" {
			t.Fatalf("message = %#v", message)
		}
		writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"data":{"messageid":4,"msgseqid":5,"ctime":6}}}`)
	})
	defer server.Close()
	result, err := newFixtureClient(t, server, time.Now).Bot.IM.Message.SendGroupMessage(context.Background(), botim.SendGroupMessageRequest{GroupID: 7, Type: "md", Body: []botim.GroupBody{{Type: "MD", Content: "hello"}, {Type: "AT", AtUserIDs: []string{"u"}}}, Reply: &botim.GroupReply{MessageID: "m", Preview: "p", IMID: "i"}})
	if err != nil || result.MessageID != 4 || result.MessageSequenceID != 5 || result.CreatedAtMillis != 6 {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

func TestMessageReadRequestsResponsesAndErrors(t *testing.T) {
	t.Run("list preserves polymorphic content and query", func(t *testing.T) {
		server := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if r.URL.Path != "/api/v1/im/message/list" || q.Get("receiverId") != "7" || q.Get("startTime") != "1" || q.Get("endTime") != "2" || q.Get("sortType") != "desc" || q.Get("pageSize") != "20" || q.Get("pageToken") != "next" {
				t.Fatalf("path/query = %s?%s", r.URL.Path, r.URL.RawQuery)
			}
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"code":200,"message":"ok","data":{"hasMore":false,"items":[{"msgType":"text","msgId":"1","content":{"blocks":[{"type":"image","downloadUrl":"temporary"}]}}]}}}`)
		})
		defer server.Close()
		result, err := newFixtureClient(t, server, time.Now).Bot.IM.Message.ListGroupMessages(context.Background(), botim.ListGroupMessagesRequest{ReceiverID: 7, StartTime: 1, EndTime: 2, PageToken: "next"})
		if err != nil || len(result.Items) != 1 || !strings.Contains(string(result.Items[0].Content), "downloadUrl") {
			t.Fatalf("result = %#v, err = %v", result, err)
		}
		assertFreshRaw(t, result.RawJSON, result.RawJSON())
	})
	t.Run("detail includes complete nested-message parameters", func(t *testing.T) {
		server := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			for key, want := range map[string]string{"fromId": "u", "receiverId": "7", "receiverType": "2", "msgId": "8", "innerMsgId": "9", "innerMsgFromId": "10", "innerMsgReceiverId": "11", "innerMsgReceiverType": "2"} {
				if q.Get(key) != want {
					t.Fatalf("%s = %q", key, q.Get(key))
				}
			}
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"code":200,"message":"ok","data":{"items":[{"msgType":"merged-message","msgId":"8","content":{"title":"x"}}]}}}`)
		})
		defer server.Close()
		items, err := newFixtureClient(t, server, time.Now).Bot.IM.Message.GetMessage(context.Background(), botim.GetMessageRequest{FromID: "u", ReceiverID: 7, ReceiverType: 2, MessageID: 8, InnerMessageID: 9, InnerFromID: 10, InnerReceiverID: 11, InnerReceiverType: 2})
		if err != nil || len(items) != 1 {
			t.Fatalf("items = %#v, err = %v", items, err)
		}
	})
	t.Run("non-progressing token is malformed", func(t *testing.T) {
		server := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"code":200,"data":{"hasMore":true,"pageToken":"same"}}}`)
		})
		defer server.Close()
		_, err := newFixtureClient(t, server, time.Now).Bot.IM.Message.ListGroupMessages(context.Background(), botim.ListGroupMessagesRequest{ReceiverID: 1, StartTime: 1, EndTime: 2, PageToken: "same"})
		if err == nil {
			t.Fatal("expected non-progressing page error")
		}
	})
}

func TestMessageManagementRequestEnvelopesAndErrors(t *testing.T) {
	tests := []struct {
		name, path, response string
		call                 func(*botim.MessageService) error
	}{
		{"revoke user", "/api/v1/app/message/revoke", `{"errcode":0}`, func(s *botim.MessageService) error {
			return s.RevokeUserMessage(context.Background(), botim.RevokeUserMessageRequest{MessageKey: "m", AgentID: 1})
		}},
		{"revoke group", "/api/v1/robot/group/msgRecall", `{"errcode":0}`, func(s *botim.MessageService) error {
			return s.RevokeGroupMessage(context.Background(), botim.RevokeGroupMessageRequest{GroupID: 1, MessageID: 2, MessageSequenceID: 3})
		}},
		{"pin", "/api/v1/robot/group/stickOnTop", `{"errcode":0}`, func(s *botim.MessageService) error {
			return s.SetGroupMessagePin(context.Background(), botim.PinGroupMessageRequest{GroupID: 1, MessageID: 2, Operation: 1})
		}},
		{"emoji add", "/api/v1/im/message/emoji/add", `{"code":"ok","data":{"bizCode":200}}`, func(s *botim.MessageService) error { return s.AddEmoji(context.Background(), validEmojiRequest()) }},
		{"emoji delete", "/api/v1/im/message/emoji/del", `{"code":"ok","data":{"bizCode":200}}`, func(s *botim.MessageService) error { return s.DeleteEmoji(context.Background(), validEmojiRequest()) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.path {
					t.Fatalf("route = %s", r.URL.Path)
				}
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if len(body) == 0 {
					t.Fatal("expected request body")
				}
				writeJSON(w, http.StatusOK, tt.response)
			})
			defer server.Close()
			if err := tt.call(&newFixtureClient(t, server, time.Now).Bot.IM.Message); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("business failure is returned", func(t *testing.T) {
		server := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, `{"errcode":40000,"errmsg":"rejected"}`)
		})
		defer server.Close()
		if err := newFixtureClient(t, server, time.Now).Bot.IM.Message.RevokeUserMessage(context.Background(), botim.RevokeUserMessageRequest{MessageKey: "m", AgentID: 1}); err == nil {
			t.Fatal("expected business error")
		}
	})
	// The message-management routes use the standard nested B envelope
	// ({code:"ok", data:{errcode:0}}). The flat A form ({errcode:0}) is also
	// accepted for backwards compatibility. Verify both succeed and that nested
	// business failures are surfaced.
	t.Run("nested B envelope succeeds", func(t *testing.T) {
		nestedTests := []struct {
			name, path string
			call       func(*botim.MessageService) error
		}{
			{"nested B revoke user", "/api/v1/app/message/revoke", func(s *botim.MessageService) error {
				return s.RevokeUserMessage(context.Background(), botim.RevokeUserMessageRequest{MessageKey: "m", AgentID: 1})
			}},
			{"nested B revoke group", "/api/v1/robot/group/msgRecall", func(s *botim.MessageService) error {
				return s.RevokeGroupMessage(context.Background(), botim.RevokeGroupMessageRequest{GroupID: 1, MessageID: 2, MessageSequenceID: 3})
			}},
			{"nested B pin", "/api/v1/robot/group/stickOnTop", func(s *botim.MessageService) error {
				return s.SetGroupMessagePin(context.Background(), botim.PinGroupMessageRequest{GroupID: 1, MessageID: 2, Operation: 1})
			}},
			{"nested B send interaction", "/api/v1/robot/group/sendInteractionMsg", func(s *botim.MessageService) error {
				return s.SendInteractionMessage(context.Background(), botim.InteractionMessageRequest{Header: botim.InteractionHeader{ToType: "group", ToID: 1}, Body: []botim.InteractionBody{{Type: "TEXT", Content: "ok"}}})
			}},
		}
		for _, tt := range nestedTests {
			t.Run(tt.name, func(t *testing.T) {
				server := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != tt.path {
						t.Fatalf("route = %s", r.URL.Path)
					}
					writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"errmsg":"ok"}}`)
				})
				defer server.Close()
				if err := tt.call(&newFixtureClient(t, server, time.Now).Bot.IM.Message); err != nil {
					t.Fatal(err)
				}
			})
		}
	})
	t.Run("nested B business failure is returned", func(t *testing.T) {
		server := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":40000,"errmsg":"rejected"}}`)
		})
		defer server.Close()
		if err := newFixtureClient(t, server, time.Now).Bot.IM.Message.SetGroupMessagePin(context.Background(), botim.PinGroupMessageRequest{GroupID: 1, MessageID: 2, Operation: 1}); err == nil {
			t.Fatal("expected business error")
		}
	})
	t.Run("nested B data.code failure is returned", func(t *testing.T) {
		server := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"code":410,"message":"rejected"}}`)
		})
		defer server.Close()
		if err := newFixtureClient(t, server, time.Now).Bot.IM.Message.RevokeGroupMessage(context.Background(), botim.RevokeGroupMessageRequest{GroupID: 1, MessageID: 2, MessageSequenceID: 3}); err == nil {
			t.Fatal("expected business error")
		}
	})
}

func TestVoiceInteractionAndStreamingProtocols(t *testing.T) {
	t.Run("voice uses documented form and envelope", func(t *testing.T) {
		server := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/va/queryASRResult" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.FormValue("md5") != "hash" {
				t.Fatalf("invalid ASR request")
			}
			writeJSON(w, http.StatusOK, `{"code":"ok","data":{"code":200,"content":"words"}}`)
		})
		defer server.Close()
		result, err := newFixtureClient(t, server, time.Now).Bot.IM.Message.TranscribeVoiceMessage(context.Background(), " hash ")
		if err != nil || result.Content != "words" {
			t.Fatalf("result = %#v, err = %v", result, err)
		}
	})
	t.Run("interaction and raw streaming have required shape", func(t *testing.T) {
		paths := map[string]bool{}
		server := extendedServer(t, func(w http.ResponseWriter, r *http.Request) {
			paths[r.URL.Path] = true
			if strings.HasPrefix(r.URL.Path, "/api/v1/msg/") {
				if r.Header.Get("LOGID") == "" || r.Header.Get("X-OP-ACCESS-CHANNEL") != "go_sdk" {
					t.Fatalf("missing streaming headers: %#v", r.Header)
				}
				if _, err := strconv.ParseInt(r.Header.Get("LOGID"), 10, 64); err != nil {
					t.Fatalf("LOGID = %q", r.Header.Get("LOGID"))
				}
				writeJSON(w, http.StatusOK, `{"observed":"untyped"}`)
				return
			}
			writeJSON(w, http.StatusOK, `{"errcode":0}`)
		})
		defer server.Close()
		client := newFixtureClient(t, server, time.Now)
		if err := client.Bot.IM.Message.SendInteractionMessage(context.Background(), botim.InteractionMessageRequest{Header: botim.InteractionHeader{ToType: "group", ToID: 1}, Body: []botim.InteractionBody{{Type: "TEXT", Content: "ok"}}}); err != nil {
			t.Fatal(err)
		}
		for _, call := range []func(context.Context, json.RawMessage) (json.RawMessage, error){client.Bot.IM.Message.CreateStreamingCard, client.Bot.IM.Message.UpdateUserStreamingCard, client.Bot.IM.Message.UpdateGroupStreamingCard} {
			raw, err := call(context.Background(), json.RawMessage(`{"raw":true}`))
			if err != nil || string(raw) != `{"observed":"untyped"}` {
				t.Fatalf("raw = %s, err = %v", raw, err)
			}
		}
		if len(paths) != 4 {
			t.Fatalf("routes = %#v", paths)
		}
	})
}

func TestExtendedMessageValidation(t *testing.T) {
	client, err := infoflow.NewClient(infoflow.Config{AppKey: fixtureAppKey, AppSecret: fixtureSecret, HTTPClient: &http.Client{Transport: &countingRoundTripper{}}})
	if err != nil {
		t.Fatal(err)
	}
	tooManyUsers := make([]string, 51)
	tooManyImages := make([]botim.InteractionBody, 6)
	for i := range tooManyImages {
		tooManyImages[i] = botim.InteractionBody{Type: "IMAGE", Content: "x"}
	}
	cases := []func() error{
		func() error {
			_, e := client.Bot.IM.Message.SendUserMessage(context.Background(), botim.SendUserMessageRequest{ToUser: "u", Type: "file"})
			return e
		},
		func() error {
			_, e := client.Bot.IM.Message.SendUserMessage(context.Background(), botim.SendUserMessageRequest{ToUser: "u", Type: "richtext", RichText: []botim.RichTextNode{{Type: "a", Href: "x"}}})
			return e
		},
		func() error {
			_, e := client.Bot.IM.Message.SendUserMessage(context.Background(), botim.SendUserMessageRequest{ToUser: "u", Type: "news", Articles: []botim.NewsArticle{{Title: ""}}})
			return e
		},
		func() error {
			_, e := client.Bot.IM.Message.SendGroupMessage(context.Background(), botim.SendGroupMessageRequest{GroupID: 1, Type: "TEXT", ClientMessageID: -1, Body: []botim.GroupBody{{Type: "TEXT", Content: "x"}}})
			return e
		},
		func() error {
			_, e := client.Bot.IM.Message.SendGroupMessage(context.Background(), botim.SendGroupMessageRequest{GroupID: 1, Type: "TEXT", Body: []botim.GroupBody{{Type: "AT", AtUserIDs: tooManyUsers}}})
			return e
		},
		func() error {
			_, e := client.Bot.IM.Message.ListGroupMessages(context.Background(), botim.ListGroupMessagesRequest{ReceiverID: 1, StartTime: 2, EndTime: 1})
			return e
		},
		func() error {
			_, e := client.Bot.IM.Message.GetMessage(context.Background(), botim.GetMessageRequest{FromID: "u", ReceiverID: 1, ReceiverType: 2, MessageID: 1, InnerMessageID: 1})
			return e
		},
		func() error {
			return client.Bot.IM.Message.AddEmoji(context.Background(), botim.EmojiRequest{FromUID: "u", ChatType: 2, BaseMessageID: "1"})
		},
		func() error {
			return client.Bot.IM.Message.SendInteractionMessage(context.Background(), botim.InteractionMessageRequest{Header: botim.InteractionHeader{ToType: "group", ToID: 1}, Body: tooManyImages})
		},
		func() error {
			_, e := client.Bot.IM.Message.CreateStreamingCard(context.Background(), json.RawMessage(`bad`))
			return e
		},
	}
	for _, call := range cases {
		assertConfigError(t, call())
	}
}

func assertContent(t *testing.T, body map[string]any, messageType, want string) {
	t.Helper()
	content := body[messageType].(map[string]any)["content"]
	if content != want {
		t.Fatalf("%s content = %#v", messageType, content)
	}
}

func assertFreshRaw(t *testing.T, accessor func() json.RawMessage, stored json.RawMessage) {
	t.Helper()
	raw := accessor()
	if len(raw) == 0 {
		t.Fatal("missing raw response")
	}
	raw[0] = '!'
	if stored[0] == '!' {
		t.Fatal("raw accessor returned internal storage")
	}
}

func validEmojiRequest() botim.EmojiRequest {
	return botim.EmojiRequest{FromUID: "u", ChatType: 2, ChatID: 1, BaseMessageID: "1", MessageID2: "2", ReplyContent: "d101", ReplyDescription: "/received"}
}
