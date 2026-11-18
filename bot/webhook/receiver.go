package webhook

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dolfly/infoflow/bot/dynamiccard"
	"github.com/dolfly/infoflow/bot/events"
	"github.com/dolfly/infoflow/bot/internal/realtime"
)

const (
	defaultMaxBodySize    = 20 << 20
	defaultDedupeCapacity = 1000
	// cardEventDedupeTTL is the contract window for one card_id + EventId.
	cardEventDedupeTTL = 10 * time.Minute
)

var (
	defaultTimestampTolerance = 5 * time.Minute
	defaultDedupeTTL          = 5 * time.Minute
)

// Request is the framework-neutral Webhook input. Body must contain the exact
// bytes received from the network, before any framework body parser.
type Request struct {
	Method  string
	Query   url.Values
	Headers http.Header
	Body    []byte
}

// Response is the stable HTTP response a framework adapter should return.
type Response struct {
	Status int
	Body   string
}

// CallbackExecutor accepts callback work. Returning false must mean that the
// task was not run and will not run; Receiver then returns 503 so the platform
// can retry. Implementations may run accepted work synchronously or later.
type CallbackExecutor interface {
	Submit(func()) bool
}

// CallbackExecutorFunc adapts a function to CallbackExecutor.
type CallbackExecutorFunc func(func()) bool

func (executor CallbackExecutorFunc) Submit(task func()) bool { return executor(task) }

type inlineExecutor struct{}

func (inlineExecutor) Submit(task func()) bool {
	task()
	return true
}

// Options configures a framework-neutral receiver. Zero values select the
// documented limits and tolerances.
type Options struct {
	Dispatcher         *events.Dispatcher
	CheckToken         string
	EncodingAESKey     string
	TimestampTolerance time.Duration
	MaxBodySize        int
	DedupeTTL          time.Duration
	DedupeCapacity     int
	Now                func() time.Time
	Observer           realtime.Observer
	Diagnostics        realtime.DiagnosticHandler
	OnError            realtime.ErrorHandler
	Executor           CallbackExecutor
}

type dedupeEntry struct {
	expiresAt time.Time
	createdAt time.Time
	pending   chan struct{}
}

// Receiver verifies, decrypts, deduplicates, and dispatches Webhook payloads.
type Receiver struct {
	dispatcher         *events.Dispatcher
	checkToken         string
	encodingAESKey     string
	timestampTolerance time.Duration
	maxBodySize        int
	dedupeTTL          time.Duration
	dedupeCapacity     int
	now                func() time.Time
	observer           realtime.Observer
	onError            realtime.ErrorHandler
	executor           CallbackExecutor

	dedupeMu sync.Mutex
	dedupe   map[string]dedupeEntry
}

func NewReceiver(options Options) (*Receiver, error) {
	if options.Dispatcher == nil {
		return nil, errors.New("webhook dispatcher must not be nil")
	}
	if options.TimestampTolerance < 0 || options.DedupeTTL < 0 || options.MaxBodySize < 0 || options.DedupeCapacity < 0 {
		return nil, errors.New("webhook limits and durations must not be negative")
	}
	tolerance := options.TimestampTolerance
	if tolerance == 0 {
		tolerance = defaultTimestampTolerance
	}
	maxBodySize := options.MaxBodySize
	if maxBodySize == 0 {
		maxBodySize = defaultMaxBodySize
	}
	dedupeTTL := options.DedupeTTL
	if dedupeTTL == 0 {
		dedupeTTL = defaultDedupeTTL
	}
	dedupeCapacity := options.DedupeCapacity
	if dedupeCapacity == 0 {
		dedupeCapacity = defaultDedupeCapacity
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	executor := options.Executor
	if executor == nil {
		executor = inlineExecutor{}
	}
	return &Receiver{
		dispatcher:         options.Dispatcher,
		checkToken:         options.CheckToken,
		encodingAESKey:     options.EncodingAESKey,
		timestampTolerance: tolerance,
		maxBodySize:        maxBodySize,
		dedupeTTL:          dedupeTTL,
		dedupeCapacity:     dedupeCapacity,
		now:                now,
		observer:           realtime.Observers(options.Observer, options.Diagnostics),
		onError:            options.OnError,
		executor:           executor,
		dedupe:             make(map[string]dedupeEntry),
	}, nil
}

// Dispatcher returns the receiver's shared dispatcher.
func (r *Receiver) Dispatcher() *events.Dispatcher { return r.dispatcher }

// Handle processes one raw request and never logs request bodies or secrets.
func (r *Receiver) Handle(ctx context.Context, request Request) Response {
	if ctx == nil {
		return Response{Status: http.StatusInternalServerError, Body: "internal error"}
	}
	ctx, requestID := realtime.EnsureRequestID(ctx)
	var response Response
	if len(request.Body) > r.maxBodySize {
		response = Response{Status: http.StatusRequestEntityTooLarge, Body: "payload too large"}
		return r.finish(response, requestID)
	}
	contentType := request.Headers.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.TrimSpace(strings.Split(contentType, ";")[0])
	}
	switch strings.ToLower(mediaType) {
	case "application/x-www-form-urlencoded":
		response = r.handleForm(ctx, request)
	case "text/plain":
		response = r.handleGroup(ctx, request.Body)
	default:
		response = Response{Status: http.StatusBadRequest, Body: "unsupported content type"}
	}
	return r.finish(response, requestID)
}

func (r *Receiver) finish(response Response, requestID string) Response {
	if response.Status >= http.StatusBadRequest {
		realtime.Emit(r.observer, realtime.Diagnostic{
			Level: "warn", Event: "webhook.response.failure", Component: "webhook",
			Operation: "response", Result: "failure", ErrorType: "request_rejected",
			RequestID: requestID, Timestamp: r.now(), HTTPStatus: response.Status,
		})
	}
	return response
}

func (r *Receiver) handleForm(ctx context.Context, request Request) Response {
	form, err := url.ParseQuery(string(request.Body))
	if err != nil {
		return Response{Status: http.StatusBadRequest, Body: "invalid form body"}
	}
	if challenge := form.Get("echostr"); challenge != "" {
		return r.handleChallenge(ctx, form, request.Query, challenge)
	}
	messageJSON := form.Get("messageJson")
	var encrypt string
	switch {
	case messageJSON != "":
		var envelope struct {
			Encrypt *string `json:"Encrypt"`
		}
		if err := json.Unmarshal([]byte(messageJSON), &envelope); err != nil {
			return Response{Status: http.StatusBadRequest, Body: "invalid messageJson"}
		}
		if envelope.Encrypt == nil {
			return Response{Status: http.StatusBadRequest, Body: "missing Encrypt field"}
		}
		encrypt = *envelope.Encrypt
	case form.Get("message") != "":
		envelope, err := decodeSimpleXML([]byte(form.Get("message")))
		if err != nil {
			return Response{Status: http.StatusBadRequest, Body: "invalid message"}
		}
		value, ok := envelope["Encrypt"]
		if !ok {
			value, ok = envelope["encrypt"]
		}
		if !ok {
			return Response{Status: http.StatusBadRequest, Body: "missing Encrypt field"}
		}
		encrypt, _ = value.(string)
	default:
		return Response{Status: http.StatusBadRequest, Body: "missing echostr, messageJson or message"}
	}
	if encrypt == "" {
		return Response{Status: http.StatusBadRequest, Body: "empty content"}
	}
	plaintext, decryptResponse := r.decrypt(ctx, encrypt)
	if decryptResponse != nil {
		return *decryptResponse
	}
	payload, err := decodeJSONObject(plaintext)
	if err != nil {
		payload, err = decodeSimpleXML(plaintext)
		if err != nil {
			return Response{Status: http.StatusInternalServerError, Body: "failed to parse message"}
		}
	}
	return r.dispatch(ctx, payload, plaintext)
}

func (r *Receiver) handleChallenge(ctx context.Context, form, query url.Values, challenge string) Response {
	field := func(name string) string {
		if value := form.Get(name); value != "" {
			return value
		}
		return query.Get(name)
	}
	timestamp := field("timestamp")
	rn := field("rn")
	signature := strings.ToLower(field("signature"))
	if r.checkToken == "" || !r.timestampValid(timestamp) {
		r.verifyFailure(ctx)
		return Response{Status: http.StatusForbidden, Body: "Invalid signature"}
	}
	digest := md5.Sum([]byte(rn + timestamp + r.checkToken))
	expected := hex.EncodeToString(digest[:])
	valid := len(signature) == len(expected) && subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) == 1
	if !valid {
		r.verifyFailure(ctx)
		return Response{Status: http.StatusForbidden, Body: "Invalid signature"}
	}
	realtime.Emit(r.observer, realtime.Diagnostic{
		Level: "info", Event: "webhook.challenge.success", Component: "webhook",
		Operation: "challenge", Result: "success", RequestID: realtime.RequestID(ctx), Timestamp: r.now(),
	})
	return Response{Status: http.StatusOK, Body: challenge}
}

func (r *Receiver) timestampValid(value string) bool {
	if value == "" {
		return false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return false
	}
	var timestamp time.Time
	if len(value) <= 10 {
		timestamp = time.Unix(parsed, 0)
	} else {
		timestamp = time.UnixMilli(parsed)
	}
	difference := r.now().Sub(timestamp)
	return difference >= -r.timestampTolerance && difference <= r.timestampTolerance
}

func (r *Receiver) verifyFailure(ctx context.Context) {
	runtimeError := realtime.RuntimeError{Component: "webhook", Operation: "verify", Category: "verification_failed"}
	realtime.Report(r.onError, runtimeError)
	realtime.Emit(r.observer, realtime.Diagnostic{
		Level: "warn", Event: "webhook.verify.failure", Component: "webhook",
		Operation: "verify", Result: "failure", ErrorType: "verification_failed",
		RequestID: realtime.RequestID(ctx), Timestamp: r.now(),
	})
}

func (r *Receiver) handleGroup(ctx context.Context, encrypted []byte) Response {
	if len(strings.TrimSpace(string(encrypted))) == 0 {
		return Response{Status: http.StatusBadRequest, Body: "empty content"}
	}
	plaintext, decryptResponse := r.decrypt(ctx, string(encrypted))
	if decryptResponse != nil {
		return *decryptResponse
	}
	payload, err := decodeJSONObject(plaintext)
	if err != nil {
		return Response{Status: http.StatusInternalServerError, Body: "failed to parse message"}
	}
	return r.dispatch(ctx, payload, plaintext)
}

func (r *Receiver) decrypt(ctx context.Context, encrypted string) ([]byte, *Response) {
	if r.encodingAESKey == "" {
		r.decryptFailure(ctx)
		response := Response{Status: http.StatusInternalServerError, Body: "encodingAESKey not configured"}
		return nil, &response
	}
	key, err := decodeURLBase64(r.encodingAESKey)
	if err != nil || (len(key) != 16 && len(key) != 24 && len(key) != 32) {
		r.decryptFailure(ctx)
		response := Response{Status: http.StatusInternalServerError, Body: "decryption failed"}
		return nil, &response
	}
	ciphertext, err := decodeURLBase64(encrypted)
	if err != nil || len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		r.decryptFailure(ctx)
		response := Response{Status: http.StatusInternalServerError, Body: "decryption failed"}
		return nil, &response
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		r.decryptFailure(ctx)
		response := Response{Status: http.StatusInternalServerError, Body: "decryption failed"}
		return nil, &response
	}
	plaintext := make([]byte, len(ciphertext))
	decryptBlocks(block, plaintext, ciphertext)
	plaintext, err = unpadPKCS7(plaintext, block.BlockSize())
	if err != nil || !utf8.Valid(plaintext) {
		r.decryptFailure(ctx)
		response := Response{Status: http.StatusInternalServerError, Body: "decryption failed"}
		return nil, &response
	}
	return plaintext, nil
}

func (r *Receiver) decryptFailure(ctx context.Context) {
	runtimeError := realtime.RuntimeError{Component: "webhook", Operation: "decrypt", Category: "decryption_failed"}
	realtime.Report(r.onError, runtimeError)
	realtime.Emit(r.observer, realtime.Diagnostic{
		Level: "warn", Event: "webhook.decrypt.failure", Component: "webhook",
		Operation: "decrypt", Result: "failure", ErrorType: "decryption_failed",
		RequestID: realtime.RequestID(ctx), Timestamp: r.now(),
	})
}

func (r *Receiver) dispatch(ctx context.Context, payload map[string]any, raw []byte) Response {
	if dynamiccard.IsCardEvent(payload) {
		return r.dispatchCardEvent(ctx, payload, raw)
	}
	key := r.dedupeKey(payload)
	var finishClaim func(bool)
	if key != "" {
		duplicate, finish, available := r.claim(ctx, key, r.dedupeTTL)
		if duplicate {
			return Response{Status: http.StatusOK, Body: "success"}
		}
		if !available {
			return r.dispatchRejected(ctx, "dedupe_admission_unavailable")
		}
		finishClaim = finish
	}
	realtime.Emit(r.observer, realtime.Diagnostic{
		Level: "debug", Event: "webhook.dispatch.start", Component: "webhook",
		Operation: "dispatch", Result: "started", PayloadBytes: len(raw),
		RequestID: realtime.RequestID(ctx), Timestamp: r.now(),
	})
	dispatchContext := realtime.DetachedContext(ctx)
	accepted := submitSafely(r.executor, func() {
		r.dispatchAccepted(dispatchContext, payload, raw)
	})
	if finishClaim != nil {
		finishClaim(accepted)
	}
	if !accepted {
		return r.dispatchRejected(ctx, "callback_rejected")
	}
	return Response{Status: http.StatusOK, Body: "success"}
}

// dispatchCardEvent validates, deduplicates and asynchronously dispatches one
// interactive card event. It always returns 200 for a well-formed frame (even a
// duplicate) before business handlers run; an invalid frame is discarded with
// an error report and no payload logging.
func (r *Receiver) dispatchCardEvent(ctx context.Context, payload map[string]any, raw []byte) Response {
	event, err := dynamiccard.NormalizeCardEvent(payload, raw)
	if err != nil {
		r.cardEventDiscarded(ctx)
		return Response{Status: http.StatusOK, Body: "success"}
	}
	key := event.CardID + ":" + event.EventID
	duplicate, finish, available := r.claim(ctx, key, cardEventDedupeTTL)
	if duplicate {
		return Response{Status: http.StatusOK, Body: "success"}
	}
	if !available {
		return r.dispatchRejected(ctx, "dedupe_admission_unavailable")
	}
	dispatchContext := realtime.DetachedContext(ctx)
	accepted := r.submitCardEvent(func() {
		r.dispatchAccepted(dispatchContext, payload, raw)
	})
	if finish != nil {
		finish(accepted)
	}
	if !accepted {
		return r.dispatchRejected(ctx, "callback_rejected")
	}
	return Response{Status: http.StatusOK, Body: "success"}
}

// submitCardEvent never blocks the HTTP response on the default inline
// executor: that executor runs accepted work synchronously, so card events are
// handed to a goroutine. An injected executor keeps control of scheduling.
func (r *Receiver) submitCardEvent(task func()) bool {
	if _, inline := r.executor.(inlineExecutor); inline {
		go func() {
			defer func() { _ = recover() }()
			task()
		}()
		return true
	}
	return submitSafely(r.executor, task)
}

func (r *Receiver) cardEventDiscarded(ctx context.Context) {
	runtimeError := realtime.RuntimeError{Component: "webhook", Operation: "dispatch", Category: "invalid_card_event"}
	realtime.Report(r.onError, runtimeError)
	realtime.Emit(r.observer, realtime.Diagnostic{
		Level: "warn", Event: "webhook.card_event.discarded", Component: "webhook",
		Operation: "dispatch", Result: "failure", ErrorType: "invalid_card_event",
		RequestID: realtime.RequestID(ctx), Timestamp: r.now(),
	})
}

func submitSafely(executor CallbackExecutor, task func()) (accepted bool) {
	defer func() {
		if recover() != nil {
			accepted = false
		}
	}()
	return executor.Submit(task)
}

func (r *Receiver) dispatchRejected(ctx context.Context, errorType string) Response {
	runtimeError := realtime.RuntimeError{Component: "webhook", Operation: "dispatch", Category: "dispatch_failed"}
	realtime.Report(r.onError, runtimeError)
	realtime.Emit(r.observer, realtime.Diagnostic{
		Level: "warn", Event: "webhook.dispatch.failure", Component: "webhook",
		Operation: "dispatch", Result: "failure", ErrorType: errorType,
		RequestID: realtime.RequestID(ctx), Timestamp: r.now(),
	})
	return Response{Status: http.StatusServiceUnavailable, Body: "dispatch unavailable"}
}

func (r *Receiver) dispatchAccepted(ctx context.Context, payload map[string]any, raw []byte) {
	if err := r.dispatcher.DispatchPayload(ctx, payload, raw); err != nil {
		runtimeError := realtime.RuntimeError{Component: "webhook", Operation: "dispatch", Category: "dispatch_failed"}
		realtime.Report(r.onError, runtimeError)
		realtime.Emit(r.observer, realtime.Diagnostic{
			Level: "warn", Event: "webhook.dispatch.failure", Component: "webhook",
			Operation: "dispatch", Result: "failure", ErrorType: "dispatch_failed",
			RequestID: realtime.RequestID(ctx), Timestamp: r.now(),
		})
	}
}

// claim serializes concurrent admission for one dedupe key without running an
// executor or user handler under the registry lock. A waiter retries if the
// first submission is rejected, so no request receives a false duplicate
// success and rejected work never consumes the key.
func (r *Receiver) claim(ctx context.Context, key string, ttl time.Duration) (duplicate bool, finish func(bool), available bool) {
	for {
		now := r.now()
		r.dedupeMu.Lock()
		for existing, entry := range r.dedupe {
			if entry.pending == nil && !entry.expiresAt.After(now) {
				delete(r.dedupe, existing)
			}
		}
		if entry, exists := r.dedupe[key]; exists {
			if entry.pending == nil {
				r.dedupeMu.Unlock()
				return true, nil, true
			}
			pending := entry.pending
			r.dedupeMu.Unlock()
			select {
			case <-pending:
				continue
			case <-ctx.Done():
				return false, nil, false
			}
		}

		for len(r.dedupe) >= r.dedupeCapacity {
			oldestKey := ""
			var oldest time.Time
			for existing, entry := range r.dedupe {
				if entry.pending == nil && (oldestKey == "" || entry.createdAt.Before(oldest)) {
					oldestKey = existing
					oldest = entry.createdAt
				}
			}
			if oldestKey == "" {
				r.dedupeMu.Unlock()
				return false, nil, false
			}
			delete(r.dedupe, oldestKey)
		}

		pending := make(chan struct{})
		r.dedupe[key] = dedupeEntry{createdAt: now, pending: pending}
		r.dedupeMu.Unlock()
		var once sync.Once
		finish := func(accepted bool) {
			once.Do(func() {
				r.dedupeMu.Lock()
				entry, exists := r.dedupe[key]
				if exists && entry.pending == pending {
					if accepted {
						now := r.now()
						entry.pending = nil
						entry.createdAt = now
						entry.expiresAt = now.Add(ttl)
						r.dedupe[key] = entry
					} else {
						delete(r.dedupe, key)
					}
				}
				close(pending)
				r.dedupeMu.Unlock()
			})
		}
		return false, finish, true
	}
}

func (r *Receiver) dedupeKey(payload map[string]any) string {
	message, _ := payload["message"].(map[string]any)
	header, _ := message["header"].(map[string]any)
	if value := scalarString(header["messageid"]); value != "" {
		return "messageid:" + value
	}
	if value := scalarString(header["msgid"]); value != "" {
		return "msgid:" + value
	}
	if value := scalarString(payload["MsgId"]); value != "" {
		return "MsgId:" + value
	}
	from := firstScalar(header, "fromuserid")
	if from == "" {
		from = firstScalar(payload, "FromUserId", "fromuserid")
	}
	if from == "" {
		return ""
	}
	group := firstScalar(payload, "groupid", "groupId")
	if group == "" {
		group = firstScalar(header, "groupid", "groupId")
	}
	if group == "" {
		group = "dm"
	}
	created := firstScalar(header, "ctime")
	if created == "" {
		created = strconv.FormatInt(r.now().UnixMilli(), 10)
	}
	return "fallback:" + from + "_" + group + "_" + created
}

func firstScalar(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := scalarString(payload[key]); value != "" {
			return value
		}
	}
	return ""
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func decodeURLBase64(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(trimmed, "=")); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(trimmed)
}

func decryptBlocks(block cipher.Block, destination, source []byte) {
	for offset := 0; offset < len(source); offset += block.BlockSize() {
		block.Decrypt(destination[offset:offset+block.BlockSize()], source[offset:offset+block.BlockSize()])
	}
}

func unpadPKCS7(value []byte, blockSize int) ([]byte, error) {
	if len(value) == 0 {
		return nil, errors.New("empty padded value")
	}
	padding := int(value[len(value)-1])
	if padding == 0 || padding > blockSize || padding > len(value) {
		return nil, errors.New("invalid padding")
	}
	for _, current := range value[len(value)-padding:] {
		if int(current) != padding {
			return nil, errors.New("invalid padding")
		}
	}
	return append([]byte(nil), value[:len(value)-padding]...), nil
}

func decodeJSONObject(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, errors.New("payload is not an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON")
		}
		return nil, err
	}
	return payload, nil
}

type xmlElement struct {
	name     string
	text     strings.Builder
	hasChild bool
}

func decodeSimpleXML(raw []byte) (map[string]any, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(raw)))
	stack := make([]*xmlElement, 0)
	payload := make(map[string]any)
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if len(stack) > 0 {
				stack[len(stack)-1].hasChild = true
			}
			stack = append(stack, &xmlElement{name: typed.Name.Local})
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write([]byte(typed))
			}
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errors.New("unbalanced XML")
			}
			current := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !current.hasChild {
				payload[current.name] = strings.TrimSpace(current.text.String())
			}
		}
	}
	if len(payload) == 0 {
		return nil, errors.New("XML contains no payload fields")
	}
	return payload, nil
}
