package im

import (
	"context"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/dolfly/infoflow/internal/testkit"
)

func TestBuildGroupMarkdownMentionBody(t *testing.T) {
	tests := []struct {
		name     string
		markdown string
		m        GroupMarkdownMention
		wantText string
		wantBody []GroupBody
	}{
		{
			name:     "all targets already in markdown",
			markdown: "hello @alice @123",
			m:        GroupMarkdownMention{AtUserIDs: []string{"alice"}, AtAgentIDs: []int64{123}},
			wantText: "hello @alice @123",
			wantBody: []GroupBody{
				{Type: "AT", AtUserIDs: []string{"alice"}},
				{Type: "AT", AtAgentIDs: []int64{123}},
				{Type: "MD", Content: "hello @alice @123"},
			},
		},
		{
			name:     "partially missing users and bots",
			markdown: "hello @alice @123",
			m:        GroupMarkdownMention{AtUserIDs: []string{"alice", "bob"}, AtAgentIDs: []int64{123, 456}},
			wantText: "hello @alice @123 @bob @456",
			wantBody: []GroupBody{
				{Type: "AT", AtUserIDs: []string{"alice", "bob"}},
				{Type: "AT", AtAgentIDs: []int64{123, 456}},
				{Type: "MD", Content: "hello @alice @123 @bob @456"},
			},
		},
		{
			name:     "all users missing",
			markdown: "hi",
			m:        GroupMarkdownMention{AtUserIDs: []string{"alice", "bob"}},
			wantText: "hi @alice @bob",
			wantBody: []GroupBody{
				{Type: "AT", AtUserIDs: []string{"alice", "bob"}},
				{Type: "MD", Content: "hi @alice @bob"},
			},
		},
		{
			name:     "only bots missing",
			markdown: "hi @alice",
			m:        GroupMarkdownMention{AtUserIDs: []string{"alice"}, AtAgentIDs: []int64{123}},
			wantText: "hi @alice @123",
			wantBody: []GroupBody{
				{Type: "AT", AtUserIDs: []string{"alice"}},
				{Type: "AT", AtAgentIDs: []int64{123}},
				{Type: "MD", Content: "hi @alice @123"},
			},
		},
		{
			name:     "mixed missing users and bots",
			markdown: "status @alice @100",
			m:        GroupMarkdownMention{AtUserIDs: []string{"alice", "bob"}, AtAgentIDs: []int64{100, 200}},
			wantText: "status @alice @100 @bob @200",
			wantBody: []GroupBody{
				{Type: "AT", AtUserIDs: []string{"alice", "bob"}},
				{Type: "AT", AtAgentIDs: []int64{100, 200}},
				{Type: "MD", Content: "status @alice @100 @bob @200"},
			},
		},
		{
			name:     "atAll short-circuits user and robot lists and injects @all marker",
			markdown: "hello @alice",
			m:        GroupMarkdownMention{AtAll: true, AtUserIDs: []string{"alice", "bob"}, AtAgentIDs: []int64{123}},
			wantText: "hello @alice @all",
			wantBody: []GroupBody{
				{Type: "AT", AtAll: true},
				{Type: "MD", Content: "hello @alice @all"},
			},
		},
		{
			name:     "atAll keeps content unchanged when @all marker is already present",
			markdown: "请 @all 注意",
			m:        GroupMarkdownMention{AtAll: true, AtUserIDs: []string{"alice"}},
			wantText: "请 @all 注意",
			wantBody: []GroupBody{
				{Type: "AT", AtAll: true},
				{Type: "MD", Content: "请 @all 注意"},
			},
		},
		{
			name:     "empty lists produce MD only",
			markdown: "hi",
			m:        GroupMarkdownMention{},
			wantText: "hi",
			wantBody: []GroupBody{
				{Type: "MD", Content: "hi"},
			},
		},
		{
			name:     "trailing space keeps empty separator",
			markdown: "hi ",
			m:        GroupMarkdownMention{AtUserIDs: []string{"alice"}},
			wantText: "hi @alice",
			wantBody: []GroupBody{
				{Type: "AT", AtUserIDs: []string{"alice"}},
				{Type: "MD", Content: "hi @alice"},
			},
		},
		{
			name:     "trailing newline keeps empty separator",
			markdown: "hi\n",
			m:        GroupMarkdownMention{AtAgentIDs: []int64{123}},
			wantText: "hi\n@123",
			wantBody: []GroupBody{
				{Type: "AT", AtAgentIDs: []int64{123}},
				{Type: "MD", Content: "hi\n@123"},
			},
		},
		{
			name:     "no trailing whitespace adds separator",
			markdown: "hi",
			m:        GroupMarkdownMention{AtAgentIDs: []int64{123}},
			wantText: "hi @123",
			wantBody: []GroupBody{
				{Type: "AT", AtAgentIDs: []int64{123}},
				{Type: "MD", Content: "hi @123"},
			},
		},
		{
			name:     "empty markdown appends with leading separator",
			markdown: "",
			m:        GroupMarkdownMention{AtUserIDs: []string{"alice"}},
			wantText: " @alice",
			wantBody: []GroupBody{
				{Type: "AT", AtUserIDs: []string{"alice"}},
				{Type: "MD", Content: " @alice"},
			},
		},
		{
			name:     "duplicate ids are kept and appended once per occurrence",
			markdown: "hi",
			m:        GroupMarkdownMention{AtUserIDs: []string{"alice", "alice"}},
			wantText: "hi @alice @alice",
			wantBody: []GroupBody{
				{Type: "AT", AtUserIDs: []string{"alice", "alice"}},
				{Type: "MD", Content: "hi @alice @alice"},
			},
		},
		{
			name:     "list order is preserved",
			markdown: "hi",
			m:        GroupMarkdownMention{AtAgentIDs: []int64{2, 1}, AtUserIDs: []string{"z", "a"}},
			wantText: "hi @z @a @2 @1",
			wantBody: []GroupBody{
				{Type: "AT", AtUserIDs: []string{"z", "a"}},
				{Type: "AT", AtAgentIDs: []int64{2, 1}},
				{Type: "MD", Content: "hi @z @a @2 @1"},
			},
		},
		{
			name:     "substring match treats a longer mention as already present",
			markdown: "check @1234",
			m:        GroupMarkdownMention{AtAgentIDs: []int64{123}},
			wantText: "check @1234",
			wantBody: []GroupBody{
				{Type: "AT", AtAgentIDs: []int64{123}},
				{Type: "MD", Content: "check @1234"},
			},
		},
		{
			name:     "carriage-return newline keeps empty separator",
			markdown: "hi\r\n",
			m:        GroupMarkdownMention{AtUserIDs: []string{"alice"}},
			wantText: "hi\r\n@alice",
			wantBody: []GroupBody{
				{Type: "AT", AtUserIDs: []string{"alice"}},
				{Type: "MD", Content: "hi\r\n@alice"},
			},
		},
		{
			name:     "duplicate id already present in markdown skips the append",
			markdown: "hi @alice",
			m:        GroupMarkdownMention{AtUserIDs: []string{"alice", "alice"}},
			wantText: "hi @alice",
			wantBody: []GroupBody{
				{Type: "AT", AtUserIDs: []string{"alice", "alice"}},
				{Type: "MD", Content: "hi @alice"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, body := BuildGroupMarkdownMentionBody(tt.markdown, tt.m)
			if text != tt.wantText {
				t.Fatalf("BuildGroupMarkdownMentionBody() content = %q, want %q", text, tt.wantText)
			}
			if !reflect.DeepEqual(body, tt.wantBody) {
				t.Fatalf("BuildGroupMarkdownMentionBody() body = %#v, want %#v", body, tt.wantBody)
			}
		})
	}
}

func TestSendGroupMarkdownWithMentionsRequestShapes(t *testing.T) {
	tests := []struct {
		name    string
		request SendGroupMarkdownMentionRequest
		want    string
	}{
		{
			name: "missing user and robot mentions are appended",
			request: SendGroupMarkdownMentionRequest{
				GroupID:         13222036,
				Markdown:        "hello",
				Mentions:        GroupMarkdownMention{AtUserIDs: []string{"alice", "bob"}, AtAgentIDs: []int64{123}},
				ClientMessageID: 123,
			},
			want: `{"message":{"header":{"toid":13222036,"totype":"GROUP","msgtype":"MD","clientmsgid":123,"role":"robot"},"body":[{"type":"AT","atuserids":["alice","bob"]},{"type":"AT","atagentids":[123]},{"type":"MD","content":"hello @alice @bob @123"}]}}`,
		},
		{
			name: "atAll short-circuits and drops lists",
			request: SendGroupMarkdownMentionRequest{
				GroupID:         13222036,
				Markdown:        "hello",
				Mentions:        GroupMarkdownMention{AtAll: true, AtUserIDs: []string{"alice"}, AtAgentIDs: []int64{123}},
				ClientMessageID: 123,
			},
			want: `{"message":{"header":{"toid":13222036,"totype":"GROUP","msgtype":"MD","clientmsgid":123,"role":"robot"},"body":[{"type":"AT","atall":true},{"type":"MD","content":"hello @all"}]}}`,
		},
		{
			name: "no mentions sends MD only",
			request: SendGroupMarkdownMentionRequest{
				GroupID:         13222036,
				Markdown:        "hello",
				ClientMessageID: 123,
			},
			want: `{"message":{"header":{"toid":13222036,"totype":"GROUP","msgtype":"MD","clientmsgid":123,"role":"robot"},"body":[{"type":"MD","content":"hello"}]}}`,
		},
		{
			name: "reply is forwarded",
			request: SendGroupMarkdownMentionRequest{
				GroupID:         13222036,
				Markdown:        "hello",
				Reply:           &GroupReply{MessageID: "m1", Preview: "prev", IMID: "imid-1"},
				ClientMessageID: 123,
			},
			want: `{"message":{"header":{"toid":13222036,"totype":"GROUP","msgtype":"MD","clientmsgid":123,"role":"robot"},"body":[{"type":"MD","content":"hello"}],"reply":{"messageid":"m1","preview":"prev","imid":"imid-1"}}}`,
		},
		{
			name: "self agent provided but not mentioned succeeds",
			request: SendGroupMarkdownMentionRequest{
				GroupID:         13222036,
				Markdown:        "hello",
				Mentions:        GroupMarkdownMention{AtAgentIDs: []int64{456}},
				SelfAgentID:     777,
				ClientMessageID: 123,
			},
			want: `{"message":{"header":{"toid":13222036,"totype":"GROUP","msgtype":"MD","clientmsgid":123,"role":"robot"},"body":[{"type":"AT","atagentids":[456]},{"type":"MD","content":"hello @456"}]}}`,
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
			result, err := service.SendGroupMarkdownWithMentions(context.Background(), tt.request)
			if err != nil {
				t.Fatalf("SendGroupMarkdownWithMentions() error = %v", err)
			}
			if result.MessageID != 4 || result.MessageSequenceID != 5 || result.CreatedAtMillis != 6 {
				t.Fatalf("result = %#v", result)
			}
			testkit.AssertJSONEqual(t, requestBody, tt.want)
		})
	}
}

func TestSendGroupMarkdownWithMentionsRejectsInvalidRequests(t *testing.T) {
	manyUsers := make([]string, 51)
	for i := range manyUsers {
		manyUsers[i] = "user" + strconv.Itoa(i)
	}
	manyBots := make([]int64, 11)
	for i := range manyBots {
		manyBots[i] = int64(100 + i)
	}
	tests := []struct {
		name    string
		request SendGroupMarkdownMentionRequest
	}{
		{
			name:    "blank markdown",
			request: SendGroupMarkdownMentionRequest{GroupID: 1, Markdown: "   "},
		},
		{
			name:    "non-positive group ID",
			request: SendGroupMarkdownMentionRequest{GroupID: 0, Markdown: "hi"},
		},
		{
			name:    "more than 50 users",
			request: SendGroupMarkdownMentionRequest{GroupID: 1, Markdown: "hi", Mentions: GroupMarkdownMention{AtUserIDs: manyUsers}},
		},
		{
			name:    "more than 10 robots",
			request: SendGroupMarkdownMentionRequest{GroupID: 1, Markdown: "hi", Mentions: GroupMarkdownMention{AtAgentIDs: manyBots}},
		},
		{
			name:    "self agent mentioned",
			request: SendGroupMarkdownMentionRequest{GroupID: 1, Markdown: "hi", Mentions: GroupMarkdownMention{AtAgentIDs: []int64{777}}, SelfAgentID: 777},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
				t.Fatalf("unexpected network request to %s", r.URL.Path)
			})
			_, err := service.SendGroupMarkdownWithMentions(context.Background(), tt.request)
			assertConfigError(t, err)
		})
	}
}

func TestSendGroupMarkdownWithMentionsAtAllIgnoresListsAndLimits(t *testing.T) {
	manyUsers := make([]string, 60)
	for i := range manyUsers {
		manyUsers[i] = "user" + strconv.Itoa(i)
	}
	var requestBody []byte
	service := newMessageOperationsTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/robot/msg/groupmsgsend" {
			t.Fatalf("route = %s", r.URL.Path)
		}
		requestBody = testkit.ReadRequestBody(t, r)
		testkit.WriteJSON(w, http.StatusOK, `{"code":"ok","data":{"errcode":0,"data":{"messageid":4,"msgseqid":5,"ctime":6}}}`)
	})
	result, err := service.SendGroupMarkdownWithMentions(context.Background(), SendGroupMarkdownMentionRequest{
		GroupID:         13222036,
		Markdown:        "hello",
		Mentions:        GroupMarkdownMention{AtAll: true, AtUserIDs: manyUsers, AtAgentIDs: []int64{777}},
		SelfAgentID:     777,
		ClientMessageID: 123,
	})
	if err != nil {
		t.Fatalf("SendGroupMarkdownWithMentions() error = %v", err)
	}
	if result.MessageID != 4 || result.MessageSequenceID != 5 || result.CreatedAtMillis != 6 {
		t.Fatalf("result = %#v", result)
	}
	testkit.AssertJSONEqual(t, requestBody, `{"message":{"header":{"toid":13222036,"totype":"GROUP","msgtype":"MD","clientmsgid":123,"role":"robot"},"body":[{"type":"AT","atall":true},{"type":"MD","content":"hello @all"}]}}`)
}
