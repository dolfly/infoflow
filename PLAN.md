# Plan

## Completed local delivery: L0/L1

- [x] Approve module, public API, toolchain, and repository rules.
- [x] Implement Client, auth/transport/envelope/error layers, and user/group
  text sends.
- [x] Preserve local validation, raw-copy, concurrency, timeout, proxy, and
  one-attempt behavior.

## Completed local delivery: T4 realtime runtime

- [x] Add `Client.Events`, `Client.Webhook`, and `Client.WebSocket` without
  changing the L1 API or wire behavior.
- [x] Implement the shared route-ordered dispatcher with thread-safe
  on/once/off, raw retention, and handler-failure isolation.
- [x] Implement framework-neutral Webhook challenge, AES/JSON/XML decoding,
  bounded dedupe/backpressure, dispatch, and authenticated HTTPS registration.
- [x] Check in the canonical Frame proto/generated binding and pin only the
  approved coder/websocket and protobuf dependencies.
- [x] Implement endpoint allocation, Phase-2 transport, state-owner session,
  heartbeat, accepted-submission ACK, one-shot registration, and fresh-endpoint
  reconnect.
- [x] Cover the frozen event/mechanism matrix with deterministic fake endpoint,
  transport, scheduler/random, queue-pressure, generation, and race tests.
- [x] Pass format, vet, unit, conformance, and race gates on Go 1.25 and 1.26
  and record SDK-local execution evidence.

The public behavior specification is [spec/realtime.md](spec/realtime.md); the
delivery record is [story/002-realtime-runtime.md](story/002-realtime-runtime.md).

## Remaining gates

- [ ] Run the exact-gated owner-approved WebSocket smoke: endpoint allocation,
  connect, heartbeat, callback-mode registration, routed event, ACK, passive
  reconnect, manual disconnect, and prior callback restoration.
- [ ] Run the exact-gated owner-approved Webhook smoke: registration, public
  challenge, decrypted/deduped routed event, and callback-mode restoration.
- [ ] Record route-level smoke evidence and promote beyond `local_validated`
  only if every required route passes.
- [ ] Define remote/review and manual release metadata in a separate task.

No live smoke, remote, push, tag, publish, or release action is part of the
local T4 implementation. Callback-mode smoke MUST run sequentially and restore
the intended mode.
