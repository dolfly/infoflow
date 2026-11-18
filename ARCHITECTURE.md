# Architecture

## Identity-first package ownership

The root `infoflow` package is intentionally small: it owns only `Client`,
configuration, public errors, and runtime assembly. Public capability types are
discovered from their owning packages:

```text
infoflow
├── bot
│   ├── im
│   ├── robot
│   ├── events
│   ├── webhook
│   ├── websocket
│   └── internal/{callback,realtime}
├── user
│   ├── im
│   ├── calendar
│   ├── meeting
│   └── internal
├── userauthorization
└── internal/{auth,envelope,runtime,sdkerrors,transport}
```

Capability packages never import the aggregate root. Bot-only callback and
realtime machinery stays below `bot/internal`; infrastructure shared by both
identity families stays in root `internal`. Go tests remain colocated by package
as standard `*_test.go` files; the repository does not use a `src` directory.
External consumer tests (`package <pkg>_test`) may import the root to exercise
the public `infoflow.NewClient` facade against a mock server; in-package tests
cannot import the root because the import cycle is rejected at compile time.

The SDK keeps one root package facade while separating protocol and runtime
responsibilities into focused packages:

```text
Client
  +-- IM.Message ----------------> auth -> HTTP transport -> envelope/errors
  +-- Events --------------------> shared transport-neutral Dispatcher
  +-- Webhook.NewReceiver -------+          |
  |   + verify/decrypt/dedupe                +--> application handlers
  |   + Register -------------> auth -> HTTP transport
  +-- WebSocket.NewSession ------+
      + endpoint allocator -> coder/websocket transport
      + single state owner -> bounded queues -> one writer
      + Frame codec -------> generated protobuf binding
      + reconnect/heartbeat/ACK/registration

Realtime Observer / OnError <--- redacted lifecycle records from every T4 layer
```

## Root runtime

`Client` copies and normalizes `Config`, assembles the runtime, and exposes
`client.Bot.IM.Message`, `client.Bot.Events`, `client.Bot.Webhook`, and
`client.Bot.WebSocket`. Webhook receivers and WebSocket sessions created by the
Client inherit the same dispatcher, observer/error hooks, HTTP/auth runtime,
and redaction policy unless an advanced option explicitly replaces a seam.

The existing L1 path is unchanged. Domain methods validate input, obtain a
token, and delegate HTTP work. The token manager alone holds the original
secret for authenticated L1/T4 calls, hashes it for token acquisition, caches
expiry, applies refresh skew, and serializes refresh. The shared HTTP transport
owns versioned URL joining, JSON encoding, headers, deadlines, and HTTP/network
error mapping. Envelope helpers own platform status parsing.

WebSocket endpoint allocation is the one isolated exception: Phase 1 posts the
raw app key and app secret to the configured gateway because that protocol
requires them. The allocator exposes only closed error categories and never
retains or emits credentials, endpoint URLs, query strings, or response bodies.

## Event and Webhook layers

`events.Dispatcher` is independent of receive transport. It classifies
protocol-open payloads into stable route sequences, stores handlers safely
under concurrent registration/dispatch, invokes them outside its registry
lock, and isolates a failing handler from later handlers. Event payload and raw
bytes cross public boundaries only through defensive copies.

`webhook.Receiver` consumes method/query/header/raw-body values rather than a
framework request object. It owns challenge verification, strict timestamp
tolerance, AES-ECB/PKCS#7 decoding, JSON/XML parsing, bounded dedupe, and stable
HTTP responses. Accepted callback work is submitted to a replaceable executor;
executor rejection produces backpressure rather than pretending delivery.
`WebhookService.Register` reuses the existing authenticated Client transport
and token cache instead of creating a second auth path.

## WebSocket layers

The WebSocket runtime is split into four boundaries:

1. `EndpointAllocator` performs Phase-1 endpoint allocation and decodes only
   server policy returned by that allocation.
2. The `coder/websocket` adapter performs Phase 2 with compression disabled,
   one continuous binary read loop, and callback functions that only enqueue
   generation-tagged events.
3. The generated `infoflow.Frame` protobuf binding and codec retain unknown
   fields and protocol-open JSON data without hand-written wire encoding.
4. `Session` owns lifecycle state in one goroutine. Separate bounded queues
   carry control, receive, send, callback, and writer work; user handlers never
   run on the state owner, and one writer path serializes user frames,
   heartbeat, and ACK traffic.

Timers enqueue events through an external scheduler; the state-owner loop does
not sleep. Concurrent `Connect` callers share one active attempt. Each
reconnect uses a new generation and a freshly allocated endpoint. Events from
old generations cannot mutate current state, and a late old transport is
closed. Manual disconnect invalidates the generation and cancels pending
timers/attempts.

DATA delivery first reserves callback capacity. Only accepted submission is
eligible for automatic ACK, and ACK does not wait for handler completion. Any
actual writer I/O failure is treated as connection loss and enters the one
reconnect loop. Phase-1 server reconnect count/interval/nonce and ping interval
override local policy; later pong payloads are events, not configuration input.

## Observability and dependencies

L1 remains silent. T4 uses the neutral `realtime.Observer` and safe
`realtime.ErrorHandler` interfaces. Records use a closed reviewed schema and
SDK-generated correlation IDs. Observer calls may be concurrent, return-path
panics are isolated, and payload-bearing underlying errors are never forwarded.

The runtime uses exactly two approved external dependencies:

- `github.com/coder/websocket` v1.8.15 for RFC 6455 transport
- `google.golang.org/protobuf` v1.36.11 for generated Frame bindings

All other layers use the Go standard library. There is no framework adapter,
generic arbitrary-route API, built-in logger, or retry of non-idempotent L1
message sends.
