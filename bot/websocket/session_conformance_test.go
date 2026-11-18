package websocket_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dolfly/infoflow/bot/events"
	"github.com/dolfly/infoflow/bot/internal/realtime"
	infows "github.com/dolfly/infoflow/bot/websocket"
)

func boolPointer(value bool) *bool                       { return &value }
func intPointer(value int) *int                          { return &value }
func durationPointer(value time.Duration) *time.Duration { return &value }

func eventually(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		runtime.Gosched()
	}
}

type captureRecords struct {
	mu      sync.Mutex
	records []realtime.Diagnostic
}

func (capture *captureRecords) Observe(record realtime.Diagnostic) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.records = append(capture.records, record)
}

func (capture *captureRecords) snapshot() []realtime.Diagnostic {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return append([]realtime.Diagnostic(nil), capture.records...)
}

func (capture *captureRecords) has(event, errorType string) bool {
	for _, record := range capture.snapshot() {
		if record.Event == event && (errorType == "" || record.ErrorType == errorType) {
			return true
		}
	}
	return false
}

type fakeTimer struct {
	delay    time.Duration
	callback func()
	stopped  atomic.Bool
	fired    atomic.Bool
}

func (timer *fakeTimer) Stop() bool { return !timer.stopped.Swap(true) }

func (timer *fakeTimer) Fire() bool {
	if timer.stopped.Load() || timer.fired.Swap(true) {
		return false
	}
	timer.callback()
	return true
}

func (timer *fakeTimer) ForceFire() {
	if !timer.fired.Swap(true) {
		timer.callback()
	}
}

type fakeScheduler struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

func (scheduler *fakeScheduler) AfterFunc(delay time.Duration, callback func()) infows.Timer {
	timer := &fakeTimer{delay: delay, callback: callback}
	scheduler.mu.Lock()
	scheduler.timers = append(scheduler.timers, timer)
	scheduler.mu.Unlock()
	return timer
}

func (scheduler *fakeScheduler) snapshot() []*fakeTimer {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return append([]*fakeTimer(nil), scheduler.timers...)
}

func (scheduler *fakeScheduler) nextActive(delay time.Duration) *fakeTimer {
	for _, timer := range scheduler.snapshot() {
		if !timer.stopped.Load() && !timer.fired.Load() && (delay < 0 || timer.delay == delay) {
			return timer
		}
	}
	return nil
}

type recordingTransport struct {
	writes    chan []byte
	closed    atomic.Bool
	writeHook func(context.Context, []byte) error
	active    atomic.Int32
	maxActive atomic.Int32
}

func newRecordingTransport() *recordingTransport {
	return &recordingTransport{writes: make(chan []byte, 128)}
}

func (transport *recordingTransport) Write(ctx context.Context, payload []byte) error {
	active := transport.active.Add(1)
	for {
		maximum := transport.maxActive.Load()
		if active <= maximum || transport.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	defer transport.active.Add(-1)
	if transport.writeHook != nil {
		if err := transport.writeHook(ctx, payload); err != nil {
			return err
		}
	}
	transport.writes <- bytes.Clone(payload)
	return nil
}

func (transport *recordingTransport) Close() error {
	transport.closed.Store(true)
	return nil
}

func (transport *recordingTransport) nextFrame(t *testing.T) *infows.Frame {
	t.Helper()
	select {
	case encoded := <-transport.writes:
		frame, err := infows.DecodeFrame(encoded)
		if err != nil {
			t.Fatalf("DecodeFrame(write) error = %v", err)
		}
		return frame
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for transport write")
		return nil
	}
}

type dialAttempt struct {
	url       string
	options   infows.DialOptions
	callbacks infows.TransportCallbacks
	transport *recordingTransport
}

type dialScript struct {
	transport    *recordingTransport
	error        error
	wait         <-chan struct{}
	ignoreCancel bool
}

type scriptedDialer struct {
	mu       sync.Mutex
	scripts  []dialScript
	attempts []dialAttempt
	started  chan struct{}
}

func (dialer *scriptedDialer) Dial(ctx context.Context, endpointURL string, options infows.DialOptions, callbacks infows.TransportCallbacks) (infows.Transport, error) {
	dialer.mu.Lock()
	index := len(dialer.attempts)
	script := dialScript{transport: newRecordingTransport()}
	if index < len(dialer.scripts) {
		script = dialer.scripts[index]
		if script.transport == nil {
			script.transport = newRecordingTransport()
		}
	}
	dialer.attempts = append(dialer.attempts, dialAttempt{
		url: endpointURL, options: options, callbacks: callbacks, transport: script.transport,
	})
	dialer.mu.Unlock()
	if dialer.started != nil {
		select {
		case dialer.started <- struct{}{}:
		default:
		}
	}
	if script.wait != nil {
		if script.ignoreCancel {
			<-script.wait
		} else {
			select {
			case <-script.wait:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	if script.error != nil {
		return nil, script.error
	}
	return script.transport, nil
}

func (dialer *scriptedDialer) snapshot() []dialAttempt {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	return append([]dialAttempt(nil), dialer.attempts...)
}

type scriptedAllocator struct {
	mu        sync.Mutex
	endpoints []infows.Endpoint
	errors    []error
	calls     int
}

func (allocator *scriptedAllocator) Allocate(context.Context) (infows.Endpoint, error) {
	allocator.mu.Lock()
	defer allocator.mu.Unlock()
	index := allocator.calls
	allocator.calls++
	if index < len(allocator.errors) && allocator.errors[index] != nil {
		return infows.Endpoint{}, allocator.errors[index]
	}
	if index >= len(allocator.endpoints) {
		return infows.Endpoint{}, errors.New("fixture endpoint unavailable")
	}
	return allocator.endpoints[index], nil
}

func (allocator *scriptedAllocator) count() int {
	allocator.mu.Lock()
	defer allocator.mu.Unlock()
	return allocator.calls
}

func newSessionFixture(t *testing.T, options infows.SessionOptions) (*infows.Session, *scriptedAllocator, *scriptedDialer, *fakeScheduler) {
	t.Helper()
	allocator, ok := options.EndpointAllocator.(*scriptedAllocator)
	if !ok || allocator == nil {
		allocator = &scriptedAllocator{endpoints: []infows.Endpoint{{URL: "wss://one.example.test/ws?connection_id=one"}}}
		options.EndpointAllocator = allocator
	}
	dialer, ok := options.Dialer.(*scriptedDialer)
	if !ok || dialer == nil {
		dialer = &scriptedDialer{}
		options.Dialer = dialer
	}
	scheduler, ok := options.Scheduler.(*fakeScheduler)
	if !ok || scheduler == nil {
		scheduler = &fakeScheduler{}
		options.Scheduler = scheduler
	}
	if options.Dispatcher == nil {
		options.Dispatcher = events.NewDispatcher(events.Options{})
	}
	if options.AutoRegister == nil {
		options.AutoRegister = boolPointer(false)
	}
	session, err := infows.NewSession(options)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	return session, allocator, dialer, scheduler
}

func connectFixture(t *testing.T, session *infows.Session, dialer *scriptedDialer) dialAttempt {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := session.Connect(ctx); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	attempts := dialer.snapshot()
	if len(attempts) == 0 {
		t.Fatal("no dial attempt")
	}
	return attempts[len(attempts)-1]
}

func encodeFixtureFrame(t *testing.T, frame *infows.Frame) []byte {
	t.Helper()
	encoded, err := infows.EncodeFrame(frame)
	if err != nil {
		t.Fatalf("EncodeFrame() error = %v", err)
	}
	return encoded
}

func TestSessionDataDispatchAckHeartbeatEncryptAndObservability(t *testing.T) {
	const (
		endpointURL  = "wss://private.example.test/ws?connection_id=seed-private-connection"
		userContent  = "seed-private-user-message"
		encryptValue = `{"MsgType":"text","content":"seed-private-user-message","messageid":9007199254740993123}`
	)
	pingInterval := 7 * time.Second
	allocator := &scriptedAllocator{endpoints: []infows.Endpoint{{
		URL: endpointURL, Config: infows.ServerConfig{PingInterval: &pingInterval},
	}}}
	capture := &captureRecords{}
	observer := realtime.Observers(
		realtime.DiagnosticHandler(func(realtime.Diagnostic) { panic("observer failure") }), capture,
	)
	dispatcher := events.NewDispatcher(events.Options{Observer: observer})
	handlerStarted := make(chan events.Event, 1)
	releaseHandler := make(chan struct{})
	if err := dispatcher.On("private.text", func(_ context.Context, event events.Event) error {
		handlerStarted <- event
		<-releaseHandler
		return errors.New("seed-private-handler-error")
	}); err != nil {
		t.Fatalf("On() error = %v", err)
	}
	heartbeats := make(chan events.Event, 4)
	_ = dispatcher.On("heartbeat", func(_ context.Context, event events.Event) error {
		heartbeats <- event
		return nil
	})
	connected := make(chan events.Event, 1)
	_ = dispatcher.On("connected", func(_ context.Context, event events.Event) error {
		connected <- event
		return nil
	})
	session, _, dialer, scheduler := newSessionFixture(t, infows.SessionOptions{
		Dispatcher: dispatcher, EndpointAllocator: allocator, Observer: observer,
		HeartbeatInterval: time.Hour,
	})
	attempt := connectFixture(t, session, dialer)
	select {
	case event := <-connected:
		if event.Payload()["connectionId"] != "seed-private-connection" {
			t.Fatalf("connected payload = %#v", event.Payload())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("connected event not dispatched")
	}
	ping := attempt.transport.nextFrame(t)
	if infows.Method(ping.GetMethod()) != infows.MethodControl || ping.GetHeaders()[0].GetValue() != "ping" {
		t.Fatalf("immediate ping = %#v", ping)
	}
	eventually(t, func() bool {
		return scheduler.nextActive(pingInterval) != nil
	}, "server heartbeat interval")
	if !scheduler.nextActive(pingInterval).Fire() {
		t.Fatal("scheduled heartbeat did not fire")
	}
	scheduledPing := attempt.transport.nextFrame(t)
	if header := scheduledPing.GetHeaders()[0].GetValue(); header != "ping" {
		t.Fatalf("scheduled header = %q", header)
	}

	outer := []byte(`{"Encrypt":` + fmt.Sprintf("%q", encryptValue) + `,"opaque":"seed-private-outer"}`)
	data := &infows.Frame{
		SeqId: 9007199254740993123, LogId: "platform-log-42", Method: int32(infows.MethodData),
		Payload: outer,
	}
	attempt.callbacks.OnMessage(encodeFixtureFrame(t, data))
	var delivered events.Event
	select {
	case delivered = <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not submitted")
	}
	ack := attempt.transport.nextFrame(t)
	if headerValue := ack.GetHeaders()[0].GetValue(); headerValue != "event_ack" {
		t.Fatalf("ACK header = %q", headerValue)
	}
	if ack.GetLogId() != data.GetLogId() || infows.Method(ack.GetMethod()) != infows.MethodData {
		t.Fatalf("ACK = %#v", ack)
	}
	ackPayload, err := infows.DecodePayload(ack.GetPayload())
	if err != nil || ackPayload["ackSeqId"] != json.Number("9007199254740993123") {
		t.Fatalf("ACK payload=%#v err=%v", ackPayload, err)
	}
	if !bytes.Equal(delivered.RawPayload(), outer) || delivered.Payload()["messageid"] != json.Number("9007199254740993123") {
		t.Fatalf("delivered raw/payload = %s %#v", delivered.RawPayload(), delivered.Payload())
	}
	close(releaseHandler)
	eventually(t, func() bool { return capture.has("ws.ack.success", "") }, "ACK observer success")

	pong := &infows.Frame{
		SeqId: 77, Method: int32(infows.MethodControl), Headers: []*infows.Header{{Key: "type", Value: "pong"}},
		Payload: []byte(`{"server":"fixture"}`),
	}
	attempt.callbacks.OnMessage(encodeFixtureFrame(t, pong))
	pongEvent := <-heartbeats
	for pongEvent.Payload()["type"] != "pong" {
		pongEvent = <-heartbeats
	}
	if pongEvent.Payload()["seqId"] != json.Number("77") {
		t.Fatalf("pong payload = %#v", pongEvent.Payload())
	}

	records := capture.snapshot()
	for i, record := range records {
		if record.Event == "" || record.Level == "" || record.Timestamp.IsZero() || record.SDKLanguage != "go" ||
			record.Component == "" || record.RequestID == "" || record.Redaction != realtime.RedactionPolicy {
			t.Fatalf("record[%d] missing required fields: %#v", i, record)
		}
	}
	var ackRecord *realtime.Diagnostic
	for i := range records {
		if records[i].Event == "ws.ack.success" {
			ackRecord = &records[i]
		}
	}
	if ackRecord == nil || ackRecord.PlatformLogID != "platform-log-42" || ackRecord.SeqID != data.GetSeqId() {
		t.Fatalf("ACK record = %#v", ackRecord)
	}
	var connectStart, connectSuccess *realtime.Diagnostic
	for i := range records {
		switch records[i].Event {
		case "ws.connect.start":
			connectStart = &records[i]
		case "ws.connect.success":
			connectSuccess = &records[i]
		}
	}
	if connectStart == nil || connectSuccess == nil || connectStart.RequestID != connectSuccess.RequestID {
		t.Fatalf("connect records are not correlated: start=%#v success=%#v", connectStart, connectSuccess)
	}
	serialized, _ := json.Marshal(records)
	for _, sensitive := range []string{
		endpointURL, "seed-private-connection", userContent, encryptValue, string(outer),
		"seed-private-outer", "seed-private-handler-error",
	} {
		if strings.Contains(string(serialized), sensitive) {
			t.Fatalf("records leaked %q: %s", sensitive, serialized)
		}
	}
}

func TestSessionReceiveAndSendQueuePressureHaveExplicitOutcomes(t *testing.T) {
	t.Run("receive overflow reconnects", func(t *testing.T) {
		capture := &captureRecords{}
		session, _, dialer, scheduler := newSessionFixture(t, infows.SessionOptions{
			Observer: capture,
			Queues:   infows.QueueCapacities{Control: 1, Receive: 1, Callback: 1},
		})
		attempt := connectFixture(t, session, dialer)
		_ = attempt.transport.nextFrame(t)
		encoded := encodeFixtureFrame(t, &infows.Frame{
			SeqId: 1, Method: int32(infows.MethodData), Payload: []byte(`{"MsgType":"text"}`),
		})
		var producers sync.WaitGroup
		for range 128 {
			producers.Add(1)
			go func() {
				defer producers.Done()
				attempt.callbacks.OnMessage(encoded)
			}()
		}
		eventually(t, func() bool {
			return capture.has("ws.close", "receive_queue_full") && session.State() == infows.StateReconnecting
		}, "receive overflow reconnect")
		producers.Wait()
		if !attempt.transport.closed.Load() || scheduler.nextActive(120*time.Second) == nil {
			t.Fatalf("overflow transport closed=%v timers=%#v", attempt.transport.closed.Load(), scheduler.snapshot())
		}
	})

	t.Run("send and writer overflow return errors and writes serialize", func(t *testing.T) {
		transport := newRecordingTransport()
		dialer := &scriptedDialer{scripts: []dialScript{{transport: transport}}}
		session, _, _, _ := newSessionFixture(t, infows.SessionOptions{
			Dialer: dialer, WriteTimeout: time.Second,
			Queues: infows.QueueCapacities{Send: 1, Writer: 1},
		})
		connectFixture(t, session, dialer)
		_ = transport.nextFrame(t)
		block := make(chan struct{})
		transport.writeHook = func(ctx context.Context, _ []byte) error {
			select {
			case <-block:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		const sends = 64
		results := make(chan error, sends)
		for index := range sends {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				results <- session.SendData(ctx, map[string]any{"index": index})
			}()
		}
		var firstError error
		select {
		case firstError = <-results:
		case <-time.After(2 * time.Second):
			t.Fatal("no send result under pressure")
		}
		if firstError == nil {
			select {
			case firstError = <-results:
			case <-time.After(2 * time.Second):
				t.Fatal("no explicit send overflow")
			}
		}
		var target *infows.SessionError
		if !errors.As(firstError, &target) || (target.Category != "queue_full" && target.Category != "writer_queue_full") {
			t.Fatalf("pressure error = %T %#v", firstError, firstError)
		}
		close(block)
		for received := 2; received < sends; received++ {
			<-results
		}
		if transport.maxActive.Load() != 1 {
			t.Fatalf("max concurrent writes = %d", transport.maxActive.Load())
		}
	})
}

func TestSessionReconnectMaxAttemptsAndConnectTimeoutLateCleanup(t *testing.T) {
	t.Run("server max attempts", func(t *testing.T) {
		interval := time.Second
		maximum := 2
		allocator := &scriptedAllocator{endpoints: []infows.Endpoint{
			{URL: "wss://initial.example.test/ws", Config: infows.ServerConfig{
				ReconnectCount: &maximum, ReconnectInterval: &interval,
			}},
			{URL: "wss://retry-one.example.test/ws"},
			{URL: "wss://retry-two.example.test/ws"},
		}}
		dialer := &scriptedDialer{scripts: []dialScript{
			{transport: newRecordingTransport()},
			{error: errors.New("seed-private-dial-one")},
			{error: errors.New("seed-private-dial-two")},
		}}
		capture := &captureRecords{}
		session, _, _, scheduler := newSessionFixture(t, infows.SessionOptions{
			EndpointAllocator: allocator, Dialer: dialer, Observer: capture,
			Jitter: func(time.Duration) time.Duration { return 0 },
		})
		first := connectFixture(t, session, dialer)
		_ = first.transport.nextFrame(t)
		first.callbacks.OnClose()
		for attempt := 1; attempt <= maximum; attempt++ {
			var timer *fakeTimer
			eventually(t, func() bool {
				timer = scheduler.nextActive(interval)
				return timer != nil
			}, fmt.Sprintf("reconnect timer %d", attempt))
			timer.Fire()
		}
		eventually(t, func() bool { return session.State() == infows.StateDisconnected }, "max reconnect exhaustion")
		if allocator.count() != 3 || len(dialer.snapshot()) != 3 {
			t.Fatalf("attempts allocations=%d dials=%d", allocator.count(), len(dialer.snapshot()))
		}
		var scheduled []int
		for _, record := range capture.snapshot() {
			if record.Event == "ws.reconnect.schedule" {
				scheduled = append(scheduled, record.Attempt)
			}
		}
		if fmt.Sprint(scheduled) != "[1 2]" {
			t.Fatalf("reconnect attempts = %v", scheduled)
		}
	})

	t.Run("connect timeout closes late transport", func(t *testing.T) {
		release := make(chan struct{})
		late := newRecordingTransport()
		dialer := &scriptedDialer{scripts: []dialScript{{transport: late, wait: release, ignoreCancel: true}}}
		capture := &captureRecords{}
		session, _, _, _ := newSessionFixture(t, infows.SessionOptions{
			Dialer: dialer, ConnectTimeout: 20 * time.Millisecond, Observer: capture,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if err := session.Connect(ctx); err == nil {
			t.Fatal("Connect() timeout error = nil")
		}
		eventually(t, func() bool { return session.State() == infows.StateDisconnected }, "timeout disconnected")
		close(release)
		eventually(t, late.closed.Load, "timeout late transport close")
		eventually(t, func() bool { return capture.has("ws.connect.failure", "command_cancelled") }, "connect failure observer")
	})

	t.Run("local minus one keeps reconnecting until success", func(t *testing.T) {
		allocator := &scriptedAllocator{endpoints: []infows.Endpoint{
			{URL: "wss://initial.example.test/ws"},
			{URL: "wss://retry-one.example.test/ws"},
			{URL: "wss://retry-two.example.test/ws"},
			{URL: "wss://retry-three.example.test/ws"},
			{URL: "wss://eventual-success.example.test/ws"},
		}}
		dialer := &scriptedDialer{scripts: []dialScript{
			{transport: newRecordingTransport()},
			{error: errors.New("seed-private-dial-one")},
			{error: errors.New("seed-private-dial-two")},
			{error: errors.New("seed-private-dial-three")},
			{transport: newRecordingTransport()},
		}}
		session, _, _, scheduler := newSessionFixture(t, infows.SessionOptions{
			EndpointAllocator: allocator, Dialer: dialer,
		})
		first := connectFixture(t, session, dialer)
		_ = first.transport.nextFrame(t)
		first.callbacks.OnClose()
		eventually(t, func() bool { return session.State() == infows.StateReconnecting }, "unlimited reconnect start")
		for attempt := 1; attempt <= 4; attempt++ {
			var timer *fakeTimer
			eventually(t, func() bool {
				timer = scheduler.nextActive(120 * time.Second)
				return timer != nil
			}, fmt.Sprintf("unlimited reconnect timer %d", attempt))
			timer.Fire()
			eventually(t, func() bool { return allocator.count() >= attempt+1 }, fmt.Sprintf("unlimited allocation %d", attempt))
		}
		eventually(t, func() bool { return session.State() == infows.StateConnected }, "eventual unlimited reconnect success")
		if allocator.count() != 5 || len(dialer.snapshot()) != 5 {
			t.Fatalf("allocations=%d dials=%d", allocator.count(), len(dialer.snapshot()))
		}
	})
}

func TestSessionErrorLifecycleForConnectTransportAndHeartbeatFailures(t *testing.T) {
	newErrorDispatcher := func(t *testing.T) (*events.Dispatcher, chan events.Event) {
		t.Helper()
		dispatcher := events.NewDispatcher(events.Options{})
		delivered := make(chan events.Event, 4)
		if err := dispatcher.On("error", func(_ context.Context, event events.Event) error {
			delivered <- event
			return nil
		}); err != nil {
			t.Fatalf("On(error) error = %v", err)
		}
		return dispatcher, delivered
	}
	assertCategory := func(t *testing.T, delivered <-chan events.Event, category string) {
		t.Helper()
		select {
		case event := <-delivered:
			if event.Payload()["category"] != category {
				t.Fatalf("error event = %#v", event.Payload())
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for error category %s", category)
		}
	}

	t.Run("phase two failure", func(t *testing.T) {
		dispatcher, delivered := newErrorDispatcher(t)
		dialer := &scriptedDialer{scripts: []dialScript{{error: errors.New("seed-private-dial-error")}}}
		session, _, _, _ := newSessionFixture(t, infows.SessionOptions{
			Dispatcher: dispatcher, Dialer: dialer,
		})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := session.Connect(ctx); err == nil {
			t.Fatal("Connect() error = nil")
		}
		assertCategory(t, delivered, "dial")
	})

	t.Run("transport error", func(t *testing.T) {
		dispatcher, delivered := newErrorDispatcher(t)
		session, _, dialer, _ := newSessionFixture(t, infows.SessionOptions{Dispatcher: dispatcher})
		attempt := connectFixture(t, session, dialer)
		_ = attempt.transport.nextFrame(t)
		attempt.callbacks.OnError()
		eventually(t, func() bool { return session.State() == infows.StateReconnecting }, "transport error reconnect")
		assertCategory(t, delivered, "transport_error")
	})

	t.Run("scheduled heartbeat write failure", func(t *testing.T) {
		dispatcher, delivered := newErrorDispatcher(t)
		transport := newRecordingTransport()
		var writes atomic.Int32
		transport.writeHook = func(context.Context, []byte) error {
			if writes.Add(1) == 2 {
				return errors.New("seed-private-heartbeat-write")
			}
			return nil
		}
		dialer := &scriptedDialer{scripts: []dialScript{{transport: transport}}}
		session, _, _, scheduler := newSessionFixture(t, infows.SessionOptions{
			Dispatcher: dispatcher, Dialer: dialer,
		})
		connectFixture(t, session, dialer)
		_ = transport.nextFrame(t)
		eventually(t, func() bool { return scheduler.nextActive(120*time.Second) != nil }, "heartbeat timer")
		scheduler.nextActive(120 * time.Second).Fire()
		eventually(t, func() bool { return session.State() == infows.StateReconnecting }, "heartbeat failure reconnect")
		assertCategory(t, delivered, "write_failure")
	})
}

func TestSessionConnectDomainEnvironmentAndStaleRegistrationTimer(t *testing.T) {
	registrar := &captureRegistrar{}
	session, _, dialer, scheduler := newSessionFixture(t, infows.SessionOptions{
		AutoRegister: boolPointer(true), Registrar: registrar,
		LookupEnv: func(name string) (string, bool) {
			if name == "INFOFLOW_WS_CONNECT_DOMAIN" {
				return "env-connect.example.test:9443", true
			}
			return "", false
		},
	})
	attempt := connectFixture(t, session, dialer)
	if attempt.url != "wss://env-connect.example.test:9443/ws?connection_id=one" ||
		attempt.options.Header.Get("X-Source") != "go_sdk" || !attempt.options.CompressionDisabled {
		t.Fatalf("phase2 attempt = %#v", attempt)
	}
	eventually(t, func() bool { return scheduler.nextActive(time.Second) != nil }, "registration timer")
	timer := scheduler.nextActive(time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	timer.ForceFire()
	runtime.Gosched()
	if registrar.count() != 0 {
		t.Fatalf("stale registration attempts = %d", registrar.count())
	}
}

func TestSessionAckPolicyMalformedEncryptAndCallbackPressure(t *testing.T) {
	capture := &captureRecords{}
	dispatcher := events.NewDispatcher(events.Options{})
	connected := make(chan struct{}, 1)
	_ = dispatcher.On("connected", func(context.Context, events.Event) error {
		connected <- struct{}{}
		return nil
	})
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var completed atomic.Int32
	_ = dispatcher.On("private.text", func(context.Context, events.Event) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		completed.Add(1)
		return nil
	})
	session, _, dialer, _ := newSessionFixture(t, infows.SessionOptions{
		Dispatcher: dispatcher, Observer: capture,
		Queues: infows.QueueCapacities{Callback: 1},
	})
	attempt := connectFixture(t, session, dialer)
	_ = attempt.transport.nextFrame(t) // immediate ping
	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		t.Fatal("connected callback did not drain")
	}

	for seq := uint64(1); seq <= 3; seq++ {
		payload := []byte(fmt.Sprintf(`{"MsgType":"text","MsgId":"m%d"}`, seq))
		attempt.callbacks.OnMessage(encodeFixtureFrame(t, &infows.Frame{
			SeqId: seq, LogId: fmt.Sprintf("log-%d", seq), Method: int32(infows.MethodData), Payload: payload,
		}))
		if seq == 1 {
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("first callback did not start")
			}
		}
	}
	firstACK := attempt.transport.nextFrame(t)
	secondACK := attempt.transport.nextFrame(t)
	if firstACK.GetHeaders()[0].GetValue() != "event_ack" || secondACK.GetHeaders()[0].GetValue() != "event_ack" {
		t.Fatalf("ACK frames = %#v %#v", firstACK, secondACK)
	}
	eventually(t, func() bool { return capture.has("ws.dispatch.failure", "callback_queue_full") }, "callback rejection")
	select {
	case extra := <-attempt.transport.writes:
		frame, _ := infows.DecodeFrame(extra)
		t.Fatalf("rejected callback unexpectedly ACKed: %#v", frame)
	default:
	}
	close(release)
	eventually(t, func() bool { return completed.Load() == 2 }, "accepted callbacks drain")

	malformedOuter := []byte(`{"Encrypt":"{broken","opaque":true}`)
	attempt.callbacks.OnMessage(encodeFixtureFrame(t, &infows.Frame{
		SeqId: 9, LogId: "malformed-log", Method: int32(infows.MethodData), Payload: malformedOuter,
	}))
	malformedACK := attempt.transport.nextFrame(t)
	if malformedACK.GetLogId() != "malformed-log" || session.State() != infows.StateConnected {
		t.Fatalf("malformed Encrypt ACK/state = %#v %s", malformedACK, session.State())
	}
	eventually(t, func() bool { return capture.has("ws.dispatch.failure", "malformed_encrypt") }, "malformed Encrypt diagnostic")

	attempt.callbacks.OnMessage([]byte{0xff})
	attempt.callbacks.OnMessage(encodeFixtureFrame(t, &infows.Frame{
		SeqId: 10, Method: int32(infows.MethodData), Payload: []byte(`{"broken"`),
	}))
	eventually(t, func() bool {
		return capture.has("ws.dispatch.failure", "malformed_frame") && capture.has("ws.dispatch.failure", "malformed_json")
	}, "malformed frame/json diagnostics")
	if session.State() != infows.StateConnected {
		t.Fatalf("malformed input changed state to %s", session.State())
	}
}

func TestSessionAutoAckDisabled(t *testing.T) {
	dispatcher := events.NewDispatcher(events.Options{})
	delivered := make(chan struct{}, 1)
	_ = dispatcher.On("private.text", func(context.Context, events.Event) error { delivered <- struct{}{}; return nil })
	session, _, dialer, _ := newSessionFixture(t, infows.SessionOptions{
		Dispatcher: dispatcher, AutoAck: boolPointer(false),
	})
	attempt := connectFixture(t, session, dialer)
	_ = attempt.transport.nextFrame(t)
	attempt.callbacks.OnMessage(encodeFixtureFrame(t, &infows.Frame{
		SeqId: 1, Method: int32(infows.MethodData), Payload: []byte(`{"MsgType":"text"}`),
	}))
	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("event not delivered")
	}
	select {
	case encoded := <-attempt.transport.writes:
		frame, _ := infows.DecodeFrame(encoded)
		t.Fatalf("autoAck=false wrote %#v", frame)
	default:
	}
}

func TestSessionReconnectFreshEndpointPolicyGenerationAndManualCancel(t *testing.T) {
	firstInterval, firstNonce, firstPing := 5*time.Second, 2*time.Second, 7*time.Second
	secondPing := 9 * time.Second
	reconnectCount := 2
	allocator := &scriptedAllocator{endpoints: []infows.Endpoint{
		{URL: "wss://one.example.test/ws?connection_id=one", Config: infows.ServerConfig{
			ReconnectCount: &reconnectCount, ReconnectInterval: &firstInterval,
			ReconnectNonce: &firstNonce, PingInterval: &firstPing,
		}},
		{URL: "wss://two.example.test/ws?connection_id=two", Config: infows.ServerConfig{PingInterval: &secondPing}},
		{URL: "wss://three.example.test/ws?connection_id=three"},
	}}
	capture := &captureRecords{}
	dispatcher := events.NewDispatcher(events.Options{})
	reconnectedEvent := make(chan events.Event, 1)
	_ = dispatcher.On("private.text", func(_ context.Context, event events.Event) error {
		reconnectedEvent <- event
		return nil
	})
	session, _, dialer, scheduler := newSessionFixture(t, infows.SessionOptions{
		EndpointAllocator: allocator, Observer: capture, Dispatcher: dispatcher,
		Jitter: func(max time.Duration) time.Duration {
			if max == 0 {
				return 0
			}
			if max != firstNonce {
				t.Errorf("jitter max = %v, want %v", max, firstNonce)
			}
			return time.Second
		},
	})
	first := connectFixture(t, session, dialer)
	_ = first.transport.nextFrame(t)
	go first.callbacks.OnClose()
	go first.callbacks.OnError()
	eventually(t, func() bool { return session.State() == infows.StateReconnecting }, "reconnecting state")
	eventually(t, func() bool { return scheduler.nextActive(6*time.Second) != nil }, "server reconnect interval+jitter")
	if count := 0; func() bool {
		for _, record := range capture.snapshot() {
			if record.Event == "ws.reconnect.schedule" {
				count++
			}
		}
		return count == 1
	}() == false {
		t.Fatalf("expected one reconnect schedule: %#v", capture.snapshot())
	}
	scheduler.nextActive(6 * time.Second).Fire()
	eventually(t, func() bool { return session.State() == infows.StateConnected && allocator.count() == 2 }, "fresh reconnect")
	attempts := dialer.snapshot()
	if len(attempts) != 2 || !strings.Contains(attempts[1].url, "two.example.test") {
		t.Fatalf("dial attempts = %#v", attempts)
	}
	second := attempts[1]
	_ = second.transport.nextFrame(t)
	eventually(t, func() bool { return scheduler.nextActive(secondPing) != nil }, "fresh endpoint ping policy")
	second.callbacks.OnMessage(encodeFixtureFrame(t, &infows.Frame{
		SeqId: 88, LogId: "reconnected-log", Method: int32(infows.MethodData),
		Payload: []byte(`{"MsgType":"text","content":"after-reconnect"}`),
	}))
	select {
	case event := <-reconnectedEvent:
		if event.Payload()["content"] != "after-reconnect" {
			t.Fatalf("reconnected event = %#v", event.Payload())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event was not dispatched after reconnect")
	}
	reconnectACK := second.transport.nextFrame(t)
	reconnectACKPayload, err := infows.DecodePayload(reconnectACK.GetPayload())
	if err != nil || reconnectACK.GetLogId() != "reconnected-log" || reconnectACKPayload["ackSeqId"] != json.Number("88") {
		t.Fatalf("reconnected ACK=%#v payload=%#v err=%v", reconnectACK, reconnectACKPayload, err)
	}

	// Every old-generation callback is ignored and cannot perturb transport 2.
	first.callbacks.OnMessage(encodeFixtureFrame(t, &infows.Frame{
		SeqId: 99, Method: int32(infows.MethodData), Payload: []byte(`{"MsgType":"text"}`),
	}))
	first.callbacks.OnClose()
	first.callbacks.OnError()
	runtime.Gosched()
	if session.State() != infows.StateConnected || allocator.count() != 2 {
		t.Fatalf("old generation changed state=%s allocations=%d", session.State(), allocator.count())
	}

	second.callbacks.OnClose()
	eventually(t, func() bool { return session.State() == infows.StateReconnecting }, "second reconnect wait")
	var reconnectTimer *fakeTimer
	eventually(t, func() bool {
		reconnectTimer = scheduler.nextActive(120 * time.Second)
		return reconnectTimer != nil
	}, "local fallback reconnect timer")
	if reconnectTimer == nil {
		var timerState []string
		for _, timer := range scheduler.snapshot() {
			timerState = append(timerState, fmt.Sprintf("%s stopped=%v fired=%v", timer.delay, timer.stopped.Load(), timer.fired.Load()))
		}
		t.Fatalf("missing local fallback reconnect timer: %v", timerState)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	reconnectTimer.ForceFire()
	runtime.Gosched()
	if session.State() != infows.StateDisconnected || allocator.count() != 2 {
		t.Fatalf("manual cancel state=%s allocations=%d", session.State(), allocator.count())
	}
	if err := session.Connect(ctx); err != nil {
		t.Fatalf("later Connect() error = %v", err)
	}
	if session.State() != infows.StateConnected || allocator.count() != 3 {
		t.Fatalf("later connect state=%s allocations=%d", session.State(), allocator.count())
	}
}

func TestSessionConcurrentConnectDisconnectDuringConnectAndLateOpenCleanup(t *testing.T) {
	release := make(chan struct{})
	lateTransport := newRecordingTransport()
	dialer := &scriptedDialer{
		started: make(chan struct{}, 2),
		scripts: []dialScript{{transport: lateTransport, wait: release, ignoreCancel: true}},
	}
	session, allocator, _, _ := newSessionFixture(t, infows.SessionOptions{Dialer: dialer})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			results <- session.Connect(ctx)
		}()
	}
	select {
	case <-dialer.started:
	case <-time.After(time.Second):
		t.Fatal("dial did not start")
	}
	eventually(t, func() bool { return session.State() == infows.StateConnecting }, "connecting state")
	if allocator.count() != 1 || len(dialer.snapshot()) != 1 {
		t.Fatalf("concurrent Connect allocated=%d dialed=%d", allocator.count(), len(dialer.snapshot()))
	}
	if err := session.SendData(context.Background(), map[string]any{"during": "connect"}); err == nil {
		t.Fatal("SendData() while connecting error = nil")
	} else {
		var target *infows.SessionError
		if !errors.As(err, &target) || target.Category != "not_connected" {
			t.Fatalf("SendData() while connecting error = %T %#v", err, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	for range 2 {
		if err := <-results; err == nil {
			t.Fatal("Connect unexpectedly succeeded after disconnect")
		}
	}
	close(release)
	eventually(t, lateTransport.closed.Load, "late old transport close")
	if session.State() != infows.StateDisconnected {
		t.Fatalf("late open changed state to %s", session.State())
	}
}

func TestSessionQueuedOldGenerationSendCannotReachReconnectedTransport(t *testing.T) {
	enteredObserver := make(chan struct{})
	releaseObserver := make(chan struct{})
	var blockOnce sync.Once
	observer := realtime.DiagnosticHandler(func(record realtime.Diagnostic) {
		if record.Event == "ws.frame.receive" {
			blockOnce.Do(func() {
				close(enteredObserver)
				<-releaseObserver
			})
		}
	})
	allocator := &scriptedAllocator{endpoints: []infows.Endpoint{
		{URL: "wss://old.example.test/ws"},
		{URL: "wss://new.example.test/ws"},
	}}
	session, _, dialer, scheduler := newSessionFixture(t, infows.SessionOptions{
		EndpointAllocator: allocator, Observer: observer,
		Queues: infows.QueueCapacities{Send: 1},
	})
	first := connectFixture(t, session, dialer)
	_ = first.transport.nextFrame(t)
	first.callbacks.OnMessage(encodeFixtureFrame(t, &infows.Frame{
		SeqId: 1, Method: int32(infows.MethodControl),
		Headers: []*infows.Header{{Key: "type", Value: "pong"}}, Payload: []byte("{}"),
	}))
	select {
	case <-enteredObserver:
	case <-time.After(2 * time.Second):
		t.Fatal("state owner did not enter blocking observer")
	}

	results := make(chan error, 2)
	for index := range 2 {
		go func(value int) {
			results <- session.SendData(context.Background(), map[string]any{"queued": value})
		}(index)
	}
	select {
	case err := <-results:
		var target *infows.SessionError
		if !errors.As(err, &target) || target.Category != "queue_full" {
			t.Fatalf("queue pressure error = %T %#v", err, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("send queue did not become full")
	}
	first.callbacks.OnClose()
	close(releaseObserver)
	select {
	case err := <-results:
		var target *infows.SessionError
		if !errors.As(err, &target) || (target.Category != "not_connected" && target.Category != "stale_generation") {
			t.Fatalf("queued old-generation send error = %T %#v", err, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued old-generation send did not complete")
	}
	eventually(t, func() bool { return session.State() == infows.StateReconnecting }, "queued-send reconnect")
	timer := scheduler.nextActive(120 * time.Second)
	if timer == nil || !timer.Fire() {
		t.Fatal("reconnect timer unavailable")
	}
	eventually(t, func() bool { return session.State() == infows.StateConnected }, "queued-send reconnect success")
	second := dialer.snapshot()[1]
	if ping := second.transport.nextFrame(t); infows.Method(ping.GetMethod()) != infows.MethodControl {
		t.Fatalf("new transport first write = %#v", ping)
	}
	select {
	case encoded := <-second.transport.writes:
		frame, _ := infows.DecodeFrame(encoded)
		t.Fatalf("old-generation send reached new transport: %#v", frame)
	default:
	}
}

func TestSessionOldGenerationReceiveFloodCannotStarveReconnectedTransport(t *testing.T) {
	firstReceiveEntered := make(chan struct{})
	releaseFirstReceive := make(chan struct{})
	secondConnectEntered := make(chan struct{})
	releaseSecondConnect := make(chan struct{})
	var firstReceiveOnce sync.Once
	var connectSuccesses atomic.Int32
	observer := realtime.DiagnosticHandler(func(record realtime.Diagnostic) {
		if record.Event == "ws.frame.receive" {
			firstReceiveOnce.Do(func() {
				close(firstReceiveEntered)
				<-releaseFirstReceive
			})
		}
		if record.Event == "ws.connect.success" && connectSuccesses.Add(1) == 2 {
			close(secondConnectEntered)
			<-releaseSecondConnect
		}
	})
	dispatcher := events.NewDispatcher(events.Options{})
	delivered := make(chan events.Event, 1)
	if err := dispatcher.On("private.text", func(_ context.Context, event events.Event) error {
		delivered <- event
		return nil
	}); err != nil {
		t.Fatalf("On() error = %v", err)
	}
	allocator := &scriptedAllocator{endpoints: []infows.Endpoint{
		{URL: "wss://old.example.test/ws"},
		{URL: "wss://new.example.test/ws"},
	}}
	session, _, dialer, scheduler := newSessionFixture(t, infows.SessionOptions{
		Dispatcher: dispatcher, EndpointAllocator: allocator, Observer: observer,
		Queues: infows.QueueCapacities{Control: 32, Receive: 512, Callback: 4},
	})
	first := connectFixture(t, session, dialer)
	_ = first.transport.nextFrame(t)
	pong := encodeFixtureFrame(t, &infows.Frame{
		SeqId: 1, Method: int32(infows.MethodControl),
		Headers: []*infows.Header{{Key: "type", Value: "pong"}}, Payload: []byte("{}"),
	})
	first.callbacks.OnMessage(pong)
	select {
	case <-firstReceiveEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("state owner did not enter first receive")
	}
	for index := range 512 {
		first.callbacks.OnMessage(encodeFixtureFrame(t, &infows.Frame{
			SeqId: uint64(index + 2), Method: int32(infows.MethodControl),
			Headers: []*infows.Header{{Key: "type", Value: "pong"}}, Payload: []byte("{}"),
		}))
	}
	first.callbacks.OnClose()
	close(releaseFirstReceive)
	eventually(t, func() bool { return session.State() == infows.StateReconnecting }, "old receive flood reconnect")
	var timer *fakeTimer
	eventually(t, func() bool {
		timer = scheduler.nextActive(120 * time.Second)
		return timer != nil
	}, "old receive flood reconnect timer")
	if !timer.Fire() {
		t.Fatal("reconnect timer unavailable")
	}
	select {
	case <-secondConnectEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("second connect did not reach observer")
	}
	second := dialer.snapshot()[1]
	second.callbacks.OnMessage(encodeFixtureFrame(t, &infows.Frame{
		SeqId: 99, Method: int32(infows.MethodData), Payload: []byte(`{"MsgType":"text"}`),
	}))
	close(releaseSecondConnect)
	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatalf("new-generation frame was starved; state=%s", session.State())
	}
	if session.State() != infows.StateConnected {
		t.Fatalf("new-generation receive overflow changed state=%s", session.State())
	}
}

func TestSessionAcceptsFrameDeliveredBeforeDialReturns(t *testing.T) {
	release := make(chan struct{})
	transport := newRecordingTransport()
	dialer := &scriptedDialer{
		started: make(chan struct{}, 1),
		scripts: []dialScript{{transport: transport, wait: release}},
	}
	delivered := make(chan events.Event, 1)
	dispatcher := events.NewDispatcher(events.Options{})
	if err := dispatcher.On("text", func(_ context.Context, event events.Event) error {
		delivered <- event
		return nil
	}); err != nil {
		t.Fatalf("On() error = %v", err)
	}
	session, _, _, _ := newSessionFixture(t, infows.SessionOptions{
		Dialer: dialer, Dispatcher: dispatcher,
	})
	connectResult := make(chan error, 1)
	go func() { connectResult <- session.Connect(context.Background()) }()
	select {
	case <-dialer.started:
	case <-time.After(time.Second):
		t.Fatal("dial did not start")
	}
	attempt := dialer.snapshot()[0]
	attempt.callbacks.OnMessage(encodeFixtureFrame(t, &infows.Frame{
		SeqId: 41, LogId: "pre-open-log", Method: int32(infows.MethodData),
		Payload: []byte(`{"MsgType":"text","content":"arrived-before-open"}`),
	}))
	close(release)
	if err := <-connectResult; err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	select {
	case event := <-delivered:
		if event.Payload()["content"] != "arrived-before-open" {
			t.Fatalf("event payload = %#v", event.Payload())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pre-open frame was dropped")
	}
	// The immediate ping is serialized before the accepted DATA acknowledgement.
	if frame := transport.nextFrame(t); infows.Method(frame.GetMethod()) != infows.MethodControl {
		t.Fatalf("first write = %#v", frame)
	}
	ack := transport.nextFrame(t)
	ackPayload, err := infows.DecodePayload(ack.GetPayload())
	if err != nil || ack.GetLogId() != "pre-open-log" || ackPayload["ackSeqId"] != json.Number("41") {
		t.Fatalf("pre-open ACK = %#v payload=%#v err=%v", ack, ackPayload, err)
	}
}

func TestSessionRejectsTransportThatClosedBeforeDialReturns(t *testing.T) {
	release := make(chan struct{})
	transport := newRecordingTransport()
	dialer := &scriptedDialer{
		started: make(chan struct{}, 1),
		scripts: []dialScript{{transport: transport, wait: release}},
	}
	capture := &captureRecords{}
	session, _, _, _ := newSessionFixture(t, infows.SessionOptions{
		Dialer: dialer, Observer: capture,
	})
	connectResult := make(chan error, 1)
	go func() { connectResult <- session.Connect(context.Background()) }()
	select {
	case <-dialer.started:
	case <-time.After(time.Second):
		t.Fatal("dial did not start")
	}
	attempt := dialer.snapshot()[0]
	attempt.callbacks.OnClose()
	close(release)
	if err := <-connectResult; err == nil {
		t.Fatal("Connect() succeeded after the transport closed during Dial")
	}
	eventually(t, transport.closed.Load, "pre-open transport cleanup")
	if session.State() != infows.StateDisconnected ||
		!capture.has("ws.connect.failure", "transport_closed") {
		t.Fatalf("state=%s records=%#v", session.State(), capture.snapshot())
	}
}

func TestSessionSerializedWriterSendAndSlowWriteDisconnect(t *testing.T) {
	transport := newRecordingTransport()
	blockUserWrite := make(chan struct{})
	var writes atomic.Int32
	transport.writeHook = func(ctx context.Context, _ []byte) error {
		if writes.Add(1) == 1 { // immediate ping
			return nil
		}
		select {
		case <-blockUserWrite:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	dialer := &scriptedDialer{scripts: []dialScript{{transport: transport}}}
	session, _, _, _ := newSessionFixture(t, infows.SessionOptions{
		Dialer: dialer, WriteTimeout: 50 * time.Millisecond,
		Queues: infows.QueueCapacities{Send: 1, Writer: 1},
	})
	connectFixture(t, session, dialer)
	_ = transport.nextFrame(t)
	sendResult := make(chan error, 1)
	go func() { sendResult <- session.SendData(context.Background(), map[string]any{"safe": true}) }()
	eventually(t, func() bool { return writes.Load() >= 2 }, "blocked user write")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect() while writer blocked error = %v", err)
	}
	if session.State() != infows.StateDisconnected {
		t.Fatalf("state = %s", session.State())
	}
	if err := <-sendResult; err == nil {
		t.Fatal("slow write unexpectedly succeeded")
	}
	if transport.maxActive.Load() != 1 {
		t.Fatalf("max concurrent transport writes = %d", transport.maxActive.Load())
	}
	close(blockUserWrite)
}

type captureRegistrar struct {
	mu     sync.Mutex
	tokens []string
	err    error
}

func (registrar *captureRegistrar) RegisterWebSocket(_ context.Context, token string) error {
	registrar.mu.Lock()
	registrar.tokens = append(registrar.tokens, token)
	registrar.mu.Unlock()
	return registrar.err
}

func (registrar *captureRegistrar) count() int {
	registrar.mu.Lock()
	defer registrar.mu.Unlock()
	return len(registrar.tokens)
}

func TestSessionAutoRegistrationDefaultDelayTokenOnceFailureKeepsConnection(t *testing.T) {
	registrar := &captureRegistrar{err: errors.New("seed-private-registration-error")}
	capture := &captureRecords{}
	session, _, dialer, scheduler := newSessionFixture(t, infows.SessionOptions{
		AutoRegister: boolPointer(true), Registrar: registrar, Observer: capture,
		TokenReader: bytes.NewReader(bytes.Repeat([]byte{0xab}, 16)),
	})
	connectFixture(t, session, dialer)
	eventually(t, func() bool { return scheduler.nextActive(time.Second) != nil }, "registration delay")
	registrationTimer := scheduler.nextActive(time.Second)
	registrationTimer.Fire()
	eventually(t, func() bool { return registrar.count() == 1 }, "registration attempt")
	eventually(t, func() bool { return capture.has("ws.registration.failure", "registration_failed") }, "registration failure")
	if session.State() != infows.StateConnected {
		t.Fatalf("registration failure changed state to %s", session.State())
	}
	registrar.mu.Lock()
	token := registrar.tokens[0]
	registrar.mu.Unlock()
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(token) || token != strings.Repeat("ab", 16) {
		t.Fatalf("callback token = %q", token)
	}
	// A stale timer and later callbacks cannot cause a second attempt.
	registrationTimer.ForceFire()
	runtime.Gosched()
	if registrar.count() != 1 {
		t.Fatalf("registration attempts = %d, want 1", registrar.count())
	}
	serialized, _ := json.Marshal(capture.snapshot())
	if strings.Contains(string(serialized), token) || strings.Contains(string(serialized), "seed-private-registration-error") {
		t.Fatalf("registration observer leaked secret: %s", serialized)
	}
}
