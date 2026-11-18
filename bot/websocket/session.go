package websocket

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dolfly/infoflow/bot/events"
	"github.com/dolfly/infoflow/bot/internal/realtime"
)

// State is the externally visible business-session lifecycle.
type State string

const (
	StateDisconnected State = "disconnected"
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateReconnecting State = "reconnecting"
)

const (
	defaultConnectTimeout       = 30 * time.Second
	defaultWriteTimeout         = 10 * time.Second
	defaultReconnectInterval    = 120 * time.Second
	defaultHeartbeatInterval    = 120 * time.Second
	defaultAutoRegisterDelay    = time.Second
	defaultQueueCapacity        = 64
	defaultMaxReconnectAttempts = -1
)

// SessionError is a secret-safe lifecycle or queue failure.
type SessionError struct {
	Operation string
	Category  string
}

func (e *SessionError) Error() string {
	return "InfoFlow WebSocket " + e.Operation + " failed (" + e.Category + ")"
}

// Registrar switches the platform callback mode after the first connection.
type Registrar interface {
	RegisterWebSocket(context.Context, string) error
}

// RegistrarFunc adapts a function to Registrar.
type RegistrarFunc func(context.Context, string) error

func (function RegistrarFunc) RegisterWebSocket(ctx context.Context, token string) error {
	return function(ctx, token)
}

// Timer and Scheduler keep all waiting outside the state-owner goroutine.
type Timer interface {
	Stop() bool
}

type Scheduler interface {
	AfterFunc(time.Duration, func()) Timer
}

// SchedulerFunc adapts deterministic test schedulers.
type SchedulerFunc func(time.Duration, func()) Timer

func (function SchedulerFunc) AfterFunc(delay time.Duration, callback func()) Timer {
	return function(delay, callback)
}

type timeScheduler struct{}

func (timeScheduler) AfterFunc(delay time.Duration, callback func()) Timer {
	return time.AfterFunc(delay, callback)
}

// QueueCapacities defines explicit bounded queues. Zero selects the default;
// negative values are rejected.
type QueueCapacities struct {
	Control  int
	Receive  int
	Send     int
	Callback int
	Writer   int
}

// JitterFunc returns a delay in [0,max]. It is injectable for deterministic
// reconnect tests.
type JitterFunc func(max time.Duration) time.Duration

// SessionOptions contains normal public settings and advanced fake seams.
// Boolean pointers distinguish an explicit false from the documented default
// true for AutoAck and AutoRegister.
type SessionOptions struct {
	Gateway           string
	EndpointTimeout   time.Duration
	HTTPClient        *http.Client
	Dispatcher        *events.Dispatcher
	EndpointAllocator EndpointAllocator
	Dialer            Dialer
	Registrar         Registrar
	Scheduler         Scheduler
	Observer          realtime.Observer
	Diagnostics       realtime.DiagnosticHandler
	OnError           realtime.ErrorHandler
	ConnectDomain     string
	// XSource is the source prefix sent in the WS handshake X-Source header.
	// When blank it defaults to "go_sdk"; explicit values normalize to
	// "{prefix}_go_sdk" (idempotent when already suffixed).
	XSource              string
	ConnectTimeout       time.Duration
	WriteTimeout         time.Duration
	ReconnectInterval    time.Duration
	MaxReconnectAttempts *int
	HeartbeatInterval    time.Duration
	AutoAck              *bool
	AutoRegister         *bool
	AutoRegisterDelay    time.Duration
	Queues               QueueCapacities
	Jitter               JitterFunc
	TokenReader          io.Reader
	LookupEnv            func(string) (string, bool)
	Now                  func() time.Time
}

type sessionConfig struct {
	dispatcher           *events.Dispatcher
	allocator            EndpointAllocator
	dialer               Dialer
	registrar            Registrar
	scheduler            Scheduler
	observer             realtime.Observer
	onError              realtime.ErrorHandler
	connectDomain        string
	xSource              string
	connectTimeout       time.Duration
	writeTimeout         time.Duration
	reconnectInterval    time.Duration
	maxReconnectAttempts int
	heartbeatInterval    time.Duration
	autoAck              bool
	autoRegister         bool
	autoRegisterDelay    time.Duration
	queues               QueueCapacities
	jitter               JitterFunc
	callbackToken        string
	now                  func() time.Time
}

// Session owns one reconnecting InfoFlow WebSocket business session.
type Session struct {
	config sessionConfig

	controlQ  chan controlEvent
	receiveQ  chan receiveEvent
	sendQ     chan sendCommand
	callbackQ chan callbackTask
	writerQ   chan writeRequest

	state            atomic.Value
	commandID        atomic.Uint64
	generationState  atomic.Uint64
	receiveAdmission sync.RWMutex

	// The fields below are read and written only by loop().
	generation            uint64
	transport             Transport
	serverConfig          ServerConfig
	connectionID          string
	manualDisconnect      bool
	seqID                 uint64
	connectWaiters        map[uint64]chan error
	attemptCancel         context.CancelFunc
	attemptReconnect      bool
	attemptRequestID      string
	attemptFailed         bool
	attemptFailureType    string
	preOpenFrames         []receiveEvent
	reconnectAttempts     int
	reconnectEpoch        uint64
	reconnectTimer        Timer
	heartbeatTimer        Timer
	registrationTimer     Timer
	registrationAttempted bool
	registered            bool
	registrationReady     chan error
}

type controlKind uint8

const (
	controlConnect controlKind = iota
	controlCancelConnect
	controlDisconnect
	controlConnectResult
	controlTransportClose
	controlTransportError
	controlReceiveOverflow
	controlWriteResult
	controlHeartbeatTick
	controlReconnectTick
	controlRegistrationTick
	controlRegistrationResult
)

type controlEvent struct {
	kind         controlKind
	ctx          context.Context
	id           uint64
	result       chan error
	generation   uint64
	transport    Transport
	endpoint     Endpoint
	connectionID string
	category     string
	requestID    string
	epoch        uint64
	write        writeResult
	succeeded    bool
}

type receiveEvent struct {
	generation uint64
	payload    []byte
}

type sendCommand struct {
	ctx        context.Context
	frame      *Frame
	assignSeq  bool
	generation uint64
	result     chan error
}

type callbackTask struct {
	ctx        context.Context
	operation  string
	generation uint64
	requestID  string
	seqID      uint64
	method     int32
	run        func(context.Context) error
}

type writeKind uint8

const (
	writeUser writeKind = iota
	writePing
	writeACK
)

type writeRequest struct {
	ctx           context.Context
	generation    uint64
	transport     Transport
	payload       []byte
	kind          writeKind
	completion    chan error
	requestID     string
	platformLogID string
	seqID         uint64
	frameMethod   int32
}

type writeResult struct {
	generation    uint64
	kind          writeKind
	succeeded     bool
	category      string
	requestID     string
	platformLogID string
	seqID         uint64
	frameMethod   int32
	frameBytes    int
}

// NewSession validates options and starts the state owner, callback executor,
// and one serialized writer. It performs no network request.
func NewSession(options SessionOptions) (*Session, error) {
	config, err := normalizeSessionOptions(options)
	if err != nil {
		return nil, err
	}
	session := &Session{
		config:            config,
		controlQ:          make(chan controlEvent, config.queues.Control),
		receiveQ:          make(chan receiveEvent, config.queues.Receive),
		sendQ:             make(chan sendCommand, config.queues.Send),
		callbackQ:         make(chan callbackTask, config.queues.Callback),
		writerQ:           make(chan writeRequest, config.queues.Writer),
		connectWaiters:    make(map[uint64]chan error),
		registrationReady: make(chan error, 1),
	}
	session.state.Store(StateDisconnected)
	go session.callbackLoop()
	go session.writerLoop()
	go session.loop()
	return session, nil
}

// WaitRegistration waits for the asynchronous callback registration result.
// Sessions without auto registration are already ready and return nil.
func (session *Session) WaitRegistration(ctx context.Context) error {
	if !session.config.autoRegister {
		return nil
	}
	select {
	case err := <-session.registrationReady:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func normalizeSessionOptions(options SessionOptions) (sessionConfig, error) {
	if options.Dispatcher == nil {
		return sessionConfig{}, errors.New("websocket dispatcher must not be nil")
	}
	if options.EndpointAllocator == nil {
		return sessionConfig{}, errors.New("websocket endpoint allocator must not be nil")
	}
	if options.Dialer == nil {
		return sessionConfig{}, errors.New("websocket dialer must not be nil")
	}
	for name, value := range map[string]time.Duration{
		"EndpointTimeout": options.EndpointTimeout,
		"ConnectTimeout":  options.ConnectTimeout, "WriteTimeout": options.WriteTimeout,
		"ReconnectInterval": options.ReconnectInterval, "HeartbeatInterval": options.HeartbeatInterval,
		"AutoRegisterDelay": options.AutoRegisterDelay,
	} {
		if value < 0 {
			return sessionConfig{}, errors.New("websocket " + name + " must not be negative")
		}
	}
	queues := options.Queues
	for name, capacity := range map[string]*int{
		"control": &queues.Control, "receive": &queues.Receive, "send": &queues.Send,
		"callback": &queues.Callback, "writer": &queues.Writer,
	} {
		if *capacity < 0 {
			return sessionConfig{}, errors.New("websocket " + name + " queue capacity must not be negative")
		}
		if *capacity == 0 {
			*capacity = defaultQueueCapacity
		}
	}
	maxReconnectAttempts := defaultMaxReconnectAttempts
	if options.MaxReconnectAttempts != nil {
		maxReconnectAttempts = *options.MaxReconnectAttempts
	}
	if maxReconnectAttempts < -1 {
		return sessionConfig{}, errors.New("websocket MaxReconnectAttempts must be -1 or greater")
	}
	autoAck := true
	if options.AutoAck != nil {
		autoAck = *options.AutoAck
	}
	autoRegister := true
	if options.AutoRegister != nil {
		autoRegister = *options.AutoRegister
	}
	if autoRegister && options.Registrar == nil {
		return sessionConfig{}, errors.New("websocket Registrar is required when AutoRegister is enabled")
	}
	lookupEnv := options.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	connectDomain := options.ConnectDomain
	if connectDomain == "" {
		connectDomain, _ = lookupEnv("INFOFLOW_WS_CONNECT_DOMAIN")
	}
	if connectDomain != "" && !validConnectDomain(connectDomain) {
		return sessionConfig{}, errors.New("websocket ConnectDomain must be a domain or domain:port")
	}
	xSource := normalizeSourceID(options.XSource)
	if strings.ContainsAny(xSource, "\r\n") {
		return sessionConfig{}, errors.New("websocket XSource must not contain newlines")
	}
	connectTimeout := options.ConnectTimeout
	if connectTimeout == 0 {
		connectTimeout = defaultConnectTimeout
	}
	writeTimeout := options.WriteTimeout
	if writeTimeout == 0 {
		writeTimeout = defaultWriteTimeout
	}
	reconnectInterval := options.ReconnectInterval
	if reconnectInterval == 0 {
		reconnectInterval = defaultReconnectInterval
	}
	heartbeatInterval := options.HeartbeatInterval
	if heartbeatInterval == 0 {
		heartbeatInterval = defaultHeartbeatInterval
	}
	autoRegisterDelay := options.AutoRegisterDelay
	if autoRegisterDelay == 0 {
		autoRegisterDelay = defaultAutoRegisterDelay
	}
	scheduler := options.Scheduler
	if scheduler == nil {
		scheduler = timeScheduler{}
	}
	jitter := options.Jitter
	if jitter == nil {
		jitter = secureJitter
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	callbackToken := ""
	if autoRegister {
		reader := options.TokenReader
		if reader == nil {
			reader = rand.Reader
		}
		rawToken := make([]byte, 16)
		if _, err := io.ReadFull(reader, rawToken); err != nil {
			return sessionConfig{}, errors.New("websocket callback token generation failed")
		}
		callbackToken = hex.EncodeToString(rawToken)
	}
	return sessionConfig{
		dispatcher: options.Dispatcher, allocator: options.EndpointAllocator, dialer: options.Dialer,
		registrar: options.Registrar, scheduler: scheduler,
		observer: realtime.Observers(options.Observer, options.Diagnostics), onError: options.OnError,
		connectDomain: connectDomain, xSource: xSource, connectTimeout: connectTimeout,
		writeTimeout: writeTimeout, reconnectInterval: reconnectInterval,
		maxReconnectAttempts: maxReconnectAttempts, heartbeatInterval: heartbeatInterval,
		autoAck: autoAck, autoRegister: autoRegister, autoRegisterDelay: autoRegisterDelay,
		queues: queues, jitter: jitter, callbackToken: callbackToken, now: now,
	}, nil
}

// normalizeSourceID turns a user-supplied source prefix into the wire value:
// blank becomes the default "go_sdk", explicit prefixes get the "_go_sdk"
// suffix, and values that already carry the suffix are kept unchanged.
func normalizeSourceID(value string) string {
	id := strings.TrimSpace(value)
	if id == "" {
		return "go_sdk"
	}
	if id != "go_sdk" && !strings.HasSuffix(id, "_go_sdk") {
		id += "_go_sdk"
	}
	return id
}

func secureJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	var bytes [8]byte
	if _, err := io.ReadFull(rand.Reader, bytes[:]); err != nil {
		return 0
	}
	value := uint64(0)
	for _, current := range bytes {
		value = value<<8 | uint64(current)
	}
	return time.Duration(value % uint64(max+1))
}

func validConnectDomain(domain string) bool {
	if strings.TrimSpace(domain) != domain || domain == "" {
		return false
	}
	parsed, err := url.Parse("wss://" + domain)
	return err == nil && parsed.Host != "" && parsed.Path == "" && parsed.RawQuery == "" &&
		parsed.Fragment == "" && parsed.User == nil && parsed.Opaque == ""
}

type connectTarget struct {
	url          string
	connectionID string
}

func prepareConnectTarget(endpointURL, connectDomain string) (connectTarget, error) {
	parsed, err := url.Parse(endpointURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "ws" && parsed.Scheme != "wss") ||
		parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return connectTarget{}, &SessionError{Operation: "connect", Category: "invalid_endpoint"}
	}
	connectionID := parsed.Query().Get("connection_id")
	if connectDomain != "" {
		if !validConnectDomain(connectDomain) {
			return connectTarget{}, &SessionError{Operation: "connect", Category: "invalid_connect_domain"}
		}
		parsed.Host = connectDomain
	}
	return connectTarget{url: parsed.String(), connectionID: connectionID}, nil
}

// State returns a lock-free mirror written only by the state-owner goroutine.
func (session *Session) State() State {
	return session.state.Load().(State)
}

// Connect coalesces concurrent callers onto one endpoint/dial attempt and
// completes only after Phase 2 succeeds.
func (session *Session) Connect(ctx context.Context) error {
	if ctx == nil {
		return &SessionError{Operation: "connect", Category: "invalid_context"}
	}
	ctx, _ = realtime.EnsureRequestID(ctx)
	id := session.commandID.Add(1)
	result := make(chan error, 1)
	command := controlEvent{kind: controlConnect, ctx: ctx, id: id, result: result}
	if err := session.sendControl(ctx, command); err != nil {
		return err
	}
	select {
	case err := <-result:
		return err
	default:
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		select {
		case err := <-result:
			return err
		default:
		}
		session.sendControlLater(controlEvent{kind: controlCancelConnect, id: id})
		return ctx.Err()
	}
}

// Disconnect intentionally closes the current generation and cancels pending
// reconnect/connect timers. A later Connect starts a fresh generation.
func (session *Session) Disconnect(ctx context.Context) error {
	if ctx == nil {
		return &SessionError{Operation: "disconnect", Category: "invalid_context"}
	}
	result := make(chan error, 1)
	if err := session.sendControl(ctx, controlEvent{kind: controlDisconnect, ctx: ctx, result: result}); err != nil {
		return err
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SendFrame defensively copies and serializes a public Frame, then completes
// only after the one writer path hands it to the active transport.
func (session *Session) SendFrame(ctx context.Context, frame *Frame) error {
	if ctx == nil {
		return &SessionError{Operation: "send", Category: "invalid_context"}
	}
	if frame == nil {
		return &SessionError{Operation: "send", Category: "invalid_frame"}
	}
	return session.send(ctx, CloneFrame(frame), false)
}

// SendData JSON-encodes a protocol-open payload in a DATA Frame.
func (session *Session) SendData(ctx context.Context, payload any) error {
	encoded, err := EncodePayload(payload)
	if err != nil {
		return err
	}
	return session.send(ctx, &Frame{Method: int32(MethodData), Payload: encoded}, true)
}

func (session *Session) send(ctx context.Context, frame *Frame, assignSeq bool) error {
	if ctx == nil {
		return &SessionError{Operation: "send", Category: "invalid_context"}
	}
	ctx, _ = realtime.EnsureRequestID(ctx)
	if session.State() != StateConnected {
		return &SessionError{Operation: "send", Category: "not_connected"}
	}
	generation := session.generationState.Load()
	if session.State() != StateConnected {
		return &SessionError{Operation: "send", Category: "not_connected"}
	}
	result := make(chan error, 1)
	command := sendCommand{ctx: ctx, frame: frame, assignSeq: assignSeq, generation: generation, result: result}
	select {
	case session.sendQ <- command:
	case <-ctx.Done():
		return ctx.Err()
	default:
		return &SessionError{Operation: "send", Category: "queue_full"}
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (session *Session) sendControl(ctx context.Context, event controlEvent) error {
	select {
	case session.controlQ <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (session *Session) sendControlLater(event controlEvent) {
	go func() { session.controlQ <- event }()
}

func (session *Session) loop() {
	for {
		select {
		case event := <-session.controlQ:
			session.handleControl(event)
			continue
		default:
		}
		select {
		case event := <-session.controlQ:
			session.handleControl(event)
		case event := <-session.receiveQ:
			session.handleReceive(event)
		case command := <-session.sendQ:
			session.handleSend(command)
		}
	}
}

func (session *Session) handleControl(event controlEvent) {
	switch event.kind {
	case controlConnect:
		session.handleConnectCommand(event)
	case controlCancelConnect:
		session.handleCancelConnect(event.id)
	case controlDisconnect:
		session.handleDisconnectCommand(event)
	case controlConnectResult:
		session.handleConnectResult(event)
	case controlTransportClose, controlTransportError, controlReceiveOverflow:
		if event.generation == session.generation {
			category := event.category
			if category == "" {
				category = "transport_closed"
			}
			if (session.State() == StateConnecting || session.State() == StateReconnecting) && session.transport == nil {
				session.attemptFailed = true
				session.attemptFailureType = category
			} else {
				session.handleConnectionLoss(category)
			}
		}
	case controlWriteResult:
		session.handleWriteResult(event.write)
	case controlHeartbeatTick:
		if event.generation == session.generation && session.State() == StateConnected {
			session.heartbeatTimer = nil
			session.queuePing(event.ctx)
			session.scheduleHeartbeat()
		}
	case controlReconnectTick:
		if event.epoch == session.reconnectEpoch && session.State() == StateReconnecting && !session.manualDisconnect {
			session.reconnectTimer = nil
			session.reconnectAttempts++
			ctx, _ := realtime.EnsureRequestID(context.Background())
			session.beginConnectAttempt(ctx, true)
		}
	case controlRegistrationTick:
		session.handleRegistrationTick(event)
	case controlRegistrationResult:
		session.handleRegistrationResult(event)
	}
}

func (session *Session) handleConnectCommand(command controlEvent) {
	if err := command.ctx.Err(); err != nil {
		complete(command.result, err)
		return
	}
	switch session.State() {
	case StateConnected:
		complete(command.result, nil)
		return
	case StateConnecting, StateReconnecting:
		session.connectWaiters[command.id] = command.result
		return
	}
	session.manualDisconnect = false
	session.reconnectAttempts = 0
	session.connectWaiters[command.id] = command.result
	session.beginConnectAttempt(command.ctx, false)
}

func (session *Session) handleCancelConnect(id uint64) {
	if _, exists := session.connectWaiters[id]; !exists {
		return
	}
	delete(session.connectWaiters, id)
	if len(session.connectWaiters) == 0 && session.State() == StateConnecting && !session.attemptReconnect {
		if session.attemptCancel != nil {
			session.attemptCancel()
			session.attemptCancel = nil
		}
		session.advanceGeneration()
		session.preOpenFrames = nil
		session.setState(StateDisconnected)
		realtime.Emit(session.config.observer, realtime.Diagnostic{
			Event: "ws.connect.failure", Level: "warn", Component: "websocket",
			Operation: "connect", Result: "failure", ErrorType: "command_cancelled",
			RequestID: session.attemptRequestID, Generation: session.generation, Timestamp: session.config.now(),
		})
	}
}

func (session *Session) beginConnectAttempt(source context.Context, reconnect bool) {
	if session.attemptCancel != nil {
		session.attemptCancel()
	}
	session.advanceGeneration()
	generation := session.generation
	base := realtime.DetachedContext(source)
	base, requestID := realtime.EnsureRequestID(base)
	attemptContext, cancel := context.WithTimeout(base, session.config.connectTimeout)
	session.attemptCancel = cancel
	session.attemptReconnect = reconnect
	session.attemptRequestID = requestID
	session.attemptFailed = false
	session.attemptFailureType = ""
	session.preOpenFrames = nil
	if reconnect {
		session.setState(StateReconnecting)
	} else {
		session.setState(StateConnecting)
	}
	realtime.Emit(session.config.observer, realtime.Diagnostic{
		Event: "ws.connect.start", Level: "debug", Component: "websocket",
		Operation: "connect", Result: "started", RequestID: requestID,
		Generation: generation, Attempt: session.reconnectAttempts, Timestamp: session.config.now(),
	})
	go session.openTransport(attemptContext, generation, requestID)
}

func (session *Session) openTransport(ctx context.Context, generation uint64, requestID string) {
	endpoint, err := session.config.allocator.Allocate(ctx)
	if err != nil {
		session.postConnectFailure(ctx, generation, requestID, categoryForContext(ctx, "endpoint"))
		return
	}
	target, err := prepareConnectTarget(endpoint.URL, session.config.connectDomain)
	if err != nil {
		session.postConnectFailure(ctx, generation, requestID, "invalid_endpoint")
		return
	}
	callbacks := TransportCallbacks{
		OnMessage: func(payload []byte) { session.enqueueReceive(generation, payload) },
		OnClose: func() {
			if session.generationState.Load() != generation {
				return
			}
			session.controlQ <- controlEvent{kind: controlTransportClose, generation: generation, category: "transport_closed"}
		},
		OnError: func() {
			if session.generationState.Load() != generation {
				return
			}
			session.controlQ <- controlEvent{kind: controlTransportError, generation: generation, category: "transport_error"}
		},
	}
	transport, err := session.config.dialer.Dial(ctx, target.url, DialOptions{
		Header: http.Header{"X-Source": []string{session.config.xSource}}, CompressionDisabled: true,
	}, callbacks)
	if err != nil {
		session.postConnectFailure(ctx, generation, requestID, categoryForContext(ctx, "dial"))
		return
	}
	if transport == nil {
		session.postConnectFailure(ctx, generation, requestID, "dial")
		return
	}
	session.controlQ <- controlEvent{
		kind: controlConnectResult, generation: generation, transport: transport,
		endpoint: endpoint, connectionID: target.connectionID, requestID: requestID,
		ctx: realtime.DetachedContext(ctx), succeeded: true,
	}
}

func categoryForContext(ctx context.Context, fallback string) string {
	if ctx.Err() != nil {
		return "timeout_or_cancelled"
	}
	return fallback
}

func (session *Session) postConnectFailure(ctx context.Context, generation uint64, requestID, category string) {
	session.controlQ <- controlEvent{
		kind: controlConnectResult, generation: generation, requestID: requestID,
		category: category, ctx: realtime.DetachedContext(ctx), succeeded: false,
	}
}

func (session *Session) handleConnectResult(result controlEvent) {
	if result.generation != session.generation || session.manualDisconnect ||
		(session.State() != StateConnecting && session.State() != StateReconnecting) {
		if result.transport != nil {
			_ = result.transport.Close()
		}
		return
	}
	if session.attemptCancel != nil {
		session.attemptCancel()
		session.attemptCancel = nil
	}
	if result.succeeded && session.attemptFailed {
		if result.transport != nil {
			_ = result.transport.Close()
		}
		result.transport = nil
		result.succeeded = false
		result.category = session.attemptFailureType
		if result.category == "" {
			result.category = "transport_closed_during_connect"
		}
	}
	if !result.succeeded {
		session.preOpenFrames = nil
		realtime.Emit(session.config.observer, realtime.Diagnostic{
			Event: "ws.connect.failure", Level: "warn", Component: "websocket",
			Operation: "connect", Result: "failure", ErrorType: result.category,
			RequestID: result.requestID, Generation: result.generation,
			Attempt: session.reconnectAttempts, Timestamp: session.config.now(),
		})
		if session.attemptReconnect {
			session.submitConnectError(result)
			session.scheduleReconnect()
		} else {
			session.setState(StateDisconnected)
			session.submitConnectError(result)
			session.completeConnectWaiters(&SessionError{Operation: "connect", Category: result.category})
		}
		return
	}
	session.transport = result.transport
	session.serverConfig = cloneServerConfig(result.endpoint.Config)
	session.connectionID = result.connectionID
	session.reconnectAttempts = 0
	session.setState(StateConnected)
	realtime.Emit(session.config.observer, realtime.Diagnostic{
		Event: "ws.connect.success", Level: "info", Component: "websocket",
		Operation: "connect", Result: "success", RequestID: result.requestID,
		Generation: result.generation, Timestamp: session.config.now(),
	})
	session.completeConnectWaiters(nil)
	ctx := result.ctx
	if ctx == nil {
		ctx, _ = realtime.EnsureRequestID(context.Background())
	}
	session.submitLifecycle(ctx, "connected", map[string]any{"connectionId": session.connectionID})
	session.queuePing(ctx)
	session.scheduleHeartbeat()
	session.scheduleRegistration(result.generation)
	pendingFrames := session.preOpenFrames
	session.preOpenFrames = nil
	for _, frame := range pendingFrames {
		session.handleReceive(frame)
	}
}

func (session *Session) submitConnectError(result controlEvent) {
	ctx := result.ctx
	if ctx == nil {
		ctx, _ = realtime.EnsureRequestID(context.Background())
	}
	session.submitLifecycle(ctx, "error", map[string]any{
		"operation": "connect", "category": result.category,
	})
}

func (session *Session) handleDisconnectCommand(command controlEvent) {
	if err := command.ctx.Err(); err != nil {
		complete(command.result, err)
		return
	}
	previousState := session.State()
	previousGeneration := session.generation
	session.manualDisconnect = true
	session.advanceGeneration()
	session.reconnectEpoch++
	session.stopTimers()
	if session.attemptCancel != nil {
		session.attemptCancel()
		session.attemptCancel = nil
	}
	transport := session.transport
	session.transport = nil
	session.preOpenFrames = nil
	session.connectionID = ""
	if transport != nil {
		_ = transport.Close()
	}
	session.setState(StateDisconnected)
	session.completeConnectWaiters(&SessionError{Operation: "connect", Category: "disconnected"})
	if previousState != StateDisconnected {
		ctx, requestID := realtime.EnsureRequestID(command.ctx)
		realtime.Emit(session.config.observer, realtime.Diagnostic{
			Event: "ws.close", Level: "info", Component: "websocket",
			Operation: "disconnect", Result: "success", RequestID: requestID,
			Generation: previousGeneration, Timestamp: session.config.now(),
		})
		session.submitLifecycle(ctx, "disconnected", map[string]any{})
	}
	complete(command.result, nil)
}

func (session *Session) stopTimers() {
	for _, timer := range []Timer{session.reconnectTimer, session.heartbeatTimer, session.registrationTimer} {
		if timer != nil {
			timer.Stop()
		}
	}
	session.reconnectTimer = nil
	session.heartbeatTimer = nil
	session.registrationTimer = nil
}

func (session *Session) handleSend(command sendCommand) {
	if err := command.ctx.Err(); err != nil {
		complete(command.result, err)
		return
	}
	if session.State() != StateConnected || session.transport == nil {
		complete(command.result, &SessionError{Operation: "send", Category: "not_connected"})
		return
	}
	if command.generation != session.generation {
		complete(command.result, &SessionError{Operation: "send", Category: "stale_generation"})
		return
	}
	if command.assignSeq {
		command.frame.SeqId = session.nextSeqID()
	}
	encoded, err := EncodeFrame(command.frame)
	if err != nil {
		complete(command.result, &SessionError{Operation: "send", Category: "invalid_frame"})
		return
	}
	requestID := realtime.RequestID(command.ctx)
	request := writeRequest{
		ctx: command.ctx, generation: session.generation, transport: session.transport,
		payload: encoded, kind: writeUser, completion: command.result, requestID: requestID,
		platformLogID: command.frame.GetLogId(), seqID: command.frame.GetSeqId(),
		frameMethod: command.frame.GetMethod(),
	}
	select {
	case session.writerQ <- request:
	default:
		complete(command.result, &SessionError{Operation: "send", Category: "writer_queue_full"})
	}
}

func (session *Session) writerLoop() {
	for request := range session.writerQ {
		if session.generationState.Load() != request.generation || session.State() != StateConnected {
			if request.completion != nil {
				complete(request.completion, &SessionError{Operation: "send", Category: "stale_generation"})
			}
			session.controlQ <- controlEvent{kind: controlWriteResult, write: writeResult{
				generation: request.generation, kind: request.kind, succeeded: false,
				category: "stale_generation", requestID: request.requestID,
				platformLogID: request.platformLogID, seqID: request.seqID,
				frameMethod: request.frameMethod, frameBytes: len(request.payload),
			}}
			continue
		}
		ctx := request.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		writeContext, cancel := context.WithTimeout(ctx, session.config.writeTimeout)
		err := request.transport.Write(writeContext, request.payload)
		category := ""
		if err != nil {
			category = "write_failure"
			if writeContext.Err() != nil {
				category = "write_timeout_or_cancelled"
			}
		}
		cancel()
		if request.completion != nil {
			if err == nil {
				complete(request.completion, nil)
			} else {
				complete(request.completion, &SessionError{Operation: "send", Category: category})
			}
		}
		session.controlQ <- controlEvent{kind: controlWriteResult, write: writeResult{
			generation: request.generation, kind: request.kind, succeeded: err == nil,
			category: category, requestID: request.requestID, platformLogID: request.platformLogID,
			seqID: request.seqID, frameMethod: request.frameMethod, frameBytes: len(request.payload),
		}}
	}
}

func (session *Session) handleWriteResult(result writeResult) {
	if result.generation != session.generation {
		return
	}
	if !result.succeeded {
		session.handleConnectionLoss(result.category)
		return
	}
	switch result.kind {
	case writePing:
		ctx, _ := realtime.EnsureRequestID(context.Background())
		session.submitLifecycle(ctx, "heartbeat", map[string]any{"seqId": result.seqID, "type": "ping"})
	case writeACK:
		realtime.Emit(session.config.observer, realtime.Diagnostic{
			Event: "ws.ack.success", Level: "debug", Component: "websocket",
			Operation: "ack", Result: "success", RequestID: result.requestID,
			Generation: result.generation, PlatformLogID: result.platformLogID,
			SeqID: result.seqID, FrameMethod: result.frameMethod, FrameBytes: result.frameBytes,
			Timestamp: session.config.now(),
		})
	}
}

func (session *Session) queuePing(ctx context.Context) {
	if session.State() != StateConnected || session.transport == nil {
		return
	}
	frame := &Frame{
		SeqId: session.nextSeqID(), Method: int32(MethodControl),
		Headers: []*Header{{Key: "type", Value: "ping"}}, Payload: []byte("{}"),
	}
	encoded, _ := EncodeFrame(frame)
	requestID := realtime.RequestID(ctx)
	request := writeRequest{
		ctx: realtime.DetachedContext(ctx), generation: session.generation, transport: session.transport,
		payload: encoded, kind: writePing, requestID: requestID, seqID: frame.SeqId,
		frameMethod: frame.Method,
	}
	select {
	case session.writerQ <- request:
	default:
		session.handleConnectionLoss("heartbeat_queue_full")
	}
}

func (session *Session) scheduleHeartbeat() {
	if session.State() != StateConnected {
		return
	}
	if session.heartbeatTimer != nil {
		session.heartbeatTimer.Stop()
	}
	interval := session.config.heartbeatInterval
	if session.serverConfig.PingInterval != nil {
		interval = *session.serverConfig.PingInterval
	}
	generation := session.generation
	session.heartbeatTimer = session.config.scheduler.AfterFunc(interval, func() {
		ctx, _ := realtime.EnsureRequestID(context.Background())
		session.controlQ <- controlEvent{kind: controlHeartbeatTick, generation: generation, ctx: ctx}
	})
}

func (session *Session) enqueueReceive(generation uint64, payload []byte) {
	session.receiveAdmission.RLock()
	if session.generationState.Load() != generation {
		session.receiveAdmission.RUnlock()
		return
	}
	event := receiveEvent{generation: generation, payload: append([]byte(nil), payload...)}
	overflow := false
	select {
	case session.receiveQ <- event:
	default:
		overflow = true
	}
	session.receiveAdmission.RUnlock()
	if overflow {
		session.controlQ <- controlEvent{
			kind: controlReceiveOverflow, generation: generation, category: "receive_queue_full",
		}
	}
}

func (session *Session) handleReceive(event receiveEvent) {
	if event.generation != session.generation {
		return
	}
	if (session.State() == StateConnecting || session.State() == StateReconnecting) && session.transport == nil {
		if len(session.preOpenFrames) >= session.config.queues.Receive {
			session.attemptFailed = true
			session.attemptFailureType = "receive_queue_full"
			return
		}
		session.preOpenFrames = append(session.preOpenFrames, event)
		return
	}
	if session.State() != StateConnected {
		return
	}
	ctx, requestID := realtime.EnsureRequestID(context.Background())
	frame, err := DecodeFrame(event.payload)
	if err != nil {
		session.dispatchFailure(event.generation, requestID, "malformed_frame", 0, 0)
		return
	}
	realtime.Emit(session.config.observer, realtime.Diagnostic{
		Event: "ws.frame.receive", Level: "debug", Component: "websocket",
		Operation: "frame_receive", Result: "success", RequestID: requestID,
		Generation: event.generation, PlatformLogID: frame.GetLogId(), SeqID: frame.GetSeqId(),
		FrameMethod: frame.GetMethod(), FrameBytes: len(event.payload), Timestamp: session.config.now(),
	})
	switch Method(frame.GetMethod()) {
	case MethodControl:
		session.handleControlFrame(ctx, frame)
	case MethodData:
		session.handleDataFrame(ctx, frame)
	case MethodResponse:
		payload, err := DecodePayload(frame.GetPayload())
		if err != nil {
			session.dispatchFailure(event.generation, requestID, "malformed_json", frame.GetSeqId(), frame.GetMethod())
			return
		}
		session.submitLifecycle(ctx, "response", payload)
	}
}

func (session *Session) handleControlFrame(ctx context.Context, frame *Frame) {
	payload, err := DecodePayload(frame.GetPayload())
	if err != nil {
		payload = map[string]any{}
	}
	controlType := headerValue(frame, "type")
	if controlType == "" {
		controlType, _ = payload["type"].(string)
	}
	if controlType == "pong" {
		session.submitLifecycle(ctx, "heartbeat", map[string]any{
			"seqId": frame.GetSeqId(), "type": "pong", "config": payload,
		})
	}
}

func (session *Session) handleDataFrame(ctx context.Context, frame *Frame) {
	requestID := realtime.RequestID(ctx)
	payload, expansionErr := expandEventPayload(frame.GetPayload())
	if payload == nil {
		session.dispatchFailure(session.generation, requestID, "malformed_json", frame.GetSeqId(), frame.GetMethod())
		return
	}
	if expansionErr != nil {
		session.dispatchFailure(session.generation, requestID, "malformed_encrypt", frame.GetSeqId(), frame.GetMethod())
	}
	accepted := session.submitCallback(callbackTask{
		ctx: realtime.DetachedContext(ctx), operation: "event_dispatch", generation: session.generation,
		requestID: requestID, seqID: frame.GetSeqId(), method: frame.GetMethod(),
		run: func(callbackContext context.Context) error {
			return session.config.dispatcher.DispatchPayload(callbackContext, payload, frame.GetPayload())
		},
	})
	if !accepted {
		session.dispatchFailure(session.generation, requestID, "callback_queue_full", frame.GetSeqId(), frame.GetMethod())
		return
	}
	if !session.config.autoAck || session.State() != StateConnected || session.transport == nil {
		return
	}
	ackPayload, _ := EncodePayload(map[string]any{
		"ackSeqId": frame.GetSeqId(), "code": 0, "message": "ok",
	})
	ack := &Frame{
		SeqId: session.nextSeqID(), LogId: frame.GetLogId(), Service: 0, Method: int32(MethodData),
		Headers: []*Header{{Key: "type", Value: "event_ack"}}, Payload: ackPayload,
	}
	encoded, _ := EncodeFrame(ack)
	request := writeRequest{
		ctx: realtime.DetachedContext(ctx), generation: session.generation, transport: session.transport,
		payload: encoded, kind: writeACK, requestID: requestID, platformLogID: frame.GetLogId(),
		seqID: frame.GetSeqId(), frameMethod: ack.GetMethod(),
	}
	select {
	case session.writerQ <- request:
	default:
		session.dispatchFailure(session.generation, requestID, "ack_queue_full", frame.GetSeqId(), frame.GetMethod())
	}
}

func (session *Session) dispatchFailure(generation uint64, requestID, category string, seqID uint64, method int32) {
	realtime.Report(session.config.onError, realtime.RuntimeError{
		Component: "websocket", Operation: "dispatch", Category: category,
	})
	realtime.Emit(session.config.observer, realtime.Diagnostic{
		Event: "ws.dispatch.failure", Level: "warn", Component: "websocket",
		Operation: "dispatch", Result: "failure", ErrorType: category,
		RequestID: requestID, Generation: generation, SeqID: seqID,
		FrameMethod: method, Timestamp: session.config.now(),
	})
}

func (session *Session) callbackLoop() {
	for task := range session.callbackQ {
		if err := task.run(task.ctx); err != nil {
			session.dispatchFailure(task.generation, task.requestID, "dispatcher_failure", task.seqID, task.method)
		}
	}
}

func (session *Session) submitCallback(task callbackTask) bool {
	select {
	case session.callbackQ <- task:
		return true
	default:
		return false
	}
}

func (session *Session) submitLifecycle(ctx context.Context, route string, payload map[string]any) bool {
	raw, _ := EncodePayload(payload)
	event := events.NewEvent(route, payload, raw)
	accepted := session.submitCallback(callbackTask{
		ctx: realtime.DetachedContext(ctx), operation: route, generation: session.generation,
		requestID: realtime.RequestID(ctx),
		run: func(callbackContext context.Context) error {
			return session.config.dispatcher.Dispatch(callbackContext, route, event)
		},
	})
	if !accepted {
		session.dispatchFailure(session.generation, realtime.RequestID(ctx), "callback_queue_full", 0, 0)
	}
	return accepted
}

func (session *Session) handleConnectionLoss(category string) {
	if session.State() != StateConnected {
		return
	}
	previousGeneration := session.generation
	session.advanceGeneration()
	session.stopTimers()
	if session.attemptCancel != nil {
		session.attemptCancel()
		session.attemptCancel = nil
	}
	transport := session.transport
	session.transport = nil
	session.preOpenFrames = nil
	if transport != nil {
		_ = transport.Close()
	}
	session.setState(StateDisconnected)
	ctx, requestID := realtime.EnsureRequestID(context.Background())
	realtime.Emit(session.config.observer, realtime.Diagnostic{
		Event: "ws.close", Level: "warn", Component: "websocket",
		Operation: "transport", Result: "failure", ErrorType: category,
		RequestID: requestID, Generation: previousGeneration, Timestamp: session.config.now(),
	})
	if category != "transport_closed" {
		session.submitLifecycle(ctx, "error", map[string]any{
			"operation": "transport", "category": category,
		})
	}
	session.submitLifecycle(ctx, "disconnected", map[string]any{})
	if session.manualDisconnect {
		return
	}
	session.setState(StateReconnecting)
	session.scheduleReconnect()
}

func (session *Session) scheduleReconnect() {
	maxAttempts := session.maxReconnectAttempts()
	if maxAttempts != -1 && session.reconnectAttempts >= maxAttempts {
		session.setState(StateDisconnected)
		session.completeConnectWaiters(&SessionError{Operation: "connect", Category: "reconnect_exhausted"})
		return
	}
	if session.reconnectTimer != nil || session.manualDisconnect {
		return
	}
	delay := session.config.reconnectInterval
	if session.serverConfig.ReconnectInterval != nil {
		delay = *session.serverConfig.ReconnectInterval
	}
	nonce := time.Duration(0)
	if session.serverConfig.ReconnectNonce != nil {
		nonce = *session.serverConfig.ReconnectNonce
	}
	jitter := session.config.jitter(nonce)
	if jitter < 0 {
		jitter = 0
	}
	if jitter > nonce {
		jitter = nonce
	}
	if jitter > time.Duration(1<<63-1)-delay {
		delay = time.Duration(1<<63 - 1)
	} else {
		delay += jitter
	}
	session.reconnectEpoch++
	epoch := session.reconnectEpoch
	ctx, requestID := realtime.EnsureRequestID(context.Background())
	realtime.Emit(session.config.observer, realtime.Diagnostic{
		Event: "ws.reconnect.schedule", Level: "info", Component: "websocket",
		Operation: "reconnect", Result: "scheduled", RequestID: requestID,
		Generation: session.generation, Attempt: session.reconnectAttempts + 1, Timestamp: session.config.now(),
	})
	session.reconnectTimer = session.config.scheduler.AfterFunc(delay, func() {
		session.controlQ <- controlEvent{kind: controlReconnectTick, epoch: epoch, ctx: ctx}
	})
}

func (session *Session) maxReconnectAttempts() int {
	if session.serverConfig.ReconnectCount != nil {
		return *session.serverConfig.ReconnectCount
	}
	return session.config.maxReconnectAttempts
}

func (session *Session) scheduleRegistration(generation uint64) {
	if !session.config.autoRegister || session.registrationAttempted || session.registered || session.registrationTimer != nil {
		return
	}
	session.registrationTimer = session.config.scheduler.AfterFunc(session.config.autoRegisterDelay, func() {
		session.controlQ <- controlEvent{kind: controlRegistrationTick, generation: generation}
	})
}

func (session *Session) handleRegistrationTick(event controlEvent) {
	session.registrationTimer = nil
	if event.generation != session.generation || session.State() != StateConnected ||
		session.registrationAttempted || session.registered {
		return
	}
	session.registrationAttempted = true
	ctx, requestID := realtime.EnsureRequestID(context.Background())
	go func(generation uint64) {
		err := session.config.registrar.RegisterWebSocket(ctx, session.config.callbackToken)
		session.controlQ <- controlEvent{
			kind: controlRegistrationResult, generation: generation, requestID: requestID, succeeded: err == nil,
		}
	}(event.generation)
}

func (session *Session) handleRegistrationResult(event controlEvent) {
	if event.generation != session.generation || session.State() != StateConnected {
		return
	}
	if event.succeeded {
		session.registered = true
		session.registrationReady <- nil
		realtime.Emit(session.config.observer, realtime.Diagnostic{
			Event: "ws.registration.success", Level: "info", Component: "websocket",
			Operation: "registration", Result: "success", RequestID: event.requestID,
			Generation: event.generation, Timestamp: session.config.now(),
		})
		return
	}
	realtime.Report(session.config.onError, realtime.RuntimeError{
		Component: "websocket", Operation: "registration", Category: "registration_failed",
	})
	realtime.Emit(session.config.observer, realtime.Diagnostic{
		Event: "ws.registration.failure", Level: "warn", Component: "websocket",
		Operation: "registration", Result: "failure", ErrorType: "registration_failed",
		RequestID: event.requestID, Generation: event.generation, Timestamp: session.config.now(),
	})
	session.registrationReady <- errors.New("websocket callback registration failed")
}

func (session *Session) setState(state State) {
	session.state.Store(state)
}

func (session *Session) advanceGeneration() {
	previous := session.generation
	session.receiveAdmission.Lock()
	session.generation++
	session.generationState.Store(session.generation)
	session.purgeReceiveGeneration(previous)
	session.receiveAdmission.Unlock()
	session.purgeWriterGeneration(previous)
}

func (session *Session) purgeReceiveGeneration(generation uint64) {
	queued := len(session.receiveQ)
	for range queued {
		select {
		case event := <-session.receiveQ:
			if event.generation != generation {
				session.receiveQ <- event
			}
		default:
			return
		}
	}
}

func (session *Session) purgeWriterGeneration(generation uint64) {
	queued := len(session.writerQ)
	for range queued {
		select {
		case request := <-session.writerQ:
			if request.generation != generation {
				session.writerQ <- request
				continue
			}
			if request.completion != nil {
				complete(request.completion, &SessionError{Operation: "send", Category: "stale_generation"})
			}
		default:
			return
		}
	}
}

func (session *Session) nextSeqID() uint64 {
	session.seqID++
	return session.seqID
}

func (session *Session) completeConnectWaiters(err error) {
	for id, waiter := range session.connectWaiters {
		complete(waiter, err)
		delete(session.connectWaiters, id)
	}
}

func complete(channel chan error, err error) {
	select {
	case channel <- err:
	default:
	}
}

func cloneServerConfig(config ServerConfig) ServerConfig {
	result := ServerConfig{}
	if config.ReconnectCount != nil {
		value := *config.ReconnectCount
		result.ReconnectCount = &value
	}
	for source, destination := range map[*time.Duration]**time.Duration{
		config.ReconnectInterval: &result.ReconnectInterval,
		config.ReconnectNonce:    &result.ReconnectNonce,
		config.PingInterval:      &result.PingInterval,
	} {
		if source != nil {
			value := *source
			*destination = &value
		}
	}
	return result
}
