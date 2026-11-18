package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

type SummaryHandler func(context.Context, SummaryEventResult) error

// OnSummary installs a typed handler and returns an idempotent cleanup
// function. The dispatcher remains responsible for concurrent registration
// and dispatch; cleanup only removes this handler instance.
func (d *Dispatcher) OnSummary(route string, options SummaryEventOptions, handler SummaryHandler) (func(), error) {
	if handler == nil {
		return func() {}, errors.New("summary event handler must not be nil")
	}
	wrapper := func(ctx context.Context, event Event) error {
		result, err := NormalizeSummaryEvent(event, options)
		handlerErr := handler(ctx, result)
		if err != nil {
			return err
		}
		return handlerErr
	}
	if err := d.On(route, wrapper); err != nil {
		return func() {}, err
	}
	var once sync.Once
	return func() { once.Do(func() { d.Off(route, wrapper) }) }, nil
}

// SummaryEventOptions controls the diagnostic raw-payload escape hatch. Raw
// payloads are omitted by default so a normalized event cannot accidentally
// duplicate message content or credentials.
type SummaryEventOptions struct{ IncludeRaw bool }

type SummaryCorrelation struct{ Kind, ID string }

type SummaryDiagnostics struct {
	Code    string
	Message string
	LogID   string
}

type SummarySubject struct {
	Title    string
	Abstract string
	Scheme   string
}

type SummarySubjectSummary struct{ Title, Abstract string }

type SummaryEventItem struct {
	Topic, Summary, Reason string
}

type SummaryStatistics struct {
	EventCount   int
	SessionCount *int
	MessageCount *int
}

// SummaryEventResult is the cross-language normalized result for either
// summary event route. Fields not applicable to a route remain empty slices
// or nil optionals; callers never need to inspect platform casing/envelopes.
type SummaryEventResult struct {
	Type             string
	EventType        string
	Outcome          string
	Correlation      SummaryCorrelation
	Subjects         []SummarySubject
	SubjectSummaries []SummarySubjectSummary
	HasMore          *bool
	NextBaseMsgID    string
	Events           []SummaryEventItem
	Statistics       SummaryStatistics
	Text             string
	Diagnostics      SummaryDiagnostics
	RawPayload       json.RawMessage
}

type SummaryEventError struct{ Code, Message, LogID string }

func (e *SummaryEventError) Error() string {
	if e.Code == "" {
		return "InfoFlow summary event failed"
	}
	return fmt.Sprintf("InfoFlow summary event failed (code %s)", e.Code)
}

// NormalizeSummaryEvent adapts either summary route after transport decode or
// Encrypt expansion. It accepts nested envelopes and case-insensitive keys.
func NormalizeSummaryEvent(event Event, options SummaryEventOptions) (SummaryEventResult, error) {
	payload := event.Payload()
	if payload == nil {
		return SummaryEventResult{}, errors.New("summary event payload is empty")
	}
	eventType := strings.ToLower(strings.TrimSpace(event.EventType))
	if eventType == "" {
		eventType = strings.TrimPrefix(strings.ToLower(event.Type), "event.")
	}
	if eventType != "message_summary_result" && eventType != "important_msg_summary_result" {
		return SummaryEventResult{}, errors.New("unsupported summary event type")
	}
	root := firstObject(payload)
	result := SummaryEventResult{Type: "event", EventType: eventType, Outcome: "success", Subjects: []SummarySubject{}, Events: []SummaryEventItem{}}
	if eventType == "message_summary_result" {
		result.Correlation = SummaryCorrelation{Kind: "request_id", ID: firstStringDeep(payload, "requestId", "request_id")}
		result.Subjects = subjectsDeep(root)
		result.SubjectSummaries = subjectSummariesDeep(root)
		result.HasMore = optionalBool(root, "hasMore", "has_more")
		result.NextBaseMsgID = firstStringDeep(root, "nextBasemsgId", "next_basemsg_id", "nextBaseMsgId")
		result.Text = firstStringDeep(root, "text")
	} else {
		result.Correlation = SummaryCorrelation{Kind: "task_id", ID: firstStringDeep(payload, "taskId", "task_id")}
		result.Events = summaryEventsDeep(root)
		result.Statistics = statisticsDeep(root, len(result.Events))
		result.Text = firstStringDeep(root, "text")
	}
	result.Diagnostics = SummaryDiagnostics{
		Code:    summaryDiagnosticCode(payload),
		Message: summaryDiagnosticMessage(payload),
		LogID:   firstStringDeep(root, "logId", "log_id", "logid"),
	}
	if result.Text == "" {
		result.Text = summaryText(result)
	}
	if options.IncludeRaw {
		result.RawPayload = event.RawPayload()
	}
	if eventType == "message_summary_result" && result.Diagnostics.Code == "1002" && noMessagesFound(result.Diagnostics.Message) {
		result.Outcome = "empty"
		result.Subjects = []SummarySubject{}
		result.SubjectSummaries = []SummarySubjectSummary{}
		result.Text = ""
		return result, nil
	}
	if !summarySuccess(result.Diagnostics.Code) {
		return result, &SummaryEventError{Code: result.Diagnostics.Code, Message: result.Diagnostics.Message, LogID: result.Diagnostics.LogID}
	}
	return result, nil
}

func noMessagesFound(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	message = strings.NewReplacer(" ", "", "_", "", "-", "").Replace(message)
	return message == "nomessagesfound" || message == "msgcountinsufficient"
}

func summaryDiagnosticMessage(root map[string]any) string {
	for _, key := range []string{"data", "result"} {
		if child, ok := lookup(root, key).(map[string]any); ok {
			if message := summaryDiagnosticMessage(child); message != "" {
				return message
			}
		}
	}
	message, _ := firstString(root, "message", "errmsg", "errMsg", "msg")
	return message
}

func summaryDiagnosticCode(root map[string]any) string {
	values := make([]string, 0, 4)
	collectScalars(root, map[string]struct{}{"code": {}, "errcode": {}, "errorcode": {}, "error_code": {}}, &values)
	for _, value := range values {
		if !summarySuccess(value) {
			return value
		}
	}
	if len(values) > 0 {
		return values[0]
	}
	return ""
}

func collectScalars(value any, keys map[string]struct{}, output *[]string) {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if _, ok := keys[strings.ToLower(key)]; ok {
				if text := scalarText(child); text != "" {
					*output = append(*output, text)
				}
			}
			collectScalars(child, keys, output)
		}
	case []any:
		for _, child := range node {
			collectScalars(child, keys, output)
		}
	}
}

func scalarText(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func firstObject(value map[string]any) map[string]any {
	for _, key := range []string{"data", "result"} {
		if child, ok := lookup(value, key).(map[string]any); ok {
			return child
		}
	}
	return value
}

func subjectsDeep(root map[string]any) []SummarySubject {
	value := firstDeep(root, "subjects")
	list, _ := value.([]any)
	result := make([]SummarySubject, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		result = append(result, SummarySubject{
			Title:    summaryString(m, "title", "name", "Scheme", "scheme"),
			Abstract: summaryString(m, "abstract", "AbstractText", "abstractText", "summary"),
			Scheme:   summaryString(m, "Scheme", "scheme"),
		})
	}
	return result
}

func summaryEventsDeep(root map[string]any) []SummaryEventItem {
	value := firstDeep(root, "events")
	list, _ := value.([]any)
	result := make([]SummaryEventItem, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		result = append(result, SummaryEventItem{Topic: summaryString(m, "topic", "title"), Summary: summaryString(m, "summary", "abstract"), Reason: summaryString(m, "reason")})
	}
	return result
}

func subjectSummariesDeep(root map[string]any) []SummarySubjectSummary {
	value := firstDeep(root, "subject_summaries", "subjectSummaries")
	list, _ := value.([]any)
	result := make([]SummarySubjectSummary, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		result = append(result, SummarySubjectSummary{Title: summaryString(m, "title", "name"), Abstract: summaryString(m, "abstract", "summary")})
	}
	return result
}

func statisticsDeep(root map[string]any, eventCount int) SummaryStatistics {
	stats := SummaryStatistics{EventCount: eventCount}
	m, _ := firstDeep(root, "statistics").(map[string]any)
	if m == nil {
		return stats
	}
	stats.SessionCount = optionalInt(m, "session_count", "sessionCount")
	stats.MessageCount = optionalInt(m, "message_count", "messageCount")
	return stats
}

func optionalInt(m map[string]any, keys ...string) *int {
	v := firstScalarDeep(m, keys...)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}

func optionalBool(m map[string]any, keys ...string) *bool {
	value := firstDeep(m, keys...)
	if value == nil {
		return nil
	}
	switch current := value.(type) {
	case bool:
		return &current
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(current))
		if err == nil {
			return &parsed
		}
	}
	return nil
}

func summaryText(result SummaryEventResult) string {
	if result.EventType == "message_summary_result" {
		parts := make([]string, 0, len(result.Subjects))
		for _, item := range result.Subjects {
			if item.Title != "" || item.Abstract != "" {
				parts = append(parts, strings.TrimSpace(item.Title+" "+item.Abstract))
			}
		}
		return strings.Join(parts, "；")
	}
	parts := make([]string, 0, len(result.Events))
	for _, item := range result.Events {
		parts = append(parts, strings.TrimSpace(item.Topic+" "+item.Summary))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("重点事件 %d 项：%s", len(parts), strings.Join(parts, "；"))
}

func summarySuccess(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	return code == "" || code == "0" || code == "200" || code == "ok" || code == "success"
}

func summaryString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := lookup(m, key).(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
func firstStringDeep(m map[string]any, keys ...string) string {
	if value := firstDeep(m, keys...); value != nil {
		if text, ok := value.(string); ok {
			return strings.TrimSpace(text)
		}
		return fmt.Sprint(value)
	}
	return ""
}
func firstScalarDeep(m map[string]any, keys ...string) string {
	value := firstDeep(m, keys...)
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return fmt.Sprint(value)
}
func firstDeep(value map[string]any, keys ...string) any {
	for _, key := range keys {
		if found := lookup(value, key); found != nil {
			return found
		}
	}
	for _, child := range value {
		switch node := child.(type) {
		case map[string]any:
			if found := firstDeep(node, keys...); found != nil {
				return found
			}
		case []any:
			for _, item := range node {
				if m, ok := item.(map[string]any); ok {
					if found := firstDeep(m, keys...); found != nil {
						return found
					}
				}
			}
		}
	}
	return nil
}
func lookup(m map[string]any, key string) any {
	for current, value := range m {
		if strings.EqualFold(current, key) {
			return value
		}
	}
	return nil
}
