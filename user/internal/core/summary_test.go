package core

import (
	"encoding/json"
	"testing"
)

func TestSummaryCorrelationKnownDataDataPaths(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"data":{"request_id":"r1","task_id":"t1"}}`),
		json.RawMessage(`{"data":{"data":{"requestId":"r2","taskId":"t2"}}}`),
	} {
		id, task := summaryCorrelation(map[string]json.RawMessage{"data": raw})
		if id == "" || task == "" {
			t.Fatalf("correlation missing for %s: %q/%q", raw, id, task)
		}
	}
}

func TestSummaryUsesRootRawEnvelopeCorrelation(t *testing.T) {
	result, err := summary(UserRawResult{Data: json.RawMessage(`{"ack":true}`), raw: json.RawMessage(`{"request_id":"root-r","data":{"task_id":"root-t"}}`)}, nil)
	if err != nil || result.RequestID != "root-r" || result.TaskID != "root-t" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
