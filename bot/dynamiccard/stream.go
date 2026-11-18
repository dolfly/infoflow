package dynamiccard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	dcinternal "github.com/dolfly/infoflow/internal/dynamiccard"
)

// StreamState is the lifecycle state of a DynamicCardStream.
type StreamState string

// Stream lifecycle states. A session starts in created, becomes streaming after
// Enable, and ends in finished. expired means the platform's 60 minute stream
// lifetime is over; failed means an unrecoverable error (auth, protocol, invalid
// target) left the session unusable.
const (
	StreamStateCreated   StreamState = "created"
	StreamStateStreaming StreamState = "streaming"
	StreamStateFinished  StreamState = "finished"
	StreamStateExpired   StreamState = "expired"
	StreamStateFailed    StreamState = "failed"
)

const (
	// DefaultStreamThrottleMs is the default minimum interval between two
	// append requests in milliseconds.
	DefaultStreamThrottleMs = 1000
	// StreamLifetime is the platform's maximum streaming lifetime measured from
	// card creation. The SDK only reports it locally; it never closes the stream
	// on its own.
	StreamLifetime = 60 * time.Minute

	streamActionEnable = "enable"
	streamActionFinish = "finish"
	streamDefaultElem  = "desc"
)

// streamCardGate serializes stream mutations per card id across every session
// created by this process. Different cards run in parallel.
var streamCardGate = dcinternal.NewGate()

// StreamOptions configures NewStream. Exactly one of ToUser and GroupID must be
// non-blank, and the remaining fields describe the initial markdown skeleton.
// At, Reply and Raw only affect the initial sendUnifiedMsg that creates the
// card; they cannot change the message header afterwards.
type StreamOptions struct {
	// ToUser is the single-chat target.
	ToUser string
	// GroupID is the group-chat target.
	GroupID string
	// SessionID is passed through to the send header when non-empty.
	SessionID string
	// At adds structured group-chat @ metadata to the initial send.
	At *AtOptions
	// Reply quotes an existing message in the initial send.
	Reply *ReplyOptions
	// InitialMarkdown becomes the initial content of the streaming markdown
	// component; empty means an empty first screen.
	InitialMarkdown string
	// ElementID is the streaming markdown component id; empty defaults to
	// "desc".
	ElementID string
	// ThrottleMs is the minimum interval between append requests. nil defaults
	// to DefaultStreamThrottleMs; 0 disables throttling (not recommended).
	// Use Int(0) to disable it explicitly.
	ThrottleMs *int
	// Raw only supplements fields the initial Send did not set; known fields
	// win at every level, so Raw cannot override the SDK-built header or the
	// typed At/Reply values.
	Raw map[string]json.RawMessage
}

// StreamBody is the final body of Finish. It contains only children and never a
// scheme field.
type StreamBody struct {
	Children ComponentList `json:"children"`
}

// FinishOptions controls the Finish call. DelayMs waits before the finish
// request so the platform can render the last appended chunk; it is 0 by
// default and never applied automatically.
type FinishOptions struct {
	DelayMs int
}

// StreamResult is the normalized result of one streaming lifecycle step.
// Sequence is always the value returned by the platform; the SDK never
// increments it locally. Raw is the original response.
type StreamResult struct {
	CardID   string
	Sequence int64
	State    StreamState
	Raw      json.RawMessage
}

// RawJSON returns the original response, defensively copied.
func (r StreamResult) RawJSON() json.RawMessage {
	return json.RawMessage(append([]byte(nil), r.Raw...))
}

// DynamicCardStream is one streaming card session: create, enable, append x N
// and finish, with per-card serialization, latest-wins coalescing and
// server-only sequence updates. It is safe for concurrent use.
type DynamicCardStream struct {
	service   Service
	cardID    string
	elementID string
	throttle  time.Duration
	startedAt time.Time

	mu       sync.Mutex
	state    StreamState
	sequence int64
	snapshot string
	lastSent time.Time
	lastRaw  json.RawMessage
	flight   *streamFlight
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
}

type streamFlight struct {
	snapshot string
	waiters  []chan streamOutcome
}

type streamOutcome struct {
	result StreamResult
	err    error
}

type streamEnablePayload struct {
	CardID            string          `json:"card_id"`
	Action            string          `json:"action"`
	Sequence          int64           `json:"sequence"`
	StreamingElements []streamElement `json:"streaming_elements"`
}

type streamElement struct {
	ElementID string `json:"element_id"`
	Content   string `json:"content"`
}

type streamAppendPayload struct {
	CardID    string `json:"card_id"`
	ElementID string `json:"element_id"`
	Sequence  int64  `json:"sequence"`
	Chunk     string `json:"chunk"`
}

type streamFinishPayload struct {
	CardID   string          `json:"card_id"`
	Action   string          `json:"action"`
	Sequence int64           `json:"sequence"`
	Body     json.RawMessage `json:"body"`
}

// NewStream creates a dynamic card with one markdown component and returns a
// session in the created state with sequence 1. It reuses Send, so the usual
// send validation and error model apply, including the At/Reply prechecks and
// the Raw merge rules. At, Reply and Raw reach only this initial send; later
// enable/append/finish cannot modify the message header. It never sends enable:
// the caller must call Enable before appending.
func (s Service) NewStream(ctx context.Context, options StreamOptions) (*DynamicCardStream, error) {
	if ctx == nil {
		return nil, newValidationError("context", "must not be nil")
	}
	elementID, err := resolveStreamElementID(options.ElementID)
	if err != nil {
		return nil, err
	}
	throttleMs := DefaultStreamThrottleMs
	if options.ThrottleMs != nil {
		throttleMs = *options.ThrottleMs
	}
	if throttleMs < 0 {
		return nil, newValidationError("throttleMs", "must not be negative")
	}
	content := NewContent(NewMarkdown(elementID, options.InitialMarkdown))
	ref, err := s.Send(ctx, SendRequest{
		ToUser:    options.ToUser,
		GroupID:   options.GroupID,
		SessionID: options.SessionID,
		At:        options.At,
		Reply:     options.Reply,
		Content:   content,
		Raw:       options.Raw,
	})
	if err != nil {
		return nil, err
	}
	now := time.Now
	stream := &DynamicCardStream{
		service:   s,
		cardID:    ref.CardID,
		elementID: elementID,
		throttle:  time.Duration(throttleMs) * time.Millisecond,
		startedAt: now(),
		state:     StreamStateCreated,
		sequence:  1,
		snapshot:  options.InitialMarkdown,
		lastRaw:   ref.RawJSON(),
		now:       now,
		sleep:     sleepContext,
	}
	return stream, nil
}

func resolveStreamElementID(value string) (string, error) {
	if value == "" {
		return streamDefaultElem, nil
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", newValidationError("elementId", "must not be blank")
	}
	return trimmed, nil
}

// CardID returns the platform card id of this session.
func (s *DynamicCardStream) CardID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cardID
}

// ElementID returns the streaming markdown component id.
func (s *DynamicCardStream) ElementID() string { return s.elementID }

// ThrottleMs returns the configured append interval in milliseconds.
func (s *DynamicCardStream) ThrottleMs() int {
	return int(s.throttle / time.Millisecond)
}

// StartedAt returns the card creation time used for the 60 minute lifetime.
func (s *DynamicCardStream) StartedAt() time.Time { return s.startedAt }

// Snapshot returns the current content snapshot used by AppendDelta.
func (s *DynamicCardStream) Snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot
}

// Sequence returns the last sequence confirmed by the platform.
func (s *DynamicCardStream) Sequence() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sequence
}

// State returns the current lifecycle state. A session that passed the 60
// minute platform lifetime but was never finished reports expired.
func (s *DynamicCardStream) State() StreamState {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshExpiredLocked()
	return s.state
}

// Enable opens the stream with action=enable. A blank content uses the current
// snapshot (the initial markdown or the first screen set by an earlier call); an
// empty string is valid and renders an empty first screen. A repeated enable
// reaches the platform and its 1010 moves the session to failed; the SDK never
// finishes and reopens a stream automatically.
func (s *DynamicCardStream) Enable(ctx context.Context, content string) (StreamResult, error) {
	if ctx == nil {
		return StreamResult{}, newValidationError("context", "must not be nil")
	}
	if err := s.checkOperable(OperationStreamEnable); err != nil {
		return StreamResult{}, err
	}
	release, err := streamCardGate.Lock(ctx, s.CardID())
	if err != nil {
		return StreamResult{}, err
	}
	defer release()
	if err := s.checkOperable(OperationStreamEnable); err != nil {
		return StreamResult{}, err
	}

	s.mu.Lock()
	effective := content
	if effective == "" {
		effective = s.snapshot
	}
	s.snapshot = effective
	sequence := s.sequence
	s.mu.Unlock()

	payload := streamEnablePayload{
		CardID:            s.CardID(),
		Action:            streamActionEnable,
		Sequence:          sequence,
		StreamingElements: []streamElement{{ElementID: s.elementID, Content: effective}},
	}
	response, err := s.service.post(ctx, dcinternal.RouteModifyStreaming, payload, OperationStreamEnable)
	if err != nil {
		return StreamResult{}, s.handleStreamFailure(response, err, OperationStreamEnable, sequence)
	}
	return s.applySuccess(response, OperationStreamEnable, sequence, StreamStateStreaming)
}

// Append sends a complete content snapshot (not a delta). Calls within the
// throttle window are coalesced: only the newest snapshot is sent and every
// waiting caller completes with that request's result. After Finish, Append
// fails locally without a request.
func (s *DynamicCardStream) Append(ctx context.Context, snapshot string) (StreamResult, error) {
	if ctx == nil {
		return StreamResult{}, newValidationError("context", "must not be nil")
	}
	if err := s.checkAppendable(OperationStreamAppend); err != nil {
		return StreamResult{}, err
	}

	s.mu.Lock()
	if s.flight != nil {
		s.flight.snapshot = snapshot
		s.snapshot = snapshot
		waiter := make(chan streamOutcome, 1)
		s.flight.waiters = append(s.flight.waiters, waiter)
		s.mu.Unlock()
		select {
		case outcome := <-waiter:
			return outcome.result, outcome.err
		case <-ctx.Done():
			return StreamResult{}, ctx.Err()
		}
	}
	flight := &streamFlight{snapshot: snapshot, waiters: []chan streamOutcome{make(chan streamOutcome, 1)}}
	s.flight = flight
	s.snapshot = snapshot
	s.mu.Unlock()

	return s.runAppend(ctx, flight)
}

// AppendDelta appends delta to the internal snapshot at the Unicode string
// level and sends the new complete snapshot. An empty delta sends nothing and
// returns the latest result. It never performs markdown merging.
func (s *DynamicCardStream) AppendDelta(ctx context.Context, delta string) (StreamResult, error) {
	if ctx == nil {
		return StreamResult{}, newValidationError("context", "must not be nil")
	}
	if delta == "" {
		if err := s.checkAppendable(OperationStreamAppend); err != nil {
			return StreamResult{}, err
		}
		s.mu.Lock()
		result := s.resultLocked()
		s.mu.Unlock()
		return result, nil
	}
	s.mu.Lock()
	s.snapshot += delta
	snapshot := s.snapshot
	s.mu.Unlock()
	return s.Append(ctx, snapshot)
}

// Finish closes the stream with action=finish. body is the authoritative final
// content; nil (or an empty children list) uses the current snapshot as one
// markdown component. The emitted body never contains a scheme field and, when
// an agent-flow-stream structure is present, its status is set to completed.
// Calling Finish on a finished session returns the last result without a
// request. DelayMs waits before the request.
func (s *DynamicCardStream) Finish(ctx context.Context, body *StreamBody, options FinishOptions) (StreamResult, error) {
	if ctx == nil {
		return StreamResult{}, newValidationError("context", "must not be nil")
	}
	if result, done, err := s.finishShortCircuit(); done {
		return result, err
	}
	if options.DelayMs < 0 {
		return StreamResult{}, newValidationError("delayMs", "must not be negative")
	}
	release, err := streamCardGate.Lock(ctx, s.CardID())
	if err != nil {
		return StreamResult{}, err
	}
	defer release()
	if result, done, err := s.finishShortCircuit(); done {
		return result, err
	}
	if options.DelayMs > 0 {
		if err := s.sleep(ctx, time.Duration(options.DelayMs)*time.Millisecond); err != nil {
			return StreamResult{}, err
		}
	}

	s.mu.Lock()
	sequence := s.sequence
	snapshot := s.snapshot
	s.mu.Unlock()
	bodyRaw, err := buildStreamFinishBody(s.elementID, snapshot, body)
	if err != nil {
		return StreamResult{}, err
	}
	payload := streamFinishPayload{
		CardID:   s.CardID(),
		Action:   streamActionFinish,
		Sequence: sequence,
		Body:     bodyRaw,
	}
	response, err := s.service.post(ctx, dcinternal.RouteModifyStreaming, payload, OperationStreamFinish)
	if err != nil {
		return StreamResult{}, s.handleStreamFailure(response, err, OperationStreamFinish, sequence)
	}
	return s.applySuccess(response, OperationStreamFinish, sequence, StreamStateFinished)
}

// finishShortCircuit returns the idempotent result of a finished session and a
// local state error for expired or failed sessions.
func (s *DynamicCardStream) finishShortCircuit() (StreamResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshExpiredLocked()
	switch s.state {
	case StreamStateFinished:
		return s.resultLocked(), true, nil
	case StreamStateExpired, StreamStateFailed:
		return StreamResult{}, true, s.stateFailureLocked(OperationStreamFinish)
	default:
		return StreamResult{}, false, nil
	}
}

func (s *DynamicCardStream) runAppend(ctx context.Context, flight *streamFlight) (StreamResult, error) {
	release, err := streamCardGate.Lock(ctx, s.CardID())
	if err != nil {
		s.completeFlight(flight, StreamResult{}, err)
		return StreamResult{}, err
	}
	defer release()

	if err := s.waitThrottle(ctx); err != nil {
		s.completeFlight(flight, StreamResult{}, err)
		return StreamResult{}, err
	}
	if err := s.checkAppendable(OperationStreamAppend); err != nil {
		s.completeFlight(flight, StreamResult{}, err)
		return StreamResult{}, err
	}

	s.mu.Lock()
	snapshot := flight.snapshot
	sequence := s.sequence
	s.mu.Unlock()

	result, err := s.sendAppend(ctx, flight, snapshot, sequence)
	s.completeFlight(flight, result, err)
	return result, err
}

func (s *DynamicCardStream) sendAppend(ctx context.Context, flight *streamFlight, snapshot string, sequence int64) (StreamResult, error) {
	for attempt := 0; ; attempt++ {
		payload := streamAppendPayload{
			CardID:    s.CardID(),
			ElementID: s.elementID,
			Sequence:  sequence,
			Chunk:     snapshot,
		}
		response, err := s.service.post(ctx, dcinternal.RouteStreamingAppend, payload, OperationStreamAppend)
		if err == nil {
			return s.applySuccess(response, OperationStreamAppend, sequence, StreamStateStreaming)
		}
		failure, ok := err.(*DynamicCardError)
		if ok && failure.IsRateLimited() && attempt == 0 {
			// One retry only, and only the newest snapshot is resent. The
			// sequence is never changed by a 6001 response.
			if sleepErr := s.sleep(ctx, streamRetryBackoff); sleepErr != nil {
				return StreamResult{}, sleepErr
			}
			s.mu.Lock()
			snapshot = flight.snapshot
			s.mu.Unlock()
			continue
		}
		return StreamResult{}, s.handleStreamFailure(response, err, OperationStreamAppend, sequence)
	}
}

// streamRetryBackoff is the fixed 6001 backoff. The shared transport does not
// expose the Retry-After header, so the contract's fixed 1000ms fallback is
// always used.
const streamRetryBackoff = 1000 * time.Millisecond

func (s *DynamicCardStream) waitThrottle(ctx context.Context) error {
	s.mu.Lock()
	throttle := s.throttle
	last := s.lastSent
	now := s.now()
	s.mu.Unlock()
	if throttle <= 0 || last.IsZero() {
		return nil
	}
	if wait := throttle - now.Sub(last); wait > 0 {
		return s.sleep(ctx, wait)
	}
	return nil
}

func (s *DynamicCardStream) completeFlight(flight *streamFlight, result StreamResult, err error) {
	s.mu.Lock()
	waiters := flight.waiters
	flight.waiters = nil
	if s.flight == flight {
		s.flight = nil
	}
	s.mu.Unlock()
	for _, waiter := range waiters {
		waiter <- streamOutcome{result: result, err: err}
	}
}

func (s *DynamicCardStream) checkAppendable(operation Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshExpiredLocked()
	switch s.state {
	case StreamStateCreated, StreamStateStreaming:
		return nil
	default:
		return s.stateFailureLocked(operation)
	}
}

func (s *DynamicCardStream) checkOperable(operation Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshExpiredLocked()
	switch s.state {
	case StreamStateCreated, StreamStateStreaming:
		return nil
	default:
		return s.stateFailureLocked(operation)
	}
}

func (s *DynamicCardStream) stateFailureLocked(operation Operation) *DynamicCardError {
	message := fmt.Sprintf("stream is %s", s.state)
	switch s.state {
	case StreamStateFinished:
		message = "stream is already finished"
	case StreamStateExpired:
		message = "stream expired after the 60 minute platform lifetime"
	case StreamStateFailed:
		message = "stream is in the failed state"
	}
	return &DynamicCardError{
		Category:  CategoryStreamState,
		Message:   message,
		Operation: operation,
		CardID:    s.cardID,
	}
}

func (s *DynamicCardStream) refreshExpiredLocked() bool {
	if s.state != StreamStateCreated && s.state != StreamStateStreaming {
		return s.state == StreamStateExpired
	}
	if s.now().Sub(s.startedAt) >= StreamLifetime {
		s.state = StreamStateExpired
		return true
	}
	return false
}

func (s *DynamicCardStream) applySuccess(response CardResponse, operation Operation, requestSequence int64, nextState StreamState) (StreamResult, error) {
	sequence, cardID, err := parseStreamSequence(response, s.CardID(), requestSequence, operation)
	if err != nil {
		s.mu.Lock()
		s.state = StreamStateFailed
		s.mu.Unlock()
		return StreamResult{}, err
	}
	s.mu.Lock()
	s.cardID = cardID
	s.sequence = sequence
	s.state = nextState
	s.lastRaw = response.RawJSON()
	s.lastSent = s.now()
	result := s.resultLocked()
	s.mu.Unlock()
	return result, nil
}

func (s *DynamicCardStream) resultLocked() StreamResult {
	return StreamResult{
		CardID:   s.cardID,
		Sequence: s.sequence,
		State:    s.state,
		Raw:      append(json.RawMessage(nil), s.lastRaw...),
	}
}

func parseStreamSequence(response CardResponse, cardID string, requestSequence int64, operation Operation) (int64, string, error) {
	if len(response.Payload) == 0 {
		return 0, "", missingResponseField("business payload", operation, response)
	}
	var payload struct {
		CardID   json.RawMessage `json:"card_id"`
		Sequence json.RawMessage `json:"sequence"`
	}
	if err := json.Unmarshal(response.Payload, &payload); err != nil {
		return 0, "", missingResponseField("business data", operation, response)
	}
	sequence, present, err := parseSequence(payload.Sequence)
	if err != nil || !present {
		return 0, "", &DynamicCardError{
			Category:        CategoryServer,
			Message:         "protocol: successful streaming response is missing a numeric sequence",
			HTTPStatus:      response.HTTPStatus,
			Operation:       operation,
			RequestSequence: &requestSequence,
			LogID:           response.LogID,
			Raw:             response.RawJSON(),
		}
	}
	resolvedCardID := scalarJSONValue(payload.CardID)
	if resolvedCardID == "" {
		resolvedCardID = cardID
	}
	return sequence, resolvedCardID, nil
}

func (s *DynamicCardStream) handleStreamFailure(response CardResponse, err error, operation Operation, requestSequence int64) error {
	failure, ok := err.(*DynamicCardError)
	if !ok {
		return err
	}
	s.mu.Lock()
	failure.CardID = s.cardID
	s.mu.Unlock()
	if failure.Operation == "" {
		failure.Operation = operation
	}
	switch {
	case failure.IsSequenceConflict():
		s.adoptSequenceConflict(failure, response, requestSequence)
	case failure.IsStreamState():
		s.applyStreamStateConflict(failure, operation)
	case failure.IsValidation(), failure.IsOwnership(), failure.IsAuth():
		s.mu.Lock()
		s.state = StreamStateFailed
		s.mu.Unlock()
	}
	return failure
}

func (s *DynamicCardStream) adoptSequenceConflict(failure *DynamicCardError, response CardResponse, requestSequence int64) {
	source := response.Raw
	if len(source) == 0 {
		source = response.Payload
	}
	if raw := firstObjectField(source, "request_sequence"); raw != nil {
		if value, err := strconv.ParseInt(scalarJSONValue(raw), 10, 64); err == nil {
			failure.RequestSequence = &value
		}
	}
	if failure.RequestSequence == nil {
		value := requestSequence
		failure.RequestSequence = &value
	}
	if raw := firstObjectField(source, "current_sequence"); raw != nil {
		if value, err := strconv.ParseInt(scalarJSONValue(raw), 10, 64); err == nil {
			failure.CurrentSequence = &value
			s.mu.Lock()
			s.sequence = value
			s.mu.Unlock()
		}
	}
	if raw := firstObjectField(source, "current_msg_content"); raw != nil {
		var content DynamicCardContent
		if err := json.Unmarshal(raw, &content); err == nil {
			failure.CurrentContent = &content
		}
	}
}

func (s *DynamicCardStream) applyStreamStateConflict(failure *DynamicCardError, operation Operation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	expired := s.refreshExpiredLocked()
	message := strings.ToLower(failure.Message)
	switch {
	case operation == OperationStreamEnable:
		s.state = StreamStateFailed
	case strings.Contains(message, "is opened now"):
		s.state = StreamStateFailed
	case s.state == StreamStateCreated:
		s.state = StreamStateFailed
	case expired || s.state == StreamStateExpired:
		s.state = StreamStateExpired
	default:
		s.state = StreamStateExpired
	}
}

// buildStreamFinishBody renders the finish body from the caller's children or
// the session snapshot, marks any agent-flow-stream structure completed, and
// guarantees the emitted object has no scheme field.
func buildStreamFinishBody(elementID, snapshot string, body *StreamBody) (json.RawMessage, error) {
	var children ComponentList
	if body != nil {
		children = body.Children
	}
	if len(children) == 0 {
		children = ComponentList{NewMarkdown(elementID, snapshot)}
	}
	raw, err := json.Marshal(StreamBody{Children: children})
	if err != nil {
		return nil, newValidationError("body", "cannot be encoded: "+err.Error())
	}
	if fields := firstObjectField(raw, "scheme"); fields != nil {
		return nil, newValidationError("body.scheme", "must not be set")
	}
	return completeAgentFlowStream(raw)
}

// completeAgentFlowStream locates agent-flow-stream structures in the body,
// including a JSON skeleton embedded in a markdown content string, and sets
// their status to completed. A body without that structure is unchanged.
func completeAgentFlowStream(raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil, newValidationError("body", "cannot be decoded: "+err.Error())
	}
	if !completeAgentFlowNode(root) {
		return raw, nil
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, newValidationError("body", "cannot be encoded: "+err.Error())
	}
	return encoded, nil
}

func completeAgentFlowNode(node any) bool {
	switch typed := node.(type) {
	case map[string]any:
		changed := false
		if kind, _ := typed["type"].(string); kind == "agent-flow-stream" {
			typed["status"] = "completed"
			changed = true
		}
		for key, value := range typed {
			if key == "content" {
				if text, ok := value.(string); ok {
					if updated, ok := completeAgentFlowString(text); ok {
						typed[key] = updated
						changed = true
						continue
					}
					if updated, ok := completeAgentFlowMarkdown(text); ok {
						typed[key] = updated
						changed = true
					}
					continue
				}
			}
			if completeAgentFlowNode(value) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, item := range typed {
			if completeAgentFlowNode(item) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

func completeAgentFlowString(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) < 2 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return "", false
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	if !completeAgentFlowNode(value) {
		return "", false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

const agentFlowStreamFence = "```agent-flow-stream"

var agentFlowStatusPattern = regexp.MustCompile(`\bstatus="[^"]*"`)

// completeAgentFlowMarkdown rewrites the fence-header status of every
// agent-flow-stream block inside a markdown content string. Content without
// that fence, or whose fence header has no status attribute, is unchanged.
func completeAgentFlowMarkdown(content string) (string, bool) {
	if !strings.Contains(content, agentFlowStreamFence) {
		return content, false
	}
	lines := strings.Split(content, "\n")
	changed := false
	for index, line := range lines {
		rewritten := rewriteAgentFlowStreamFenceHeader(line)
		if rewritten != line {
			lines[index] = rewritten
			changed = true
		}
	}
	if !changed {
		return content, false
	}
	return strings.Join(lines, "\n"), true
}

// rewriteAgentFlowStreamFenceHeader sets the first status attribute of an
// exact agent-flow-stream fence line to completed. Every other byte, including
// the indent and the remaining attributes, is preserved.
func rewriteAgentFlowStreamFenceHeader(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, agentFlowStreamFence) {
		return line
	}
	rest := trimmed[len(agentFlowStreamFence):]
	if rest != "" && rest[0] != ' ' {
		return line
	}
	location := agentFlowStatusPattern.FindStringIndex(rest)
	if location == nil {
		return line
	}
	indent := line[:len(line)-len(trimmed)]
	return indent + agentFlowStreamFence + rest[:location[0]] + `status="completed"` + rest[location[1]:]
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
