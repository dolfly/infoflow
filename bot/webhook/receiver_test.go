package webhook_test

import (
	"context"
	"crypto/aes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dolfly/infoflow/bot/events"
	"github.com/dolfly/infoflow/bot/internal/realtime"
	"github.com/dolfly/infoflow/bot/webhook"
)

func encodeKey(key []byte) string {
	return base64.RawURLEncoding.EncodeToString(key)
}

func encryptECB(t *testing.T, key, plaintext []byte) string {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher() error = %v", err)
	}
	padding := block.BlockSize() - len(plaintext)%block.BlockSize()
	padded := append(append([]byte(nil), plaintext...), bytesOf(byte(padding), padding)...)
	ciphertext := make([]byte, len(padded))
	for offset := 0; offset < len(padded); offset += block.BlockSize() {
		block.Encrypt(ciphertext[offset:offset+block.BlockSize()], padded[offset:offset+block.BlockSize()])
	}
	return base64.RawURLEncoding.EncodeToString(ciphertext)
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for i := range result {
		result[i] = value
	}
	return result
}

func formRequest(values url.Values) webhook.Request {
	return webhook.Request{
		Method: http.MethodPost,
		Headers: http.Header{
			"Content-Type": []string{"application/x-www-form-urlencoded; charset=utf-8"},
		},
		Body: []byte(values.Encode()),
	}
}

func textRequest(body string) webhook.Request {
	return webhook.Request{
		Method:  http.MethodPost,
		Headers: http.Header{"Content-Type": []string{"text/plain"}},
		Body:    []byte(body),
	}
}

func TestReceiverChallengeValidation(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	token := "fixture-check-token"
	timestamp := "1800000000"
	rn := "random-nonce"
	digest := md5.Sum([]byte(rn + timestamp + token))
	signature := hex.EncodeToString(digest[:])

	newReceiver := func(checkToken string) *webhook.Receiver {
		receiver, err := webhook.NewReceiver(webhook.Options{
			Dispatcher:         events.NewDispatcher(events.Options{}),
			CheckToken:         checkToken,
			TimestampTolerance: 5 * time.Minute,
			Now:                func() time.Time { return now },
		})
		if err != nil {
			t.Fatalf("NewReceiver() error = %v", err)
		}
		return receiver
	}

	valid := url.Values{"echostr": {"echo-value"}, "signature": {signature}, "timestamp": {timestamp}, "rn": {rn}}
	response := newReceiver(token).Handle(context.Background(), formRequest(valid))
	if response.Status != http.StatusOK || response.Body != "echo-value" {
		t.Fatalf("valid challenge response = %#v", response)
	}

	t.Run("query fields are accepted", func(t *testing.T) {
		request := formRequest(url.Values{"echostr": {"query-echo"}})
		request.Query = url.Values{"signature": {signature}, "timestamp": {timestamp}, "rn": {rn}}
		response := newReceiver(token).Handle(context.Background(), request)
		if response.Status != http.StatusOK || response.Body != "query-echo" {
			t.Fatalf("response = %#v", response)
		}
	})

	t.Run("extreme future timestamp is rejected without duration overflow", func(t *testing.T) {
		extremeTimestamp := "9223372036854775807"
		extremeDigest := md5.Sum([]byte(rn + extremeTimestamp + token))
		values := url.Values{
			"echostr": {"extreme-echo"}, "signature": {hex.EncodeToString(extremeDigest[:])},
			"timestamp": {extremeTimestamp}, "rn": {rn},
		}
		if response := newReceiver(token).Handle(context.Background(), formRequest(values)); response.Status != http.StatusForbidden {
			t.Fatalf("response = %#v", response)
		}
	})

	tests := []struct {
		name       string
		token      string
		mutate     func(url.Values)
		wantStatus int
	}{
		{"missing token", "", func(url.Values) {}, http.StatusForbidden},
		{"bad signature", token, func(values url.Values) { values.Set("signature", strings.Repeat("0", 32)) }, http.StatusForbidden},
		{"invalid timestamp", token, func(values url.Values) { values.Set("timestamp", "not-a-time") }, http.StatusForbidden},
		{"expired timestamp", token, func(values url.Values) { values.Set("timestamp", "1700000000") }, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := url.Values{}
			for key, value := range valid {
				values[key] = append([]string(nil), value...)
			}
			tt.mutate(values)
			response := newReceiver(tt.token).Handle(context.Background(), formRequest(values))
			if response.Status != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Status, tt.wantStatus)
			}
		})
	}
}

func TestReceiverContentAESJSONXMLAndLargeInteger(t *testing.T) {
	keys := [][]byte{
		[]byte("0123456789abcdef"),
		[]byte("0123456789abcdefghijklmn"),
		[]byte("0123456789abcdefghijklmnopqrstuv"),
	}
	for _, key := range keys {
		t.Run(string(rune('a'+len(key))), func(t *testing.T) {
			dispatcher := events.NewDispatcher(events.Options{})
			var captured events.Event
			if err := dispatcher.On("private.text", func(_ context.Context, event events.Event) error {
				captured = event
				return nil
			}); err != nil {
				t.Fatalf("On() error = %v", err)
			}
			receiver, err := webhook.NewReceiver(webhook.Options{
				Dispatcher: dispatcher, EncodingAESKey: encodeKey(key),
			})
			if err != nil {
				t.Fatalf("NewReceiver() error = %v", err)
			}
			plaintext := []byte(`{"MsgType":"text","MsgId":9007199254740993123,"future":{"kept":true}}`)
			messageJSON, _ := json.Marshal(map[string]string{"Encrypt": encryptECB(t, key, plaintext)})
			response := receiver.Handle(context.Background(), formRequest(url.Values{"messageJson": {string(messageJSON)}}))
			if response.Status != http.StatusOK || response.Body != "success" {
				t.Fatalf("response = %#v", response)
			}
			if captured.Payload()["MsgId"] != json.Number("9007199254740993123") {
				t.Fatalf("large MsgId = %#v", captured.Payload()["MsgId"])
			}
		})
	}

	t.Run("private XML fallback retains original raw", func(t *testing.T) {
		key := []byte("0123456789abcdef")
		dispatcher := events.NewDispatcher(events.Options{})
		var captured events.Event
		_ = dispatcher.On("private.text", func(_ context.Context, event events.Event) error {
			captured = event
			return nil
		})
		receiver, _ := webhook.NewReceiver(webhook.Options{Dispatcher: dispatcher, EncodingAESKey: encodeKey(key)})
		xmlPayload := `<xml><MsgType><![CDATA[text]]></MsgType><FromUserId><![CDATA[u1]]></FromUserId><MsgId>42</MsgId></xml>`
		messageJSON, _ := json.Marshal(map[string]string{"Encrypt": encryptECB(t, key, []byte(xmlPayload))})
		response := receiver.Handle(context.Background(), formRequest(url.Values{"messageJson": {string(messageJSON)}}))
		if response.Status != http.StatusOK || captured.MessageType != "text" {
			t.Fatalf("response=%#v event=%#v", response, captured)
		}
		if string(captured.RawPayload()) != xmlPayload {
			t.Fatalf("raw = %q", captured.RawPayload())
		}
	})

	t.Run("group JSON", func(t *testing.T) {
		key := []byte("0123456789abcdef")
		dispatcher := events.NewDispatcher(events.Options{})
		var calls atomic.Int32
		_ = dispatcher.On("group.text", func(context.Context, events.Event) error { calls.Add(1); return nil })
		receiver, _ := webhook.NewReceiver(webhook.Options{Dispatcher: dispatcher, EncodingAESKey: encodeKey(key)})
		payload := `{"groupid":123,"message":{"header":{"msgtype":"text","messageid":1}}}`
		response := receiver.Handle(context.Background(), textRequest(encryptECB(t, key, []byte(payload))))
		if response.Status != http.StatusOK || calls.Load() != 1 {
			t.Fatalf("response=%#v calls=%d", response, calls.Load())
		}
	})
}

func TestReceiverMalformedAndBodyLimits(t *testing.T) {
	dispatcher := events.NewDispatcher(events.Options{})
	receiver, err := webhook.NewReceiver(webhook.Options{
		Dispatcher: dispatcher, EncodingAESKey: encodeKey([]byte("0123456789abcdef")),
	})
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}
	tests := []struct {
		name string
		req  webhook.Request
		want int
	}{
		{"unsupported", webhook.Request{Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{}`)}, http.StatusBadRequest},
		{"empty group", textRequest(""), http.StatusBadRequest},
		{"invalid messageJson", formRequest(url.Values{"messageJson": {`{bad`}}), http.StatusBadRequest},
		{"missing Encrypt", formRequest(url.Values{"messageJson": {`{}`}}), http.StatusBadRequest},
	}

	t.Run("too large", func(t *testing.T) {
		limited, _ := webhook.NewReceiver(webhook.Options{Dispatcher: dispatcher, MaxBodySize: 8})
		if got := limited.Handle(context.Background(), textRequest("123456789")); got.Status != http.StatusRequestEntityTooLarge {
			t.Fatalf("response = %#v", got)
		}
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := receiver.Handle(context.Background(), tt.req); got.Status != tt.want {
				t.Fatalf("response = %#v, want status %d", got, tt.want)
			}
		})
	}

	t.Run("invalid key", func(t *testing.T) {
		invalid, _ := webhook.NewReceiver(webhook.Options{Dispatcher: dispatcher, EncodingAESKey: encodeKey([]byte("short"))})
		response := invalid.Handle(context.Background(), textRequest("encrypted"))
		if response.Status != http.StatusInternalServerError {
			t.Fatalf("response = %#v", response)
		}
	})

	t.Run("invalid padding or parse", func(t *testing.T) {
		key := []byte("0123456789abcdef")
		badCiphertext := base64.RawURLEncoding.EncodeToString(make([]byte, aes.BlockSize))
		bad, _ := webhook.NewReceiver(webhook.Options{Dispatcher: dispatcher, EncodingAESKey: encodeKey(key)})
		response := bad.Handle(context.Background(), textRequest(badCiphertext))
		if response.Status != http.StatusInternalServerError {
			t.Fatalf("response = %#v", response)
		}
	})
}

func TestReceiverDedupeTTLCapacityAndHandlerError(t *testing.T) {
	key := []byte("0123456789abcdef")
	now := time.Unix(1_800_000_000, 0)
	dispatcherErrors := make([]realtime.RuntimeError, 0)
	dispatcher := events.NewDispatcher(events.Options{OnError: func(runtimeError realtime.RuntimeError) {
		dispatcherErrors = append(dispatcherErrors, runtimeError)
	}})
	var calls atomic.Int32
	_ = dispatcher.On("private.text", func(context.Context, events.Event) error {
		calls.Add(1)
		return errors.New("sensitive handler detail")
	})
	receiver, err := webhook.NewReceiver(webhook.Options{
		Dispatcher: dispatcher, EncodingAESKey: encodeKey(key), DedupeTTL: time.Minute,
		DedupeCapacity: 2, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	send := func(payload string) webhook.Response {
		messageJSON, _ := json.Marshal(map[string]string{"Encrypt": encryptECB(t, key, []byte(payload))})
		return receiver.Handle(context.Background(), formRequest(url.Values{"messageJson": {string(messageJSON)}}))
	}
	fixtures := []string{
		`{"MsgType":"text","message":{"header":{"messageid":"m1"}}}`,
		`{"MsgType":"text","message":{"header":{"msgid":"m2"}}}`,
		`{"MsgType":"text","MsgId":"m3"}`,
		`{"MsgType":"text","fromuserid":"u1","message":{"header":{"ctime":"1"}}}`,
	}
	for _, fixture := range fixtures {
		before := calls.Load()
		if got := send(fixture); got.Status != http.StatusOK {
			t.Fatalf("first response = %#v", got)
		}
		if got := send(fixture); got.Status != http.StatusOK || calls.Load() != before+1 {
			t.Fatalf("duplicate response=%#v calls=%d before=%d", got, calls.Load(), before)
		}
	}
	if len(dispatcherErrors) != len(fixtures) {
		t.Fatalf("handler errors = %d, want %d", len(dispatcherErrors), len(fixtures))
	}
	for _, runtimeError := range dispatcherErrors {
		if strings.Contains(runtimeError.Error(), "sensitive handler detail") {
			t.Fatalf("runtime error leaked detail: %v", runtimeError)
		}
	}

	now = now.Add(2 * time.Minute)
	before := calls.Load()
	if got := send(fixtures[0]); got.Status != http.StatusOK || calls.Load() != before+1 {
		t.Fatalf("TTL response=%#v calls=%d before=%d", got, calls.Load(), before)
	}
}

func TestReceiverConcurrentDedupeAdmissionIsAtomic(t *testing.T) {
	key := []byte("0123456789abcdef")
	payload := `{"MsgType":"text","MsgId":"same-concurrent-id"}`
	messageJSON, _ := json.Marshal(map[string]string{"Encrypt": encryptECB(t, key, []byte(payload))})
	request := formRequest(url.Values{"messageJson": {string(messageJSON)}})

	t.Run("accepted submission is dispatched once", func(t *testing.T) {
		dispatcher := events.NewDispatcher(events.Options{})
		var handlerCalls atomic.Int32
		_ = dispatcher.On("private.text", func(context.Context, events.Event) error {
			handlerCalls.Add(1)
			return nil
		})
		firstSubmission := make(chan struct{})
		releaseFirst := make(chan struct{})
		var submissions atomic.Int32
		receiver, _ := webhook.NewReceiver(webhook.Options{
			Dispatcher: dispatcher, EncodingAESKey: encodeKey(key),
			Executor: webhook.CallbackExecutorFunc(func(task func()) bool {
				if submissions.Add(1) == 1 {
					close(firstSubmission)
					<-releaseFirst
				}
				task()
				return true
			}),
		})
		firstResult := make(chan webhook.Response, 1)
		secondResult := make(chan webhook.Response, 1)
		go func() { firstResult <- receiver.Handle(context.Background(), request) }()
		<-firstSubmission
		go func() { secondResult <- receiver.Handle(context.Background(), request) }()
		select {
		case result := <-secondResult:
			t.Fatalf("duplicate completed before admission was decided: %#v", result)
		case <-time.After(50 * time.Millisecond):
		}
		close(releaseFirst)
		if first := <-firstResult; first.Status != http.StatusOK {
			t.Fatalf("first response = %#v", first)
		}
		if second := <-secondResult; second.Status != http.StatusOK {
			t.Fatalf("second response = %#v", second)
		}
		if submissions.Load() != 1 || handlerCalls.Load() != 1 {
			t.Fatalf("submissions=%d handlerCalls=%d", submissions.Load(), handlerCalls.Load())
		}
	})

	t.Run("rejected submission releases key for waiter retry", func(t *testing.T) {
		dispatcher := events.NewDispatcher(events.Options{})
		var handlerCalls atomic.Int32
		_ = dispatcher.On("private.text", func(context.Context, events.Event) error {
			handlerCalls.Add(1)
			return nil
		})
		firstSubmission := make(chan struct{})
		releaseFirst := make(chan struct{})
		var submissions atomic.Int32
		receiver, _ := webhook.NewReceiver(webhook.Options{
			Dispatcher: dispatcher, EncodingAESKey: encodeKey(key),
			Executor: webhook.CallbackExecutorFunc(func(task func()) bool {
				if submissions.Add(1) == 1 {
					close(firstSubmission)
					<-releaseFirst
					return false
				}
				task()
				return true
			}),
		})
		firstResult := make(chan webhook.Response, 1)
		secondResult := make(chan webhook.Response, 1)
		go func() { firstResult <- receiver.Handle(context.Background(), request) }()
		<-firstSubmission
		go func() { secondResult <- receiver.Handle(context.Background(), request) }()
		close(releaseFirst)
		if first := <-firstResult; first.Status != http.StatusServiceUnavailable {
			t.Fatalf("first response = %#v", first)
		}
		if second := <-secondResult; second.Status != http.StatusOK {
			t.Fatalf("second response = %#v", second)
		}
		if submissions.Load() != 2 || handlerCalls.Load() != 1 {
			t.Fatalf("submissions=%d handlerCalls=%d", submissions.Load(), handlerCalls.Load())
		}
	})
}

func TestReceiverObserverCorrelationRedactionAndExecutorRejection(t *testing.T) {
	const (
		checkToken    = "seed-private-callback-token"
		payloadSecret = "seed-private-user-content"
		handlerSecret = "seed-private-handler-detail"
		signatureSeed = "seed-private-signature-source"
	)
	key := []byte("seed-aes-key-016")
	encodedKey := encodeKey(key)
	var records []realtime.Diagnostic
	observer := realtime.Observers(
		realtime.DiagnosticHandler(func(realtime.Diagnostic) { panic("observer fixture failure") }),
		realtime.DiagnosticHandler(func(record realtime.Diagnostic) { records = append(records, record) }),
	)
	dispatcher := events.NewDispatcher(events.Options{Observer: observer})
	var handlerCalls atomic.Int32
	if err := dispatcher.On("private.text", func(context.Context, events.Event) error {
		handlerCalls.Add(1)
		return errors.New(handlerSecret)
	}); err != nil {
		t.Fatalf("On() error = %v", err)
	}
	var submissions atomic.Int32
	receiver, err := webhook.NewReceiver(webhook.Options{
		Dispatcher: dispatcher, CheckToken: checkToken, EncodingAESKey: encodedKey, Observer: observer,
		Executor: webhook.CallbackExecutorFunc(func(task func()) bool {
			if submissions.Add(1) == 1 {
				return false
			}
			task()
			return true
		}),
	})
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}

	payload := []byte(`{"MsgType":"text","MsgId":"retryable","content":"` + payloadSecret + `"}`)
	encrypted := encryptECB(t, key, payload)
	messageJSON, _ := json.Marshal(map[string]string{"Encrypt": encrypted})
	request := formRequest(url.Values{"messageJson": {string(messageJSON)}})
	if response := receiver.Handle(context.Background(), request); response.Status != http.StatusServiceUnavailable || response.Body != "dispatch unavailable" {
		t.Fatalf("rejected response = %#v", response)
	}
	if handlerCalls.Load() != 0 {
		t.Fatalf("rejected submission invoked %d handlers", handlerCalls.Load())
	}
	if response := receiver.Handle(context.Background(), request); response.Status != http.StatusOK || response.Body != "success" {
		t.Fatalf("retry response = %#v", response)
	}
	if handlerCalls.Load() != 1 {
		t.Fatalf("rejected submission was deduped; handler calls = %d", handlerCalls.Load())
	}
	if response := receiver.Handle(context.Background(), request); response.Status != http.StatusOK || handlerCalls.Load() != 1 {
		t.Fatalf("accepted duplicate response=%#v calls=%d", response, handlerCalls.Load())
	}

	now := time.Unix(1_800_000_000, 0)
	timestamp := "1800000000"
	rn := signatureSeed
	digest := md5.Sum([]byte(rn + timestamp + checkToken))
	validSignature := hex.EncodeToString(digest[:])
	challenge := url.Values{
		"echostr": {"safe-echo"}, "timestamp": {timestamp}, "rn": {rn}, "signature": {validSignature},
	}
	challengeReceiver, _ := webhook.NewReceiver(webhook.Options{
		Dispatcher: dispatcher, CheckToken: checkToken, EncodingAESKey: encodedKey,
		Observer: observer, Now: func() time.Time { return now },
	})
	if response := challengeReceiver.Handle(context.Background(), formRequest(challenge)); response.Status != http.StatusOK {
		t.Fatalf("challenge response = %#v", response)
	}
	challenge.Set("signature", strings.Repeat("f", 32))
	if response := challengeReceiver.Handle(context.Background(), formRequest(challenge)); response.Status != http.StatusForbidden {
		t.Fatalf("verify failure response = %#v", response)
	}
	if response := challengeReceiver.Handle(context.Background(), textRequest("seed-private-ciphertext")); response.Status != http.StatusInternalServerError {
		t.Fatalf("decrypt failure response = %#v", response)
	}

	requiredNames := map[string]bool{
		"webhook.dispatch.start": false, "webhook.dispatch.failure": false,
		"webhook.response.failure": false, "webhook.challenge.success": false,
		"webhook.verify.failure": false, "webhook.decrypt.failure": false,
		"event.dispatch.start": false, "event.handler.failure": false, "event.dispatch.success": false,
	}
	byRequest := make(map[string]map[string]bool)
	for i, record := range records {
		if record.Event == "" || record.Level == "" || record.Timestamp.IsZero() || record.SDKLanguage != "go" ||
			record.Component == "" || record.RequestID == "" || record.Redaction != realtime.RedactionPolicy {
			t.Fatalf("record[%d] missing required fields: %#v", i, record)
		}
		if _, expected := requiredNames[record.Event]; expected {
			requiredNames[record.Event] = true
		}
		if byRequest[record.RequestID] == nil {
			byRequest[record.RequestID] = make(map[string]bool)
		}
		byRequest[record.RequestID][record.Event] = true
	}
	for eventName, observed := range requiredNames {
		if !observed {
			t.Errorf("required observer event %q was not captured: %#v", eventName, records)
		}
	}
	correlatedReject := false
	correlatedDispatch := false
	for _, names := range byRequest {
		if names["webhook.dispatch.start"] && names["webhook.dispatch.failure"] && names["webhook.response.failure"] {
			correlatedReject = true
		}
		if names["webhook.dispatch.start"] && names["event.dispatch.start"] && names["event.handler.failure"] && names["event.dispatch.success"] {
			correlatedDispatch = true
		}
	}
	if !correlatedReject || !correlatedDispatch {
		t.Fatalf("request correlation missing: reject=%v dispatch=%v records=%#v", correlatedReject, correlatedDispatch, records)
	}
	serialized, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, sensitive := range []string{
		checkToken, encodedKey, string(key), payloadSecret, handlerSecret, signatureSeed,
		validSignature, encrypted, string(messageJSON), string(payload), "seed-private-ciphertext",
	} {
		if strings.Contains(string(serialized), sensitive) {
			t.Fatalf("observer records leaked exact sensitive value %q: %s", sensitive, serialized)
		}
	}
}
