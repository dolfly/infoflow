# SDK Contract

## Identity

- Module: `github.com/dolfly/infoflow`
- Root package: `infoflow`
- Minimum Go: 1.25
- Validation: Go 1.25.x and 1.26.x
- Approved external dependencies:
  - `github.com/coder/websocket` v1.8.15
  - `google.golang.org/protobuf` v1.36.11

No other runtime dependency may be added without an explicit contract change.
The canonical protobuf source and generated Go binding MUST remain checked in;
Frame wire bytes MUST NOT be hand-written.

## Public L1 surface

```go
client, err := infoflow.NewClient(infoflow.Config{
    AppKey:    "...",
    AppSecret: "...",
})

user, err := client.Bot.IM.Message.SendToUser(ctx, userID, text)
group, err := client.Bot.IM.Message.SendToGroup(ctx, groupID, text)
```

The additive robot-IM surface retains those text conveniences and also exposes
typed user/group message requests, group message reads, message detail reads,
recall, pin, emoji and voice-transcription operations through
`client.Bot.IM.Message`. Message content returned by read APIs is retained as
`json.RawMessage` to preserve every documented polymorphic field (including
temporary image `downloadUrl`). Streaming-card methods accept and return raw
JSON only: their field and envelope semantics are observed, not typed. List
requests enforce the documented 24-hour and page-size bounds and reject a
non-progressing page token. Unsupported single-chat `file` message sends are
rejected locally; BOS file upload IS part of this SDK slice through
`client.Bot.IM.File` (`Upload`, `GetURL`, `Download`).

The user ID and text MUST be non-blank. The group ID MUST be positive and its
text non-blank. Group client message IDs are generated internally as positive
millisecond values.

The L1 slice calls exactly these routes:

- `POST /auth/app_access_token`
- `POST /app/message/send`
- `POST /robot/msg/groupmsgsend`

Unknown response fields remain available through `RawJSON()`. Every raw
accessor MUST return a fresh copy.

This owner-approved `narrower_surface` does not claim parity with every Node T2
send-helper form. It intentionally defers recipient collections and non-text
payload variants while retaining validation, generated group client message
IDs, typed mapping, and raw-response semantics.

T4 is additive. Existing L1 signatures, wire bytes, auth behavior, one-attempt
semantics, proxy behavior, error types, and silence MUST NOT change when
realtime configuration is present.

## Public T4 surface

Every `Client` exposes:

```go
client.Bot.Events
client.Bot.Webhook.NewReceiver(options)
client.Bot.Webhook.Register(ctx, registration)
client.Bot.WebSocket.NewSession(options)
client.Bot.WebSocket.Register(ctx, callbackToken)
```

Receivers and sessions constructed from the Client MUST use its shared event
dispatcher, realtime observer/error hooks, authenticated transport, token
cache, and redaction rules unless an advanced override is explicitly supplied.
Constructors MUST validate configuration without network I/O.

### Event dispatcher

- `On`, `Once`, `Off`, `Dispatch`, `DispatchFrame`, and `DispatchPayload` MUST
  be safe under concurrent registration and dispatch.
- Matching handlers MUST run in stable route and registration order. One
  handler failure MUST NOT prevent later handlers and MUST be reported only as
  a safe category.
- Challenge payloads MUST NOT be dispatched.
- Protocol-open payloads, unknown fields, and raw bytes MUST be retained behind
  defensive-copy accessors. Large JSON integers MUST retain `json.Number`
  semantics.
- The normative route order is defined in `spec/realtime.md`.
- Direct/lifecycle dispatch MUST invoke its exact route followed by the global
  wildcard; dispatching the global wildcard itself MUST invoke it only once.
- Protocol-open handler routing MUST retain unknown exact values, while
  observer and error-hook route/message fields MUST map unknown values to the
  closed `private.other`, `group.other`, `event.other`, `trigger.other`,
  `other`, and message `other` buckets.

### Webhook

- The receiver MUST consume exact raw bytes through a framework-neutral
  request type and MUST enforce bounded body, timestamp, dedupe, and callback
  execution limits.
- Challenge verification MUST use lowercase MD5 of
  `rn + timestamp + checkToken`, timing-safe comparison, and the configured
  tolerance.
- Encrypted private/group callbacks MUST support URL-safe Base64,
  AES-128/192/256-ECB, PKCS#7 unpadding, JSON, and private XML fallback.
- A duplicate MUST return success without redispatch. Handler failure after
  accepted submission MUST preserve `200 success`. Executor rejection MUST
  return backpressure and MUST NOT insert a dedupe marker.
- Webhook registration MUST reject non-HTTPS URLs before I/O, use the existing
  authenticated transport, send matching token/key plus
  `receiveMsgState: 1`, and omit `enWebSocket`.

### WebSocket

- Phase 1 MUST call `POST https://{gateway}/open/ws/endpoint` with the raw app
  key/secret required by that route. This is the only raw-secret transport
  exception. Success requires exact HTTP 200, explicit `code == 0`, and a
  non-blank `data.url`.
- Phase 2 MUST preserve `ws`/`wss` when replacing an optional connect host,
  send `X-Source`, disable compression, extract `connection_id`, and use one
  continuous binary read loop.
- A Session MUST expose `disconnected`, `connecting`, `connected`, and
  `reconnecting`. One goroutine MUST own state. Transport/timer callbacks MUST
  only enqueue generation-tagged events; user handlers MUST NOT run on the
  state owner; all writes MUST use one serialized path.
- Control, receive, send, callback, and writer queues MUST be bounded and each
  overflow MUST have an explicit error/reconnect outcome.
- `Connect`, `Disconnect`, `SendFrame`, and `SendData` MUST complete through
  their context/result path. Concurrent `Connect` calls MUST coalesce onto one
  endpoint/dial attempt.
- Open MUST send an immediate business ping and schedule heartbeat. Only
  Phase-1 endpoint `client_config` may override reconnect and ping policy;
  pong payloads MUST NOT mutate session policy.
- Phase-2 and underlying transport failures MUST dispatch the shared `error`
  lifecycle route with closed operation/category fields only.
- DATA callback capacity MUST be accepted before automatic ACK is queued.
  Rejected callback submission means no ACK. Once accepted, ACK MUST NOT wait
  for handler completion and MUST preserve the inbound log ID and ACK sequence.
- `AutoAck` and `AutoRegister` MUST use pointer booleans so explicit false is
  preserved; omitted values default to true.
- A malformed string-valued `Encrypt` payload MUST report a safe diagnostic,
  retain the outer raw payload as the classification fallback, and MUST NOT
  close the connection.
- Passive close/error, receive failure, heartbeat failure, and every actual
  writer I/O failure MUST enter at most one reconnect loop. Every attempt MUST
  allocate a fresh endpoint. Old-generation events MUST be ignored and a late
  old transport MUST be closed.
- Manual disconnect MUST cancel in-flight connect/reconnect and timers, close
  the current transport, and prevent reopening. A later explicit Connect MUST
  re-enable connection and reconnect behavior.
- Automatic callback-mode registration MUST make at most one submission per
  Session. Failure MUST be observable but MUST NOT retry or disconnect.
- Explicit WebSocket registration MUST be synchronous so an operator can
  disable delayed auto-registration and order registration before callback
  restoration. Sensitive endpoint/callback POSTs MUST reject every 3xx without
  replaying the request body or mutating a caller-owned HTTP client.

## Runtime behavior and security

- Original app secrets are lower-case MD5 encoded only in token requests,
  except for the isolated raw-secret WebSocket endpoint allocation contract.
- App tokens are cached, refreshed early, and synchronized under concurrency.
- Non-authenticated-exception requests use the exact `Bearer-` Authorization
  scheme.
- Non-2xx, malformed JSON, auth, nested business, configuration, endpoint, and
  session failures remain distinguishable through typed or closed-category
  errors.
- No L1 message request is automatically retried.
- Zero optional durations select defaults; negative durations are invalid.
- Generated group client message IDs are positive and strictly increasing for
  one Client, even with a fixed/backward clock or concurrent calls.
- L1 emits no logs or telemetry. Realtime diagnostics are opt-in, structured,
  closed-schema, and redacted.
- Observer/error-hook implementations MAY be called concurrently, MUST be
  concurrency safe, and SHOULD return quickly. SDK behavior MUST survive a
  callback panic.
- Credentials, tokens, Authorization values, endpoint URLs/query strings,
  callback token/key, headers, raw frames, decrypted payloads, message content,
  targets, response bodies, and underlying error strings MUST NOT enter
  diagnostics or error-hook values.
- The generated HTTP transport preserves Go's default environment proxy
  selection; an injected HTTP client remains caller-owned.

## Validation and release boundary

Local validation requires format, vet, all tests, and race tests on Go 1.25.x
and 1.26.x, including the machine-readable realtime conformance matrix. Unit
tests MUST NOT contact the platform.

Real-service WebSocket and Webhook smoke remains a required later gate. The
common-robot owner-waiver flow MUST use the exact final-WebSocket gate, run
Webhook before WebSocket, stop listeners and disconnect the SDK session, and
record restoration as not applicable without issuing a restoration POST. The
existing explicit restoration path remains available for other runs. Until the
required exact-gated routes run, this repository may claim only
`local_validated`; release/remote readiness remains `blocked_needs_smoke`.
