package realtime_test

import (
	"context"
	"testing"

	"github.com/dolfly/infoflow/bot/internal/realtime"
)

type captureObserver struct {
	records []realtime.Diagnostic
}

func (observer *captureObserver) Observe(record realtime.Diagnostic) {
	observer.records = append(observer.records, record)
}

func TestObserverNormalizesRequiredFieldsAndIsolatesFailure(t *testing.T) {
	capture := &captureObserver{}
	panicking := realtime.DiagnosticHandler(func(realtime.Diagnostic) { panic("observer fixture failure") })
	realtime.Emit(realtime.Observers(panicking, capture), realtime.Diagnostic{
		Event: "fixture.event", Level: "info", Component: "fixture",
	})
	if len(capture.records) != 1 {
		t.Fatalf("records = %d, want 1", len(capture.records))
	}
	record := capture.records[0]
	if record.Event == "" || record.Level == "" || record.Timestamp.IsZero() ||
		record.SDKLanguage != "go" || record.Component == "" || record.RequestID == "" ||
		record.Redaction != realtime.RedactionPolicy {
		t.Fatalf("required fields not normalized: %#v", record)
	}
}

func TestEnsureRequestIDCorrelatesNestedOperations(t *testing.T) {
	ctx, first := realtime.EnsureRequestID(context.Background())
	nested, second := realtime.EnsureRequestID(ctx)
	if first == "" || first != second || realtime.RequestID(nested) != first {
		t.Fatalf("request ids = %q, %q, nested=%q", first, second, realtime.RequestID(nested))
	}
}
