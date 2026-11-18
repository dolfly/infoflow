# T4 Realtime Runtime Specification

## Scope and shared runtime

This specification is the SDK-local source of truth for the additive T4 event
receive runtime. It does not change the L1 HTTP/message contract in
`spec/l0-l1-http.md`.

`NewClient` creates one `events.Dispatcher` and exposes it as `client.Bot.Events`.
`client.Bot.Webhook.NewReceiver` and `client.Bot.WebSocket.NewSession` use that same
dispatcher, the Client's realtime observer/error hooks, and the appropriate
existing auth/HTTP runtime unless an advanced option supplies an explicit
override. Constructors perform validation only and do not contact the network.

## Event model and routing

`events.Event` exposes normalized classification fields plus `Payload()` and
`RawPayload()` defensive copies. JSON decoding retains large integers as
`json.Number`; unknown protocol-open fields are preserved. Challenge payloads
are verification traffic and are never dispatched.

`On` and `Once` retain insertion order. `Once` removes its registration before
invocation. `Off` removes the registration associated with the original
handler. Registration and dispatch are concurrency safe, handlers execute
outside the registry lock, and one handler error does not block later handlers.

The dispatcher invokes matching routes in this exact order:

| Input class | Routes |
|---|---|
| private message with type `t` | `private.t`, `t`, `private.*`, `*` |
| group message with type `t` | `group.t`, `t`, `group.*`, `*` |
| private/group message with present but empty type | chat wildcard, then `*` |
| private message with missing type | no dispatch |
| group system event `E` | `group.event.e`, `group.event.*`, `*` |
| trigger type `T` | `trigger.t`, `trigger.*`, `*` |
| auth/interaction/summary/other event namespace `E` | `event.e`, `event.*`, `*` |
| direct/lifecycle route `R` | `R`, `*` (or `*` once when `R == "*"`) |

Normalization is lowercase. A leading `GROUP_` is removed for group-system
route names. The supported conformance matrices include private text/image/
file/richtext/markdown/voice; group text/image/file/richtext/markdown/mixed/
voice; group lifecycle/menu/emoji events; auth success/rejection; interaction
submit; message summaries; and trigger casing variants.

## Realtime diagnostics

`Config.Realtime` accepts an `Observer`, a function-style `Diagnostics`
observer, and an `OnError` safe-category hook. Observer records use a closed
field set, required SDK-generated request IDs, `sdk_language=go`, and
`redaction=sensitive_fields_omitted`. There is no default output.

Endpoint allocation, session state, writer, callback executor, Webhook, and
dispatcher paths can invoke observers concurrently. Implementations must be
concurrency safe and should return quickly. The SDK isolates observer and
error-hook panics.

Records and callback errors never carry app credentials, app/access tokens,
Authorization values, endpoint URLs or query strings, callback token/AES key,
headers, request/response bodies, raw frames, decrypted payloads, message
content, targets, or underlying error strings. L1 calls do not emit T4
diagnostics.

## Webhook receive mode

### Framework boundary and defaults

`webhook.Receiver.Handle` accepts `webhook.Request` containing method, query,
headers, and the exact raw body bytes. Framework adapters translate the
returned `Status` and `Body`; the SDK has no framework dependency.

Zero-value options select:

- maximum body size: 20 MiB
- challenge timestamp tolerance: five minutes
- dedupe TTL: five minutes
- dedupe capacity: 1000
- callback executor: inline accepted execution

Negative limits/durations are invalid. A custom executor returns true only
when work has been accepted and will run. Rejection returns
`503 dispatch unavailable` and does not consume the dedupe key, allowing the
platform to retry.

### Challenge and encrypted callbacks

For challenge form traffic, the expected lowercase signature is MD5 of
`rn + timestamp + checkToken`. Form values take precedence over query values.
Timestamp seconds and milliseconds are accepted within the configured strict
tolerance. The comparison is timing safe; missing token, invalid/expired
timestamp, or a bad signature returns 403.

Private form callbacks read `messageJson.Encrypt`, decode URL-safe Base64,
decrypt AES-128/192/256 in ECB mode, remove PKCS#7 padding, then parse JSON with
XML fallback. Group `text/plain` callbacks use the equivalent encrypted JSON
path. Invalid keys, ciphertext, padding, or payloads fail with stable responses
without exposing content.

The dedupe key uses protocol message IDs when available and otherwise a stable
fallback. An accepted duplicate returns `200 success` without redispatch.
Handler failure is isolated and an accepted callback still returns
`200 success`.

### Webhook callback registration

`client.Bot.Webhook.Register` rejects missing fields or a non-HTTPS callback URL
before I/O. It reuses the Client token manager and authenticated transport to
call `POST /imRobot/updateReCallUrl` with:

```json
{
  "recallUrl": "https://example.test/infoflow/events",
  "token": "matching-check-token",
  "encodingAesKey": "matching-aes-key",
  "receiveMsgState": 1
}
```

The request omits `enWebSocket`, uses a numeric `LOGID`, and sends
`X-OP-ACCESS-CHANNEL: go_sdk`. Registration switches platform callback delivery
to Webhook mode; applications/coordinators are responsible for intentional
mode changes and restoration.

## WebSocket protocol

### Phase 1: endpoint allocation

Every initial connect or reconnect calls
`POST https://{gateway}/open/ws/endpoint`. The JSON body contains the original
`app_key` and `app_secret` required by this isolated route. Gateway precedence
is explicit `SessionOptions.Gateway`, `INFOFLOW_WS_GATEWAY`, then
`infoflow-open-gateway.baidu.com`. Success requires HTTP 200, explicit numeric
`code == 0`, and a non-blank `data.url`.

The optional Phase-1 `client_config` is converted from seconds into Go
durations and is the only server-policy input:

- `reconnect_count` (`-1` means unlimited)
- `reconnect_interval`
- `reconnect_nonce` (maximum injectable jitter)
- `ping_interval`

Pong payloads are dispatched as heartbeat events but never mutate policy.
Endpoint errors expose only closed categories and never retain the raw secret,
URL/query, response body, or underlying error text.

### Phase 2 and Frame

Phase 2 dials the allocated `ws`/`wss` URL immediately, optionally replaces
only its host from `ConnectDomain`/`INFOFLOW_WS_CONNECT_DOMAIN`, adds
`X-Source` (default `go_sdk`; explicit source prefixes normalize to
`{prefix}_go_sdk`), disables compression, and extracts `connection_id`. The
coder/websocket adapter uses one continuous binary read loop; transport
callbacks only enqueue copied, generation-tagged events.

`proto/frame.proto` is canonical and its generated binding is checked in.
Methods are CONTROL=0, DATA=1, REQUEST=2, and RESPONSE=3. Encoding is
deterministic, decoding retains protobuf unknown fields, public Frame values
are defensively cloned, and open JSON retains large integers as `json.Number`.

For a DATA payload with string-valued `Encrypt`, valid inner JSON is merged
only for route classification while the original outer bytes remain the raw
payload. Malformed inner JSON emits the safe `malformed_encrypt` diagnostic,
uses the outer object as the classification fallback, and does not close the
connection.

## WebSocket session lifecycle

`websocket.Session` exposes four states:

```text
disconnected --Connect--> connecting --open--> connected
     ^                           |                 |
     |                           +--failure-------+
     |                                             |
     +--manual disconnect--------------------------+
                                                   |
                         passive/write loss -> reconnecting
                                                   |
                               fresh endpoint/open-+
```

One goroutine owns state. Control, receive, send, callback, and writer queues
are independently bounded (default 64 each); each overflow reports an explicit
failure or reconnect outcome. Timers are external scheduler callbacks that
enqueue events. The state loop neither sleeps nor executes user handlers. One
writer serializes user frames, ping, and ACK.

`Connect` completes only after Phase 2 succeeds. Concurrent calls while
connecting/reconnecting coalesce onto the current attempt. `SendFrame` clones
the Frame; `SendData` creates a DATA Frame and assigns a sequence. Send fails
explicitly when disconnected, timed out, or a queue is full.

Open sends an immediate business ping and schedules the next heartbeat.
CONTROL pong dispatches route `heartbeat`; lifecycle routes `connected`,
`disconnected`, Phase-2/transport failures route `error`, and RESPONSE route
`response` use the shared dispatcher. Error-event payloads contain only closed
operation/category values, never an underlying error or endpoint.

### Callback admission and ACK

DATA processing reserves callback capacity before ACK eligibility. If callback
submission is rejected, no ACK is queued. Once accepted, dispatch runs off the
state owner and ACK is queued immediately when `AutoAck` is true; ACK does not
wait for handler completion. The ACK copies the inbound `logId`, carries the
inbound sequence as `ackSeqId`, and uses a new outbound sequence. Disconnected
or explicitly disabled sessions do not ACK.

`AutoAck` is `*bool`: nil means the default true and a pointer to false remains
false. The same explicit-optional rule applies to `AutoRegister`.

### Automatic registration

When `AutoRegister` is enabled, the first successful connection schedules one
registration attempt after the configured delay. It sends a random 32-character
lowercase hexadecimal token with:

```json
{"enWebSocket":1,"receiveMsgState":1,"token":"<32 lowercase hex>"}
```

The registrar reuses Client auth/HTTP and the callback-update route. There is
at most one submission for the entire Session, including across reconnects or
a later explicit Connect. A failure is reported but is not retried and does
not disconnect.

The Client also exposes synchronous `client.Bot.WebSocket.Register(ctx, token)`.
Operational flows that must restore an earlier callback mode disable
`AutoRegister`, complete this call explicitly, and only then begin restoration.
The endpoint-allocation and callback-registration HTTP clients reject every
3xx response without following or replaying the sensitive POST, while leaving
an injected caller-owned HTTP client unchanged.

### Loss and reconnect

Passive close, transport error, receive overflow/failure, heartbeat failure,
and every actual writer I/O failure enter at most one reconnect loop. Each
attempt increments generation and allocates a fresh one-time endpoint. Old
message/error/close/connect-result events cannot affect the new generation; a
late old transport is closed.

Local reconnect defaults are 120 seconds plus zero jitter and unlimited
attempts. Phase-1 server count/interval/nonce overrides them. Jitter is clamped
to `[0, nonce]` and injectable for deterministic use/tests.

Manual `Disconnect` cancels heartbeat, registration, reconnect timers, and any
in-flight attempt; closes the current transport; invalidates the generation;
and prevents a queued reconnect from reopening. A later explicit `Connect`
starts a fresh connection lifecycle and re-enables automatic reconnect. The
per-Session one-attempt registration policy remains unchanged.

## Validation and live boundary

Local tests use fake endpoint/transport/scheduler/random/executor components;
they never contact InfoFlow. The frozen route/mechanism matrix, both supported
Go versions, vet, unit tests, and race tests are the local gate.

Real-service smoke is separate and exact-gated by
`INFOFLOW_RUN_SAFE_ONLINE_REALTIME_SMOKE=1`. WebSocket smoke must cover endpoint,
connect, heartbeat, registration, routed event, ACK, passive reconnect, and
manual disconnect. Its passive-reconnect phase actively interrupts the current
transport through a smoke-only dialer wrapper, then requires a fresh dial and
successful reconnect; lifecycle wildcard events do not satisfy the business
event phase.
Webhook smoke must cover registration, public challenge, decrypted/deduped
dispatch, and listener cleanup. The explicit
`INFOFLOW_REALTIME_SMOKE_FINAL_MODE=websocket` gate applies the owner waiver:
run Webhook before WebSocket, omit prior-mode restoration inputs, mark the
restore route `not_applicable` with
`owner_waiver: final_websocket_mode`, and leave the platform in WebSocket
callback mode after the SDK session disconnects. Missing restoration inputs
never imply the waiver. Without that exact gate, the existing restoration path
and its complete inputs remain required. WebSocket smoke disables delayed
automatic registration and waits for explicit synchronous registration to
finish before cleanup, including after SIGINT/SIGTERM.
Until those sequential routes are recorded, status remains
`local_validated` / `blocked_needs_smoke`.
