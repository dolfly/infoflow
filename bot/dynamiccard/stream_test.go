package dynamiccard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	dcinternal "github.com/dolfly/infoflow/internal/dynamiccard"
)

const streamSendResponse = `{
  "code": "ok",
  "data": {
    "code": 0,
    "errmsg": "success",
    "data": {
      "partial": {"msgkey": 1879549251012710400},
      "extra": {"card_id": "card_stream_001"},
      "messageid": null,
      "msgseqid": null,
      "ctime": null
    }
  }
}`

func streamSuccess(sequence int) string {
	return fmt.Sprintf(`{"code":"ok","data":{"code":0,"card_id":"card_stream_001","sequence":%d}}`, sequence)
}

type streamReply struct {
	response string
	status   int
}

type streamCall struct {
	route   string
	payload any
}

type streamScriptedTransport struct {
	mu          sync.Mutex
	calls       []streamCall
	queue       map[string][]streamReply
	blockAppend bool
	gate        chan struct{}
	entered     chan struct{}
	inFlight    int
	maxInFlight int
}

func (t *streamScriptedTransport) script(route, response string, status int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queue[route] = append(t.queue[route], streamReply{response: response, status: status})
}

func (t *streamScriptedTransport) PostJSON(_ context.Context, route string, payload any) (dcinternal.Decision, error) {
	t.mu.Lock()
	t.calls = append(t.calls, streamCall{route: route, payload: payload})
	replies := t.queue[route]
	var reply streamReply
	if len(replies) > 0 {
		reply = replies[0]
		t.queue[route] = replies[1:]
	}
	block := t.blockAppend && route == dcinternal.RouteStreamingAppend
	if block {
		t.inFlight++
		if t.inFlight > t.maxInFlight {
			t.maxInFlight = t.inFlight
		}
	}
	entered := t.entered
	t.mu.Unlock()
	if block {
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		<-t.gate
		t.mu.Lock()
		t.inFlight--
		t.mu.Unlock()
	}
	if reply.response == "" {
		return dcinternal.Decision{}, fmt.Errorf("no scripted response for %s", route)
	}
	status := reply.status
	if status == 0 {
		status = 200
	}
	return dcinternal.Decide([]byte(reply.response)).WithHTTPStatus(status), nil
}

func (t *streamScriptedTransport) count(route string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	count := 0
	for _, call := range t.calls {
		if call.route == route {
			count++
		}
	}
	return count
}

func (t *streamScriptedTransport) payloads(route string) []any {
	t.mu.Lock()
	defer t.mu.Unlock()
	var payloads []any
	for _, call := range t.calls {
		if call.route == route {
			payloads = append(payloads, call.payload)
		}
	}
	return payloads
}

func (t *streamScriptedTransport) lastPayload(tester *testing.T, route string) map[string]any {
	tester.Helper()
	payloads := t.payloads(route)
	if len(payloads) == 0 {
		tester.Fatalf("no %s call recorded", route)
	}
	return decodePayload(tester, payloads[len(payloads)-1])
}

func newTestStream(t *testing.T, options StreamOptions) (*DynamicCardStream, *streamScriptedTransport) {
	t.Helper()
	transport := &streamScriptedTransport{queue: make(map[string][]streamReply)}
	transport.script(dcinternal.RouteSend, streamSendResponse, 200)
	service := Service{transport: transport}
	stream, err := service.NewStream(context.Background(), options)
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	stream.sleep = func(context.Context, time.Duration) error { return nil }
	return stream, transport
}

func TestNewStreamCreatesMarkdownSkeleton(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{
		ToUser:          "user_1001",
		InitialMarkdown: "# 生成中",
	})

	if got := stream.State(); got != StreamStateCreated {
		t.Fatalf("state = %q", got)
	}
	if stream.Sequence() != 1 {
		t.Fatalf("sequence = %d", stream.Sequence())
	}
	if stream.ElementID() != "desc" {
		t.Fatalf("element id = %q", stream.ElementID())
	}
	if stream.ThrottleMs() != DefaultStreamThrottleMs {
		t.Fatalf("throttle = %d", stream.ThrottleMs())
	}
	if stream.Snapshot() != "# 生成中" {
		t.Fatalf("snapshot = %q", stream.Snapshot())
	}
	if transport.count(dcinternal.RouteModifyStreaming) != 0 {
		t.Fatal("create must not enable the stream")
	}
	payload := transport.lastPayload(t, dcinternal.RouteSend)
	contents := objectAt(t, payload, "message", "contents")
	children, _ := objectAt(t, contents, "body")["children"].([]any)
	if len(children) != 1 {
		t.Fatalf("children = %v", children)
	}
	component := children[0].(map[string]any)
	if component["type"] != ComponentTypeMarkdown || component["id"] != "desc" || component["content"] != "# 生成中" {
		t.Fatalf("component = %v", component)
	}
}

func TestNewStreamCustomElementAndThrottle(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{
		GroupID:    "group_1",
		ElementID:  "answer",
		ThrottleMs: Int(0),
	})
	if stream.ElementID() != "answer" || stream.ThrottleMs() != 0 {
		t.Fatalf("element = %q, throttle = %d", stream.ElementID(), stream.ThrottleMs())
	}
	payload := transport.lastPayload(t, dcinternal.RouteSend)
	header := objectAt(t, payload, "message", "header")
	if header["toType"] != SendToTypeGroup {
		t.Fatalf("toType = %v", header["toType"])
	}
	contents := objectAt(t, payload, "message", "contents")
	children, _ := objectAt(t, contents, "body")["children"].([]any)
	component := children[0].(map[string]any)
	if component["id"] != "answer" || component["content"] != "" {
		t.Fatalf("component = %v", component)
	}
}

func TestNewStreamValidation(t *testing.T) {
	cases := []struct {
		name    string
		options StreamOptions
	}{
		{"missing target", StreamOptions{}},
		{"both targets", StreamOptions{ToUser: "u", GroupID: "g"}},
		{"blank element id", StreamOptions{ToUser: "u", ElementID: "   "}},
		{"negative throttle", StreamOptions{ToUser: "u", ThrottleMs: Int(-1)}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &streamScriptedTransport{queue: make(map[string][]streamReply)}
			service := Service{transport: transport}
			if _, err := service.NewStream(context.Background(), testCase.options); err == nil {
				t.Fatal("expected validation error")
			}
			if transport.count(dcinternal.RouteSend) != 0 {
				t.Fatal("validation must not send a request")
			}
		})
	}
}

func TestStreamEnablePayloadAndState(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001", InitialMarkdown: "首屏"})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)

	result, err := stream.Enable(context.Background(), "")
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if stream.State() != StreamStateStreaming || stream.Sequence() != 2 {
		t.Fatalf("state = %q, sequence = %d", stream.State(), stream.Sequence())
	}
	if result.State != StreamStateStreaming || result.Sequence != 2 {
		t.Fatalf("result = %+v", result)
	}
	payload := transport.lastPayload(t, dcinternal.RouteModifyStreaming)
	if payload["card_id"] != "card_stream_001" || payload["action"] != streamActionEnable || payload["sequence"] != float64(1) {
		t.Fatalf("payload = %v", payload)
	}
	elements, _ := payload["streaming_elements"].([]any)
	if len(elements) != 1 {
		t.Fatalf("streaming_elements = %v", payload["streaming_elements"])
	}
	element := elements[0].(map[string]any)
	if element["element_id"] != "desc" || element["content"] != "首屏" {
		t.Fatalf("element = %v", element)
	}
}

func TestStreamAppendSendsCompleteSnapshot(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteStreamingAppend, streamSuccess(3), 200)

	if _, err := stream.Enable(context.Background(), ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	result, err := stream.Append(context.Background(), "# 标题\n\n第一段")
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if result.Sequence != 3 || stream.Sequence() != 3 {
		t.Fatalf("sequence = %d", result.Sequence)
	}
	payload := transport.lastPayload(t, dcinternal.RouteStreamingAppend)
	if payload["card_id"] != "card_stream_001" || payload["element_id"] != "desc" {
		t.Fatalf("payload = %v", payload)
	}
	if payload["sequence"] != float64(2) {
		t.Fatalf("sequence = %v", payload["sequence"])
	}
	if payload["chunk"] != "# 标题\n\n第一段" {
		t.Fatalf("chunk = %v", payload["chunk"])
	}
}

func TestStreamFinishOmitsScheme(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteStreamingAppend, streamSuccess(3), 200)
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(4), 200)

	if _, err := stream.Enable(context.Background(), ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := stream.Append(context.Background(), "中间态"); err != nil {
		t.Fatalf("append: %v", err)
	}
	result, err := stream.Finish(context.Background(), &StreamBody{
		Children: ComponentList{NewMarkdown("desc", "最终内容")},
	}, FinishOptions{})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if result.State != StreamStateFinished || stream.State() != StreamStateFinished {
		t.Fatalf("state = %q", result.State)
	}
	payload := transport.lastPayload(t, dcinternal.RouteModifyStreaming)
	if payload["action"] != streamActionFinish || payload["sequence"] != float64(3) {
		t.Fatalf("payload = %v", payload)
	}
	body := objectAt(t, payload, "body")
	if _, exists := body["scheme"]; exists {
		t.Fatalf("finish body must not carry scheme: %v", body)
	}
	children, _ := body["children"].([]any)
	component := children[0].(map[string]any)
	if component["content"] != "最终内容" {
		t.Fatalf("children = %v", children)
	}
}

func TestStreamFinishWithoutBodyUsesSnapshot(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteStreamingAppend, streamSuccess(3), 200)
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(4), 200)

	if _, err := stream.Enable(context.Background(), ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := stream.Append(context.Background(), "快照内容"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := stream.Finish(context.Background(), nil, FinishOptions{}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	payload := transport.lastPayload(t, dcinternal.RouteModifyStreaming)
	body := objectAt(t, payload, "body")
	children, _ := body["children"].([]any)
	component := children[0].(map[string]any)
	if component["type"] != ComponentTypeMarkdown || component["id"] != "desc" || component["content"] != "快照内容" {
		t.Fatalf("component = %v", component)
	}
}

func TestStreamFinishIsIdempotentAndBlocksAppend(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(3), 200)

	if _, err := stream.Enable(context.Background(), ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	first, err := stream.Finish(context.Background(), nil, FinishOptions{})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	modifyCalls := transport.count(dcinternal.RouteModifyStreaming)

	second, err := stream.Finish(context.Background(), nil, FinishOptions{})
	if err != nil {
		t.Fatalf("second finish: %v", err)
	}
	if transport.count(dcinternal.RouteModifyStreaming) != modifyCalls {
		t.Fatal("idempotent finish must not send a request")
	}
	if second.Sequence != first.Sequence || second.State != StreamStateFinished {
		t.Fatalf("second = %+v", second)
	}

	_, err = stream.Append(context.Background(), "after finish")
	var failure *DynamicCardError
	if !errors.As(err, &failure) || !failure.IsStreamState() {
		t.Fatalf("append after finish error = %v", err)
	}
	if transport.count(dcinternal.RouteStreamingAppend) != 0 {
		t.Fatal("append after finish must not send a request")
	}
}

func TestStreamStateMachineFixture(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteStreamingAppend, streamSuccess(3), 200)
	transport.script(dcinternal.RouteStreamingAppend, streamSuccess(4), 200)
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(5), 200)
	ctx := context.Background()

	steps := []struct {
		name   string
		expect StreamState
		seq    int64
		run    func() error
	}{
		{"create", StreamStateCreated, 1, func() error { return nil }},
		{"enable", StreamStateStreaming, 2, func() error { _, err := stream.Enable(ctx, ""); return err }},
		{"append", StreamStateStreaming, 3, func() error { _, err := stream.Append(ctx, "a"); return err }},
		{"append", StreamStateStreaming, 4, func() error { _, err := stream.Append(ctx, "ab"); return err }},
		{"finish", StreamStateFinished, 5, func() error { _, err := stream.Finish(ctx, nil, FinishOptions{}); return err }},
		{"append after finish", StreamStateFinished, 5, func() error {
			_, err := stream.Append(ctx, "late")
			if err == nil {
				return errors.New("expected local error")
			}
			return nil
		}},
	}
	for _, step := range steps {
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if stream.State() != step.expect {
			t.Fatalf("%s: state = %q, want %q", step.name, stream.State(), step.expect)
		}
		if stream.Sequence() != step.seq {
			t.Fatalf("%s: sequence = %d, want %d", step.name, stream.Sequence(), step.seq)
		}
	}
}

func TestStreamSequenceConflictAdoptsCurrent(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteStreamingAppend, `{
	  "code": 1009,
	  "message": "sequence not match",
	  "request_sequence": 3,
	  "current_sequence": 5,
	  "current_msg_content": {"scheme": "1", "body": {"children": []}}
	}`, 200)
	ctx := context.Background()
	if _, err := stream.Enable(ctx, ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	// Move the local sequence to 3 so request_sequence is observable.
	stream.mu.Lock()
	stream.sequence = 3
	stream.mu.Unlock()
	if _, err := stream.Append(ctx, "stale"); err == nil {
		t.Fatal("expected sequence conflict")
	} else {
		var failure *DynamicCardError
		if !errors.As(err, &failure) || !failure.IsSequenceConflict() {
			t.Fatalf("error = %v", err)
		}
		if failure.RequestSequence == nil || *failure.RequestSequence != 3 {
			t.Fatalf("request sequence = %v", failure.RequestSequence)
		}
		if failure.CurrentSequence == nil || *failure.CurrentSequence != 5 {
			t.Fatalf("current sequence = %v", failure.CurrentSequence)
		}
		if failure.CurrentContent == nil || failure.CurrentContent.Scheme != SchemeV1 {
			t.Fatalf("current content = %+v", failure.CurrentContent)
		}
	}
	if stream.Sequence() != 5 {
		t.Fatalf("local sequence = %d", stream.Sequence())
	}
	if stream.State() != StreamStateStreaming {
		t.Fatalf("state = %q", stream.State())
	}
	if transport.count(dcinternal.RouteStreamingAppend) != 1 {
		t.Fatal("sequence conflict must not be replayed")
	}
}

func TestStreamRateLimitedRetriesOnce(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001", ThrottleMs: Int(0)})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteStreamingAppend, `{"code":6001,"message":"append too frequently"}`, 429)
	transport.script(dcinternal.RouteStreamingAppend, streamSuccess(6), 200)
	var sleeps []time.Duration
	stream.sleep = func(_ context.Context, duration time.Duration) error {
		sleeps = append(sleeps, duration)
		return nil
	}
	ctx := context.Background()
	if _, err := stream.Enable(ctx, ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	result, err := stream.Append(ctx, "latest")
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if result.Sequence != 6 || stream.Sequence() != 6 {
		t.Fatalf("sequence = %d", result.Sequence)
	}
	if transport.count(dcinternal.RouteStreamingAppend) != 2 {
		t.Fatalf("append attempts = %d", transport.count(dcinternal.RouteStreamingAppend))
	}
	if len(sleeps) != 1 || sleeps[0] != streamRetryBackoff {
		t.Fatalf("backoff = %v", sleeps)
	}
	payload := transport.lastPayload(t, dcinternal.RouteStreamingAppend)
	if payload["chunk"] != "latest" || payload["sequence"] != float64(2) {
		t.Fatalf("retry payload = %v", payload)
	}
}

func TestStreamRateLimitedTwiceFails(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001", ThrottleMs: Int(0)})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteStreamingAppend, `{"code":6001,"message":"append too frequently"}`, 429)
	transport.script(dcinternal.RouteStreamingAppend, `{"code":6001,"message":"append too frequently"}`, 429)
	ctx := context.Background()
	if _, err := stream.Enable(ctx, ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	_, err := stream.Append(ctx, "x")
	var failure *DynamicCardError
	if !errors.As(err, &failure) || !failure.IsRateLimited() {
		t.Fatalf("error = %v", err)
	}
	if transport.count(dcinternal.RouteStreamingAppend) != 2 {
		t.Fatalf("attempts = %d", transport.count(dcinternal.RouteStreamingAppend))
	}
	if stream.State() != StreamStateStreaming {
		t.Fatalf("state = %q", stream.State())
	}
}

func TestStream1010States(t *testing.T) {
	ctx := context.Background()

	t.Run("append before enable fails", func(t *testing.T) {
		stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
		transport.script(dcinternal.RouteStreamingAppend, `{"code":1010,"message":"stream is not opened now"}`, 400)
		_, err := stream.Append(ctx, "x")
		var failure *DynamicCardError
		if !errors.As(err, &failure) || !failure.IsStreamState() {
			t.Fatalf("error = %v", err)
		}
		if stream.State() != StreamStateFailed {
			t.Fatalf("state = %q", stream.State())
		}
	})

	t.Run("repeat enable fails", func(t *testing.T) {
		stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
		transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
		transport.script(dcinternal.RouteModifyStreaming, `{"code":1010,"message":"stream is opened now"}`, 400)
		if _, err := stream.Enable(ctx, ""); err != nil {
			t.Fatalf("enable: %v", err)
		}
		_, err := stream.Enable(ctx, "again")
		var failure *DynamicCardError
		if !errors.As(err, &failure) || !failure.IsStreamState() {
			t.Fatalf("error = %v", err)
		}
		if stream.State() != StreamStateFailed {
			t.Fatalf("state = %q", stream.State())
		}
		if transport.count(dcinternal.RouteModifyStreaming) != 2 {
			t.Fatal("repeat enable must reach the platform")
		}
	})

	t.Run("expired append fails locally", func(t *testing.T) {
		stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
		transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
		if _, err := stream.Enable(ctx, ""); err != nil {
			t.Fatalf("enable: %v", err)
		}
		base := stream.StartedAt()
		stream.now = func() time.Time { return base.Add(StreamLifetime + time.Minute) }
		if stream.State() != StreamStateExpired {
			t.Fatalf("state = %q", stream.State())
		}
		_, err := stream.Append(ctx, "late")
		var failure *DynamicCardError
		if !errors.As(err, &failure) || !failure.IsStreamState() {
			t.Fatalf("error = %v", err)
		}
		if transport.count(dcinternal.RouteStreamingAppend) != 0 {
			t.Fatal("expired append must not send a request")
		}
		if _, err := stream.Finish(ctx, nil, FinishOptions{}); err == nil {
			t.Fatal("expired finish must fail locally")
		}
		if transport.count(dcinternal.RouteModifyStreaming) != 1 {
			t.Fatal("expired finish must not send a request")
		}
	})

	t.Run("1010 after expiry maps to expired", func(t *testing.T) {
		stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
		transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
		transport.script(dcinternal.RouteStreamingAppend, `{"code":1010,"message":"stream is not opened now"}`, 400)
		if _, err := stream.Enable(ctx, ""); err != nil {
			t.Fatalf("enable: %v", err)
		}
		if _, err := stream.Append(ctx, "x"); err == nil {
			t.Fatal("expected stream state error")
		}
		if stream.State() != StreamStateExpired {
			t.Fatalf("state = %q", stream.State())
		}
	})
}

func TestStreamSequenceMissingIsProtocolFailure(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteStreamingAppend, `{"code":"ok","data":{"code":0}}`, 200)
	ctx := context.Background()
	if _, err := stream.Enable(ctx, ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	_, err := stream.Append(ctx, "x")
	var failure *DynamicCardError
	if !errors.As(err, &failure) || !failure.IsServer() {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(failure.Message, "protocol") {
		t.Fatalf("message = %q", failure.Message)
	}
	if stream.State() != StreamStateFailed {
		t.Fatalf("state = %q", stream.State())
	}
}

func TestStreamFailureStateMapping(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		response string
		status   int
		state    StreamState
		check    func(*DynamicCardError) bool
	}{
		{"1001 validation fails session", `{"code":1001,"message":"invalid target"}`, 400, StreamStateFailed,
			(*DynamicCardError).IsValidation},
		{"3003 ownership fails session", `{"code":3003,"message":"not owner"}`, 403, StreamStateFailed,
			(*DynamicCardError).IsOwnership},
		{"401 auth fails session", `{"code":401,"message":"unauthorized"}`, 401, StreamStateFailed,
			(*DynamicCardError).IsAuth},
		{"9001 server keeps session", `{"code":9001,"message":"internal gateway error"}`, 500, StreamStateStreaming,
			(*DynamicCardError).IsServer},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
			transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
			transport.script(dcinternal.RouteStreamingAppend, testCase.response, testCase.status)
			if _, err := stream.Enable(ctx, ""); err != nil {
				t.Fatalf("enable: %v", err)
			}
			_, err := stream.Append(ctx, "x")
			var failure *DynamicCardError
			if !errors.As(err, &failure) || !testCase.check(failure) {
				t.Fatalf("error = %v", err)
			}
			if stream.State() != testCase.state {
				t.Fatalf("state = %q, want %q", stream.State(), testCase.state)
			}
			if transport.count(dcinternal.RouteStreamingAppend) != 1 {
				t.Fatal("failed append must not be retried")
			}
		})
	}
}

func TestStreamAppendDelta(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001", InitialMarkdown: "起"})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteStreamingAppend, streamSuccess(3), 200)
	transport.script(dcinternal.RouteStreamingAppend, streamSuccess(4), 200)
	transport.script(dcinternal.RouteStreamingAppend, streamSuccess(5), 200)
	ctx := context.Background()
	if _, err := stream.Enable(ctx, ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := stream.AppendDelta(ctx, ""); err != nil {
		t.Fatalf("empty delta: %v", err)
	}
	if transport.count(dcinternal.RouteStreamingAppend) != 0 {
		t.Fatal("empty delta must not send a request")
	}
	result, err := stream.AppendDelta(ctx, "始")
	if err != nil {
		t.Fatalf("append delta: %v", err)
	}
	if result.Sequence != 3 {
		t.Fatalf("sequence = %d", result.Sequence)
	}
	payload := transport.lastPayload(t, dcinternal.RouteStreamingAppend)
	if payload["chunk"] != "起始" {
		t.Fatalf("chunk = %v", payload["chunk"])
	}
	// Mixing Append (full snapshot) with AppendDelta keeps one snapshot.
	if _, err := stream.Append(ctx, "完整"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if stream.Snapshot() != "完整" {
		t.Fatalf("snapshot = %q", stream.Snapshot())
	}
	if _, err := stream.AppendDelta(ctx, "补充"); err != nil {
		t.Fatalf("append delta after append: %v", err)
	}
	payload = transport.lastPayload(t, dcinternal.RouteStreamingAppend)
	if payload["chunk"] != "完整补充" {
		t.Fatalf("chunk = %v", payload["chunk"])
	}
}

func TestStreamUnicodeDelta(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteStreamingAppend, streamSuccess(3), 200)
	ctx := context.Background()
	if _, err := stream.Enable(ctx, ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := stream.AppendDelta(ctx, "你好🙂"); err != nil {
		t.Fatalf("append delta: %v", err)
	}
	if stream.Snapshot() != "你好🙂" {
		t.Fatalf("snapshot = %q", stream.Snapshot())
	}
}

func TestStreamThrottleBehavior(t *testing.T) {
	ctx := context.Background()

	t.Run("default throttle waits after first request", func(t *testing.T) {
		stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
		transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
		transport.script(dcinternal.RouteStreamingAppend, streamSuccess(3), 200)
		var sleeps []time.Duration
		stream.sleep = func(_ context.Context, duration time.Duration) error {
			sleeps = append(sleeps, duration)
			return nil
		}
		if _, err := stream.Enable(ctx, ""); err != nil {
			t.Fatalf("enable: %v", err)
		}
		if _, err := stream.Append(ctx, "first"); err != nil {
			t.Fatalf("first append: %v", err)
		}
		if len(sleeps) != 1 || sleeps[0] <= 0 {
			t.Fatalf("first append after enable must wait within the throttle window: %v", sleeps)
		}
	})

	t.Run("throttle disabled sends immediately", func(t *testing.T) {
		stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001", ThrottleMs: Int(0)})
		transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
		transport.script(dcinternal.RouteStreamingAppend, streamSuccess(3), 200)
		var sleeps []time.Duration
		stream.sleep = func(_ context.Context, duration time.Duration) error {
			sleeps = append(sleeps, duration)
			return nil
		}
		if _, err := stream.Enable(ctx, ""); err != nil {
			t.Fatalf("enable: %v", err)
		}
		if _, err := stream.Append(ctx, "first"); err != nil {
			t.Fatalf("append: %v", err)
		}
		if len(sleeps) != 0 {
			t.Fatalf("throttleMs=0 must not wait: %v", sleeps)
		}
	})
}

func TestStreamCoalescesLatestSnapshot(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteStreamingAppend, streamSuccess(3), 200)
	ctx := context.Background()
	if _, err := stream.Enable(ctx, ""); err != nil {
		t.Fatalf("enable: %v", err)
	}

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	stream.sleep = func(_ context.Context, _ time.Duration) error {
		entered <- struct{}{}
		<-release
		return nil
	}

	type outcome struct {
		result StreamResult
		err    error
	}
	first := make(chan outcome, 1)
	go func() {
		result, err := stream.Append(ctx, "first")
		first <- outcome{result: result, err: err}
	}()
	<-entered

	second := make(chan outcome, 1)
	go func() {
		result, err := stream.Append(ctx, "second")
		second <- outcome{result: result, err: err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		stream.mu.Lock()
		waiters := 0
		if stream.flight != nil {
			waiters = len(stream.flight.waiters)
		}
		stream.mu.Unlock()
		if waiters >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second append was not coalesced")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)

	firstOutcome := <-first
	secondOutcome := <-second
	if firstOutcome.err != nil || secondOutcome.err != nil {
		t.Fatalf("append errors: %v, %v", firstOutcome.err, secondOutcome.err)
	}
	if firstOutcome.result.Sequence != 3 || secondOutcome.result.Sequence != 3 {
		t.Fatalf("results = %+v, %+v", firstOutcome.result, secondOutcome.result)
	}
	if transport.count(dcinternal.RouteStreamingAppend) != 1 {
		t.Fatalf("coalesced appends = %d", transport.count(dcinternal.RouteStreamingAppend))
	}
	payload := transport.lastPayload(t, dcinternal.RouteStreamingAppend)
	if payload["chunk"] != "second" {
		t.Fatalf("coalesced chunk = %v", payload["chunk"])
	}
}

func TestStreamSameCardSerializesAcrossSessions(t *testing.T) {
	transport := &streamScriptedTransport{queue: make(map[string][]streamReply)}
	transport.script(dcinternal.RouteSend, streamSendResponse, 200)
	transport.script(dcinternal.RouteSend, streamSendResponse, 200)
	service := Service{transport: transport}
	first, err := service.NewStream(context.Background(), StreamOptions{ToUser: "user_1001", ThrottleMs: Int(0)})
	if err != nil {
		t.Fatalf("new first stream: %v", err)
	}
	second, err := service.NewStream(context.Background(), StreamOptions{ToUser: "user_1001", ThrottleMs: Int(0)})
	if err != nil {
		t.Fatalf("new second stream: %v", err)
	}
	first.sleep = func(context.Context, time.Duration) error { return nil }
	second.sleep = func(context.Context, time.Duration) error { return nil }
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.gate = make(chan struct{})
	transport.entered = make(chan struct{}, 1)
	transport.blockAppend = true
	transport.script(dcinternal.RouteStreamingAppend, streamSuccess(3), 200)
	transport.script(dcinternal.RouteStreamingAppend, streamSuccess(4), 200)
	ctx := context.Background()
	if _, err := first.Enable(ctx, ""); err != nil {
		t.Fatalf("enable first: %v", err)
	}
	if _, err := second.Enable(ctx, ""); err != nil {
		t.Fatalf("enable second: %v", err)
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := first.Append(ctx, "first")
		firstDone <- err
	}()
	<-transport.entered

	secondDone := make(chan error, 1)
	go func() {
		_, err := second.Append(ctx, "second")
		secondDone <- err
	}()
	time.Sleep(20 * time.Millisecond)
	close(transport.gate)

	if err := <-firstDone; err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second append: %v", err)
	}
	transport.mu.Lock()
	peak := transport.maxInFlight
	transport.mu.Unlock()
	if peak != 1 {
		t.Fatalf("concurrent appends for one card = %d", peak)
	}
}

func TestStreamAgentFlowStreamCompletedOnFinish(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(3), 200)
	ctx := context.Background()
	if _, err := stream.Enable(ctx, ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	skeleton := `{"type":"agent-flow-stream","status":"running","tools":[]}`
	if _, err := stream.Finish(ctx, &StreamBody{
		Children: ComponentList{NewMarkdown("desc", skeleton)},
	}, FinishOptions{}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	payload := transport.lastPayload(t, dcinternal.RouteModifyStreaming)
	body := objectAt(t, payload, "body")
	children, _ := body["children"].([]any)
	component := children[0].(map[string]any)
	content, _ := component["content"].(string)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		t.Fatalf("content = %q: %v", content, err)
	}
	if decoded["status"] != "completed" {
		t.Fatalf("agent-flow-stream status = %v", decoded["status"])
	}
}

// MD-08: finish rewrites only the agent-flow-stream fence header status and
// leaves every other byte untouched.
func TestStreamFinishMarksAgentFlowFenceCompleted(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(3), 200)
	ctx := context.Background()
	if _, err := stream.Enable(ctx, ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	content := "前言\n\n```agent-flow-stream status=\"streaming\" thinking_expanded=\"true\"\n" +
		":::reasoning\n先读文件\n:::\n" +
		":::tool name=\"read_api\" status=\"running\"\n{\"file\":\"a.txt\"}\n:::\n" +
		"```\n\n最终答案"
	want := "前言\n\n```agent-flow-stream status=\"completed\" thinking_expanded=\"true\"\n" +
		":::reasoning\n先读文件\n:::\n" +
		":::tool name=\"read_api\" status=\"running\"\n{\"file\":\"a.txt\"}\n:::\n" +
		"```\n\n最终答案"
	if _, err := stream.Finish(ctx, &StreamBody{
		Children: ComponentList{NewMarkdown("desc", content)},
	}, FinishOptions{}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	payload := transport.lastPayload(t, dcinternal.RouteModifyStreaming)
	body := objectAt(t, payload, "body")
	children, _ := body["children"].([]any)
	component := children[0].(map[string]any)
	if component["content"] != want {
		t.Fatalf("finish content:\n got: %q\nwant: %q", component["content"], want)
	}
}

func TestStreamFinishDelay(t *testing.T) {
	stream, transport := newTestStream(t, StreamOptions{ToUser: "user_1001"})
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(2), 200)
	transport.script(dcinternal.RouteModifyStreaming, streamSuccess(3), 200)
	var sleeps []time.Duration
	stream.sleep = func(_ context.Context, duration time.Duration) error {
		sleeps = append(sleeps, duration)
		return nil
	}
	ctx := context.Background()
	if _, err := stream.Enable(ctx, ""); err != nil {
		t.Fatalf("enable: %v", err)
	}
	sleeps = nil
	if _, err := stream.Finish(ctx, nil, FinishOptions{DelayMs: 4000}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if len(sleeps) != 1 || sleeps[0] != 4*time.Second {
		t.Fatalf("delay sleeps = %v", sleeps)
	}
}

func newScriptedService() (Service, *streamScriptedTransport) {
	transport := &streamScriptedTransport{queue: make(map[string][]streamReply)}
	transport.script(dcinternal.RouteSend, streamSendResponse, 200)
	return Service{transport: transport}, transport
}

func TestNewStreamSendsAtReplySessionAndRaw(t *testing.T) {
	service, transport := newScriptedService()
	stream, err := service.NewStream(context.Background(), StreamOptions{
		GroupID:         "group_1",
		SessionID:       "session-1",
		InitialMarkdown: "# 生成中",
		At:              &AtOptions{Users: []string{"wangyi71"}},
		Reply: &ReplyOptions{
			MessageID:  "1880207593545261334",
			Digest:     "请处理",
			SenderName: "wangyi71",
		},
		Raw: map[string]json.RawMessage{
			"message": json.RawMessage(`{
				"header": {"extra": {
					"at": {"atAll": false, "atUserIds": ["raw"]},
					"trace": "abc"
				}}
			}`),
		},
	})
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	if stream.State() != StreamStateCreated {
		t.Fatalf("state = %q", stream.State())
	}
	if transport.count(dcinternal.RouteModifyStreaming) != 0 {
		t.Fatal("create must not enable the stream")
	}
	payload := transport.lastPayload(t, dcinternal.RouteSend)
	header := objectAt(t, payload, "message", "header")
	if header["toType"] != SendToTypeGroup {
		t.Fatalf("toType = %v", header["toType"])
	}
	toID := objectAt(t, header, "toId")
	if toID["groupId"] != "group_1" {
		t.Fatalf("groupId = %v", toID["groupId"])
	}
	extra := objectAt(t, header, "extra")
	if extra[SendSessionIDKey] != "session-1" {
		t.Fatalf("sessionId = %v", extra[SendSessionIDKey])
	}
	if extra["trace"] != "abc" {
		t.Fatalf("raw escape hatch was dropped: %v", extra)
	}
	// The typed AT goes to the documented top-level message.body node. A raw
	// header.extra.at is not a v37 field and survives only as raw passthrough.
	body, ok := objectAt(t, payload, "message")["body"].([]any)
	if !ok || len(body) != 1 {
		t.Fatalf("message.body = %v", objectAt(t, payload, "message")["body"])
	}
	node := body[0].(map[string]any)
	if ids := stringList(t, node["atusers"], "atusers"); !reflect.DeepEqual(ids, []string{"wangyi71"}) {
		t.Fatalf("atusers = %v", ids)
	}
	rawAt := objectAt(t, extra, "at")
	if rawAt["atAll"] != false {
		t.Fatalf("raw header at passthrough was dropped: %v", rawAt)
	}
	reply := objectAt(t, extra, "reply")
	if reply["messageId"] != "1880207593545261334" {
		t.Fatalf("messageId = %v", reply["messageId"])
	}
	if reply["digest"] != "请处理" || reply["senderName"] != "wangyi71" {
		t.Fatalf("reply = %v", reply)
	}
	if len(extra) != 4 {
		t.Fatalf("unexpected header.extra fields: %v", extra)
	}
}

func TestNewStreamWithoutAtReplyPayloadUnchanged(t *testing.T) {
	service, transport := newScriptedService()
	if _, err := service.NewStream(context.Background(), StreamOptions{
		ToUser:    "user_1001",
		SessionID: "session-1",
	}); err != nil {
		t.Fatalf("new stream: %v", err)
	}
	got := transport.lastPayload(t, dcinternal.RouteSend)
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
						map[string]any{"type": ComponentTypeMarkdown, "id": "desc", "content": ""},
					},
				},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload changed without at/reply:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestNewStreamAtReplyValidationDoesNotSend(t *testing.T) {
	cases := []struct {
		name    string
		options StreamOptions
	}{
		{
			name:    "blank at user id",
			options: StreamOptions{GroupID: "g", At: &AtOptions{Users: []string{"ok", "  "}}},
		},
		{
			name:    "missing reply message id",
			options: StreamOptions{ToUser: "u", Reply: &ReplyOptions{Digest: "d"}},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, transport := newScriptedService()
			if _, err := service.NewStream(context.Background(), testCase.options); err == nil {
				t.Fatal("expected validation error")
			} else {
				var failure *DynamicCardError
				if !errors.As(err, &failure) || !failure.IsValidation() {
					t.Fatalf("error = %v, want validation DynamicCardError", err)
				}
			}
			if transport.count(dcinternal.RouteSend) != 0 {
				t.Fatal("validation must not send a request")
			}
		})
	}
}
