# Story 002: Go T4 realtime runtime

- Change ID: `2026-07-15-go-java-realtime-runtime`
- Scope: `go-event-webhook`, `go-websocket-reconnect`
- Local status: complete
- Release state: `blocked_needs_smoke`
- Spec refs: `CONTRACT.md`, `spec/realtime.md`, `spec/l0-l1-http.md`

## User outcome

As an InfoFlow Go developer, I can register one handler on `client.Bot.Events` and
receive the same normalized event through a framework-neutral Webhook receiver
or a reconnecting WebSocket Session, without building auth, encryption, Frame,
ACK, heartbeat, or reconnect machinery myself.

## Delivered behavior

- Added shared Client-level `Events`, `Webhook`, and `WebSocket` components
  without changing L1 calls or wire shapes.
- Added ordered private/group/system/trigger/namespace routing with concurrent
  on/once/off, defensive open payloads, and handler-failure isolation.
- Added Webhook challenge verification, AES/PKCS#7 JSON/XML receive paths,
  bounded dedupe/backpressure, and authenticated HTTPS registration.
- Added canonical generated protobuf Frame bindings, dedicated raw-secret
  endpoint allocation, coder/websocket Phase 2, and one state-owner Session.
- Added bounded queues, serialized writes, heartbeat, accepted-submission ACK,
  one-shot automatic mode registration, generation filtering, and
  fresh-endpoint reconnect.
- Added opt-in structured observability with correlation, panic isolation, and
  exact-value security/redaction coverage.

## Frozen local decisions

- Every actual writer I/O failure starts the single reconnect path.
- Concurrent `Connect` calls coalesce onto one active endpoint/dial attempt.
- Automatic registration submits at most once per Session; failure is observed
  without retry or disconnect.
- Callback queue rejection means the DATA event is not ACKed; accepted
  submission is ACKed without waiting for handler completion.
- Malformed string `Encrypt` uses the outer payload for fallback classification
  and diagnostics without terminating the connection.
- Only the latest Phase-1 endpoint `client_config` updates server heartbeat and
  reconnect policy; pong payloads do not.
- `AutoAck` and `AutoRegister` are pointer booleans so explicit false is not
  replaced by a default.

## Local acceptance

The frozen conformance cases cover dispatcher matrices/lifecycle; Webhook
challenge, content/AES, dedupe, dispatch, and registration; WebSocket endpoint,
Phase 2, Frame, heartbeat/ACK, registration, reconnect, concurrency; and L1
compatibility/security. Tests use only fake local dependencies.

Acceptance commands run with `GOTOOLCHAIN=local` on Go 1.25.12 and 1.26.5:

```bash
test -z "$(gofmt -l .)"
go vet ./...
go test ./...
go test -race ./...
git diff --check
```

The SDK-local machine-readable coverage matrix records every frozen case as
`passed`, `blocked`, or `not_applicable`. Implementation, tests, docs,
generated code, and dependency pins are committed together on the local task
branch; the repository has no remote and is not pushed.

## Remaining acceptance

Real-service smoke has not run. The coordinator must supply the exact command
and owner-approved inputs, execute WebSocket and Webhook phases sequentially,
record each required route, and restore the intended callback mode. Missing
live event/public callback inputs block only smoke, but prevent promotion from
`local_validated` to `validated` and prevent release readiness.
