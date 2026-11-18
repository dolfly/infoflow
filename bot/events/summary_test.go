package events

import (
	"context"
	"errors"
	"testing"
)

func TestNormalizeMessageSummaryCaseInsensitiveAndRawOptIn(t *testing.T) {
	d := NewDispatcher(Options{})
	var got SummaryEventResult
	cleanup, err := d.OnSummary("event.message_summary_result", SummaryEventOptions{}, func(_ context.Context, result SummaryEventResult) error { got = result; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	err = d.DispatchPayload(context.Background(), map[string]any{
		"MSGTYPE": "EVENT", "EVENTTYPE": "MESSAGE_SUMMARY_RESULT", "REQUESTID": "req-1",
		"DATA": map[string]any{"CODE": 200, "SUBJECTS": []any{map[string]any{"Scheme": "项目进展", "AbstractText": "完成联调"}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Correlation.ID != "req-1" || len(got.Subjects) != 1 || got.Subjects[0].Title != "项目进展" || got.Text == "" || len(got.RawPayload) != 0 {
		t.Fatalf("result=%#v", got)
	}

	cleanup, err = d.OnSummary("event.message_summary_result", SummaryEventOptions{IncludeRaw: true}, func(_ context.Context, result SummaryEventResult) error { got = result; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	_ = d.DispatchPayload(context.Background(), map[string]any{"msgType": "event", "eventType": "message_summary_result", "requestId": "req-2", "subjects": []any{}}, []byte(`{"msgType":"event"}`))
	if len(got.RawPayload) == 0 || len(got.Subjects) != 0 || got.Text != "" {
		t.Fatalf("raw/empty result=%#v", got)
	}
}

func TestNormalizeImportantSummaryErrorAndEmpty(t *testing.T) {
	event := Event{Type: "event.important_msg_summary_result", EventType: "IMPORTANT_MSG_SUMMARY_RESULT"}
	// Event payload is intentionally built through dispatcher so clone/raw
	// semantics remain identical to WebSocket and Webhook paths.
	d := NewDispatcher(Options{})
	var got SummaryEventResult
	cleanup, err := d.OnSummary("event.important_msg_summary_result", SummaryEventOptions{}, func(_ context.Context, result SummaryEventResult) error { got = result; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := d.DispatchPayload(context.Background(), map[string]any{"msgType": "event", "eventType": "important_msg_summary_result", "taskId": "task-1", "data": map[string]any{"statistics": map[string]any{"sessionCount": 3}, "events": []any{}}}, nil); err != nil {
		t.Fatal(err)
	}
	if got.Correlation.Kind != "task_id" || got.Correlation.ID != "task-1" || got.Statistics.EventCount != 0 || got.Statistics.SessionCount == nil || got.Text != "" {
		t.Fatalf("empty=%#v", got)
	}

	result, err := NormalizeSummaryEvent(Event{Type: event.Type, EventType: event.EventType, payload: map[string]any{"taskId": "task-2", "code": 500, "errmsg": "failed", "logId": "log-1"}}, SummaryEventOptions{})
	if result.Diagnostics.Code != "500" || result.Diagnostics.Message != "failed" || result.Diagnostics.LogID != "log-1" {
		t.Fatalf("diagnostics=%#v", result.Diagnostics)
	}
	var summaryErr *SummaryEventError
	if !errors.As(err, &summaryErr) || summaryErr.Code != "500" || summaryErr.LogID != "log-1" {
		t.Fatalf("err=%T %#v", err, err)
	}
	result, err = NormalizeSummaryEvent(Event{Type: "event.message_summary_result", EventType: "message_summary_result", payload: map[string]any{"code": "ok", "data": map[string]any{"code": 501, "message": "nested failed"}}}, SummaryEventOptions{})
	if result.Diagnostics.Code != "501" || result.Diagnostics.Message != "nested failed" || err == nil {
		t.Fatalf("nested diagnostics=%#v err=%v", result.Diagnostics, err)
	}
}

func TestNormalizeMessageSummaryNoMessagesIsTerminalEmpty(t *testing.T) {
	for _, message := range []string{"no_messages_found", " NO MESSAGES FOUND ", "no-messages-found", "msg_count_insufficient", " MSG COUNT INSUFFICIENT "} {
		result, err := NormalizeSummaryEvent(Event{Type: "event.message_summary_result", EventType: "MESSAGE_SUMMARY_RESULT", payload: map[string]any{
			"requestId": "req-empty", "code": 1002, "message": message, "subjects": []any{map[string]any{"title": "must-disappear"}},
		}}, SummaryEventOptions{})
		if err != nil || result.Outcome != "empty" || len(result.Subjects) != 0 || result.Text != "" || result.Diagnostics.Code != "1002" {
			t.Fatalf("message=%q result=%#v err=%v", message, result, err)
		}
	}
	result, err := NormalizeSummaryEvent(Event{Type: "event.message_summary_result", EventType: "message_summary_result", payload: map[string]any{"code": 1002, "message": "other failure"}}, SummaryEventOptions{})
	if err == nil || result.Outcome == "empty" {
		t.Fatalf("unexpected non-empty error result=%#v err=%v", result, err)
	}
}

func TestSummaryHandlerCleanupAndNoHandlerDiagnostic(t *testing.T) {
	d := NewDispatcher(Options{})
	cleanup, err := d.OnSummary("event.message_summary_result", SummaryEventOptions{}, func(context.Context, SummaryEventResult) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if err := d.DispatchPayload(context.Background(), map[string]any{"msgType": "event", "eventType": "message_summary_result", "requestId": "req"}, nil); err != nil {
		t.Fatal(err)
	}
}
