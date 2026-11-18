# Changelog

## v0.2.2 - 2026-11-18

- Restructure documentation: `README.md` is now a concise entry point, and the
  install, quick start, usage, AI dynamic card, FAQ, testing, and API reference
  pages live under `docs/`. No public API changes since v0.2.0.
- Add a MkDocs Material documentation site (`mkdocs.yml`) and a GitHub Actions
  workflow that builds it and publishes `site/` to the `gh-pages` branch.
  GitHub Pages still needs to be enabled in repository settings (the mirror is
  a private repository on a Free plan).
- Move iCode-only release and regression documentation to `docs/internal/`.
  `scripts/upstream_push` now removes `docs/internal`, `go.env`, `ci.yml`,
  agent guides, and worker evidence from the GitHub mirror, in addition to
  rewriting the module path and regenerating protobuf bindings.
- Remove internal proxy and internal guide links from public documentation and
  from the Makefile/CI comments.
- Publish the GitHub mirror as a single root commit with a neutral author
  identity, so source commit messages, authors, and the internal module path do
  not appear in the upstream history.

## v0.2.0 - 2026-11-04

- Align the dynamic-card public model with the platform's AI dynamic message
  guide v37 (2026-11-03). **Behavior changes for @ only:**
  `AtOptions.AtAll` / `AtUserIDs` are renamed to `All` / `Users`, the default
  `Transport` is `body` (the `header` write of `message.header.extra.at` is now
  an explicit legacy opt-in and is no longer emitted by default), and the body
  AT node is written as the top-level `message.body =
  [{"type":"AT","atall":<bool>,"atusers":[...]}]`, a sibling of `contents`.
  The old `atuserids` / `atagentids` body keys are gone: the platform accepted
  them with HTTP 200 but clients showed no AT, so a markdown `@<uuapId>` must
  also appear in the text (`AutoMentionText` injects missing mentions;
  otherwise `CardRef.Warnings` carries `at_mention_text_missing`).
  `atall` and `atusers` are independent and may be emitted together, matching
  the v37 §8.3 example `{"type":"AT","atall":true,"atusers":["a","b"]}`
  (@all plus named members).
  `AtAgentIDs` is kept only as a deprecated field and never reaches the wire;
  `transport: "both"` is rejected locally. Add `ActionBarComponent` /
  `ActionBarItem` (`action_bar`, keys `ai_tag` / `tts` / `vote_up` / `vote_down`
  / `forward`, unknown keys and fields pass through) and the top-level
  `contents.footer` region (`Footer` / `NewFooter`, structurally identical to
  `body`, carried by send and by a full update). Card events now expose
  `CardEventActionFooterOperation` with `element_id` = action_bar id,
  `action.value` = operation key and the original `client_context` object
  array (`source_platform`, numeric `state` 1/0, `biz_extra`) preserved through
  `ClientContextValue.Raw`. `UpdateResult.Sequence` is documented as usable as
  the `enable.sequence` of a subsequent stream and `AckResult.Sequence` stays
  nullable (only when a `card_update` was applied). No new dependencies.

- Add the AI dynamic-card (scheme `1`) contract foundation as the new public
  package `bot/dynamiccard`: the eight-component content model (`markdown`,
  `text`, `button`, `input`, `radio_group`, `row`, `column`) with a
  `RawComponent` passthrough and per-component `Extra` flat passthrough (known
  fields win), `NewContent`/`DynamicCardContent`, the three-layer response
  decision (`DecodeResponse`, `CardResponse`), the unified `DynamicCardError`
  with 12 categories, 8 business codes and 14 HTTP statuses, the local P0/P1
  prechecks (`ValidateContent`, 63,000 UTF-8 bytes, JSON depth 14, advisory
  warnings only), and the lossless `ID` type that stores 19-digit identifiers
  as `int64` and always serializes them as JSON strings. Additive: no existing
  client method, type, wire field or JSON field name changes. The client-scoped
  dynamic-card capability is delivered by the bullets below.

- Add `client.Bot.IM.DynamicCard.Send` for AI dynamic messages
  (`POST /app/message/sendUnifiedMsg`, qyopenapi profile). Exactly one of
  `ToUser` / `GroupID` must be non-blank and the SDK derives
  `message.header.toType` / `message.header.toId` from it; `SessionID` is
  passed through verbatim to `message.header.extra.sessionId` and is omitted
  when empty; `msgType=INTERACTIVE`, `role=robot` and `contents.scheme="1"` are
  fixed. `SendRequest.Raw` only supplements fields the SDK did not set
  (known fields win at every level, and `header.toId` is fully SDK-derived, so
  raw cannot inject the other target type). Local target/content validation,
  including the 63,000-byte limit, fails with a `validation`
  `DynamicCardError` without sending. The result `CardRef` exposes `CardID`,
  `Secret`, `MsgKey`, `MsgSeqID`, `MessageID` and `CTime` as strings so a
  19-digit `msgkey` keeps every digit; `Secret` is sensitive and must never be
  logged. Sends are never retried automatically, including for `6001`/`9001`.
  The dynamic-card transport resolves the qyopenapi base itself: the
  `INFOFLOW_DYNAMIC_CARD_BASE_URL` environment variable overrides the
  production default, and `Config.BaseURL` is not reused. Additive: no
  existing method, type, wire field or JSON field name changes.

- Add `client.Bot.IM.DynamicCard.NewStream` for a first-class streaming card
  session (`create -> enable -> append x N -> finish`, qyopenapi profile).
  `NewStream(ctx, StreamOptions)` reuses `Send` to create a card whose only
  component is the markdown skeleton `{type,id,content}` (`ElementID` defaults
  to `desc`) and returns a `*DynamicCardStream` in `created` with
  `Sequence=1`; it never enables automatically. `Enable` posts
  `modifyStreaming` with `action=enable` and one or more markdown
  `streaming_elements`; `Append` posts a complete snapshot to
  `streaming/append`; `AppendDelta` concatenates at the Unicode string level
  (an empty delta sends nothing) and keeps one snapshot shared with `Append`;
  `Finish` posts `action=finish` with a children-only body that never carries
  `scheme`, sets any `agent-flow-stream` status to `completed` (also inside a
  JSON skeleton in markdown content and in the markdown fence header, where only
  the first `status="..."` value becomes `completed` and every other byte is
  preserved), waits `DelayMs` when set, and is
  idempotent once finished. Mutations serialize per `card_id` while different
  cards run in parallel; appends inside the default 1000ms throttle window
  (use `Int(0)` to disable) coalesce to the newest snapshot and every waiting
  caller completes with the covering request's result; appends are rejected
  locally once finish starts. `sequence` is only ever taken from a successful
  response (a missing value is a protocol/server failure and moves the session
  to `failed`); `1009` adopts `current_sequence` and returns
  `sequence_conflict` with `RequestSequence`/`CurrentSequence`/
  `CurrentContent` without replay; `6001` retries once after a fixed 1000ms
  backoff with the newest snapshot and an unchanged sequence, then returns
  `rate_limited`; `1010` maps repeat enable / append-before-enable to `failed`
  and an in-stream conflict to `expired`; 60 minutes after create (`StreamLifetime`)
  `State()` reports `expired` and append/finish fail locally; `1001`/`3003`/`401`
  move the session to `failed` while `9001`/5xx keep it unchanged. Additive: the
  legacy `StreamingCardSession` and every existing route, method and wire field
  are unchanged.

- Add structured @ and quoted-reply support to dynamic-card sends. Both
  `SendRequest` and `StreamOptions` accept `At *AtOptions` (`All`, `Users`,
  `Transport`, `AutoMentionText`) and `Reply *ReplyOptions`
  (`MessageID`, `Digest`, `SenderName`), emitted as the top-level `message.body`
  AT node and/or `message.header.extra.reply` of the first `sendUnifiedMsg`.
  The default body form is
  `{"type":"AT","atall":...,"atusers":[...]}`: an empty user list is omitted and
  `atall` is always emitted (`All=true` without users emits only
  `{"type":"AT","atall":true}`; `All=true` with users emits both fields).
  `Transport` is `body` (default) or the legacy
  `header`; a blank user id and an unknown transport fail local
  `validation` without a request. `MessageID` is always sent as a string so a
  19-digit id keeps every digit;
  `FormatReplyMessageID` converts a numeric id and rejects negative or
  IEEE-754-unsafe values. `reply.digest` / `reply.senderName` are omitted when
  empty. `StreamOptions.Raw` exposes the same escape hatch as `SendRequest.Raw`
  for the card-creating send. When `sessionId`, `at` and `reply` coexist all
  three are preserved; raw can only supplement unknown fields and never
  overrides the typed values or fixed header fields. Without `at`/`reply` the
  request body is unchanged. At/Reply only affect the first send; later
  enable/append/finish/update cannot change the message header.

- Write the AT declaration into the top-level `message.body` at the same level
  as `contents`. An existing caller-provided array under `raw.message.body` is
  preserved and the SDK AT node is appended after it; a non-array value there is
  a local `validation` failure when an AT node must be written. Without `At` no
  `message.body` field is written.

- Fix `CardRef.MessageID` parsing. It now reads
  `data.data.partial.data.messageid` first, then `data.data.messageid` /
  `message_id`, then `partial.data.msgid` / `msg_id`, and only then falls back
  to `partial.msgkey` (rendered as its decimal string). `MsgSeqID` and `CTime`
  also prefer `partial.data.*`. Null or absent values are skipped instead of
  becoming `""`, `"null"` or `"<nil>"` text.

- Add `bot/dynamiccard.Markdown`, a pure string-rendering helper for the
  dynamic-card markdown syntax (`var Markdown = MarkdownHelper{}`):
  `AtUser` / `AtAgent` / `AtAll`, `Color`, `Link`, `Image`, `Divider`,
  `CodeBlock`, `Table`, `ReplyQuote`, `ButtonGroup`, `AgentFlowStream` and
  `Join`. Every helper returns `(string, error)` and invalid arguments fail with
  the existing `validation` `DynamicCardError`. Output bytes match the
  cross-language markdown spec, including adaptive code fences, escaped reply
  attributes, the closed colour set and the `:::reasoning` / `:::tool`
  `agent-flow-stream` blocks; `AgentFlowStream` builds the same fenced structure
  that `Finish` marks completed.


- Add `client.Bot.IM.DynamicCard.Update` for the AI dynamic-card update route
  (`POST /interactive/message/update`, qyopenapi profile). `UpdateType` is a
  discriminated input: `full` requires `CardContent` and forbids `Updates`;
  `element` requires complete components carrying `type` + `id`, a globally
  unique id set within the request and no ancestor/descendant pair, and forbids
  `CardContent`. Neither mode can produce a `sequence` field. Local validation
  covers structure and request-internal rules; the optional `CurrentContent`
  baseline additionally checks target existence, unchanged component type,
  cross-level ancestor/descendant conflicts and container-children
  completeness (a row/column replacement that drops baseline children yields a
  non-blocking `container_children_missing` warning). A successful result
  exposes `CardID` (falling back to the request value), `Sequence` (with a
  `sequence_missing` warning when the platform omits it), `Message`, `Warnings`
  and `Raw`. A `1010` response is classified `stream_state`, its message
  contains `请先 finish 流式消息`, and `CurrentSequence`/`CurrentContent` are
  populated when present. `1001` maps to `validation`, `3003` to `ownership`,
  and updates are never retried automatically. Additive: no existing method,
  type, wire field or JSON field name changes.

- Add `client.Bot.IM.DynamicCard.Ack` for interactive card-event ACKs
  (`POST /interactive/message/card_event_ack`, qyopenapi profile), plus the
  `CardEvent` shortcut `AckEvent` (it reads only `CardID` / `EventID` and never
  derives a `card_update`). The payload is
  `{card_id, event_id, ack_data:{code, mode, toast?, card_update?}}`;
  `sync` may carry a toast and a `card_update`, while `async` requires
  `code=0`, forbids `card_update` and only signals acceptance. `CardUpdate`
  mirrors the update request without `card_id`/`sequence` (its JSON encoding
  cannot emit either field): `full` needs a scheme `"1"` content and no
  `updates`, `element` needs complete components with `type` + `id`, unique ids
  and no ancestor/descendant pair. The local validation matrix (blank ids,
  missing/invalid mode, non-zero code with async or without toast or with a
  `card_update`, async with `card_update`, empty toast content, invalid toast
  level, mismatched `update_type`, `element` items missing `type`/`id`) returns
  `validation` without sending a request. The 4 second window is measured from
  `AckRequest.ReceivedAt`: a late `sync` ACK is still sent with the
  `ack_window_exceeded_toast_suppressed` warning, a late `async` ACK is sent by
  default (the platform answers `1011`) unless `StrictWindow` is set, and an
  event older than 10 minutes is rejected locally as `ack_expired`.
  `AckResult` normalizes `Accepted`, `AlreadyProcessed` (the platform's
  `ack already processed` is a success), `CardID`, `EventID`, `Sequence`,
  `Message`, `Mode`, `AgeMs`, `ProceedWithBusiness` (true only for an accepted
  async ACK), `Warnings` and `Raw`. `1001` maps to `validation`, `1010` to
  `stream_state` without `current_*` fields, `1011` to `ack_expired`, `3003` to
  `ownership`, `9001` to `server`, `401` to `auth` and a gateway string code to
  `gateway`. ACKs are never retried. Additive: no existing method, type, route
  or wire field changes.

- Receive interactive card events through the existing Webhook receiver. An
  encrypted `messageJson` envelope or the new `message` XML fallback is
  decrypted (AES-128-ECB / PKCS5; the key is the URL-safe Base64 decoding of
  `EncodingAESKey + "=="`) and parsed as JSON with an XML fallback. A frame
  whose `EventType` is exactly `INTERACTIVE_MSG_CARD_EVENT` is normalized into
  `dynamiccard.CardEvent` (19-digit `msg_sbasemsgid`, `chat_id` and `from_id`
  are carried as strings; `client_context` keeps its string-or-string-array
  shape), deduplicated on `card_id + ":" + EventId` for 10 minutes, and
  dispatched on the `event.interactive_msg_card_event` route (`event.*` and `*`
  included) through the new `Dispatcher.OnCardEvent`. The receiver returns HTTP
  200 `success` after decryption/parsing/dedupe and before business handlers
  run: with the default executor card-event work is handed to a goroutine, so
  the response never blocks on a handler, while an injected
  `CallbackExecutor` keeps control of scheduling. An invalid frame (`EventData`
  or `card_id` missing, malformed content) is discarded with an error report
  and still returns 200; a duplicate returns 200 without a second dispatch.
  The response is not an ACK and never calls the event-center ACK; event
  bodies, ciphertext, tokens and `encodingAesKey` are never logged.
  `CardEventAck` is delivered by the separate `Ack` bullet above it. Additive:
  no existing method, route or wire field changes.

- Fix `im.FileService.Upload` BOS objectKey safety. When `ObjectKey` is blank
  the SDK now generates a pure-ASCII `sdk-uploads/{yyyyMMdd}/{uuid}{ext}` key
  instead of letting the platform fall back to `FileName`; a non-ASCII file
  name previously uploaded successfully but then failed the `getUrl` lookup
  (`40001 非法objectKey`), and a retry with the same key returned `40900`.
  Behavior change: an omitted `ObjectKey` no longer resolves to `FileName`.
  A caller-supplied `ObjectKey` is now validated before upload (allowed
  characters `[A-Za-z0-9._/-]`, non-empty, non-`.`/`..` path segments, length
  ≤ 1024) and a `FileName` containing `/` is rejected up front, both as
  a parameter error (`*sdkerrors.ConfigError`) without sending a request.

- Fix `im.FileService.GetURL` business errors dropping the platform reason:
  `parseBosURLResponse` now passes the platform `message`/`msg`/`errmsg` into
  the returned `BusinessError` (previously an empty string) and retains the raw
  error envelope, so callers can read `business.Code` (`40001`) together with
  the platform message.

- Add `client.Bot.IM.Message.SendGroupMarkdownWithMentions` and the pure
  `BuildGroupMarkdownMentionBody` helper for Markdown group messages with
  @-mentions. For each requested user/robot target whose literal `@<id>` is
  missing from the Markdown, the helper appends it to the content (already
  present targets stay in place), preserves list order without
  de-duplication, and emits the `AT` block(s) before the `MD` block.
  `atall=true` short-circuits and ignores the user/robot lists. The sender
  requires a non-blank Markdown, enforces ≤50 users and ≤10 robots, and
  rejects a `SelfAgentID` that appears in `AtAgentIDs` (the platform forbids
  @ self). Additive: `SendGroupMessage`, `validGroupBody`, and all existing
  wire behavior are unchanged.
- Unified source identifiers: the HTTP `X-OP-ACCESS-CHANNEL` header and the
  WS handshake `X-Source` header both default to `go_sdk` when unset (empty or
  whitespace). Explicit values are treated as a source prefix and normalized
  to `{prefix}_go_sdk`; values that already carry the suffix (including the
  default `go_sdk`) are kept unchanged — no `go_sdk_go_sdk`.

- Breaking: `im.SidebarRequest.AutoOpen` / `CustomSidebar` change from `int` to
  `*int` (`json:"...,omitempty"`). `nil` means "do not change" and the field is
  omitted from the request body; pass explicit 0/1 values via the new
  `botim.Int(v int) *int` helper when the flags must be set. Previously the
  zero value was always serialized, which made a link-only call also declare
  both switches as 0 and require group-manager permission.

- Fix `client.Bot.Calendar.CreateEvent` / `UpdateEvent` request shape (discovered
  in real-service smoke): the qyOpenApi platform rejects a missing/empty
  `bodyType` (`BodyType参数错误`), a missing/null `conference`
  (`conference不能为null`), and `conference.bzType != 1`, all with business code
  `410`. `EventRequest.BodyType` and `EventRequest.Conference` now have safe
  defaults (`"Text"` and `{"bzType":1}`) applied when the caller leaves the
  zero value, so natural usage produces an accepted request. Explicit values
  are preserved.

- Fix `client.Bot.Calendar.GetFreeBusy` response parsing (QA CAL-005): the
  qyOpenApi `freeBusy` route returns either an object
  (`data.freeBusyList`) or a bare array (`data.data` 数组 + snake_case 字段).
  `FreeBusyResult.FreeBusyList` is now `[]FreeBusyItem` with typed
  `Email` / `Appointments` (`start_time`/`end_time`) / `BusySlots`
  (`start`/`end`), aligned with the Node reference `FreeBusyItem`; `busySlots`
  is always populated (derived from `appointments` when absent). The previous
  always-empty `FreeBusyResult.Email` field and raw `[]json.RawMessage`
  `FreeBusyList` element type are removed.

- Add first-class `app_msg` / `work_card` support to group message reads.
  `MessageItem.WorkCard()` normalizes `content` into a typed `im.WorkCardContent`
  exposing `cardId` (missing, null, or non-string `cardId` falls back to an
  empty string instead of becoming unknown), and `WorkCardText()` /
  `WorkCardTextLine()` render the transcript placeholder
  `[工作卡片 cardId=<cardId>]` (or `[工作卡片]` when the cardId is absent) with a
  Beijing-time `[<time>] [msgId:<msgId>]` prefix and a `bot#<agentId>` sender
  label for robot senders (humans keep their `uuapId` label). Backward
  compatible: the raw `Content` / `Sender` JSON fields are unchanged.

- Add three additive Bot IM capability families under `client.Bot.IM`,
  matching the Node/Python public surface without changing existing behavior:
  - `File` (`client.Bot.IM.File`): BOS multipart `Upload`
    (`POST /im/bos/upload` on the fixed
    `https://infoflow-open-gateway.baidu.com` gateway), temporary presigned
    `GetURL` (`GET /im/bos/getUrl`), chat-file download URLs
    (`GetDownloadURLPrivate` /
    `GetDownloadURLGroup` on
    `/open-file-service/file/get/download/url/*byFid`), and authenticated byte
    `Download` using the `x-openapi-gateway-identity` app-token header.
    Upload-then-getUrl recovery and sensitive URL redaction match Node
    `FileApi`.
  - `StreamingCardSession`
    (`client.Bot.IM.Message.NewStreamingCardSession`): a lifecycle wrapper over
    the raw streaming-card create/update-user/update-group routes with
    `Start` / `AppendText` / `AppendReasoning` / `NotifyToolStart` /
    `NotifyToolResult` / `Complete` / `Fail`, including the independent
    failure path that pushes an error state to the card.
  - `RenderBubbleWorkflowCard`
    (`client.Bot.IM.Message.RenderBubbleWorkflowCard` posting to
    `/robot/card/bubble-workflow-card/render` with the B-bubble envelope and
    `general_form` layout constraints) and the typed
    `client.Bot.Events.OnInteractionSubmit` /
    `events.NormalizeInteractionSubmitEvent` interaction-submit event.

- Add `client.Bot.MessageSummary` async generic message summary tasks:
  `CreateTask` (`POST /im/messageSummaryTasks`) and `GetTask`
  (`GET /im/messageSummaryTasks?taskId=...`) with the documented `Bearer-`
  auth headers and envelope-C (bizCode) parsing, typed error propagation, and
  local input validation. `UgateToken` is a required user identity for both
  POST and GET (real-service verified: the server does not validate `X-OP-*`
  headers and returns 50013/52002 when `Ugate-Token` is missing). The legacy
  `CorpID` / `AgentID` / `AuthUser` fields are optional and deprecated: they are
  only emitted as `X-OP-*` headers when explicitly provided and are not sent by
  default. No new dependencies.

- Refactored the no-installed-user SDK surface into real `bot/**`, `user/**`,
  and `userauthorization` packages. Removed prior root capability type paths
  and SDK-owned `personal` terminology without compatibility aliases. Service
  routes, payload keys, authentication, envelopes, and runtime behavior are
  unchanged.

- Bootstrap the independent Go module and repository documentation.
- Add an app-token-aware `Client` with concurrency-safe token caching.
- Add text sends to users and groups through `client.Bot.IM.Message`.
- Add robot IM message variants, reads, recall/pin/emoji/voice management,
  group interaction sends, and raw streaming-card wrappers through
  `client.Bot.IM.Message`; unsupported single-chat file sends are rejected.
- Add typed errors, defensive raw response accessors, and Go 1.25/1.26
  validation while preserving one-attempt L1 behavior.
- Add the shared `client.Bot.Events` dispatcher with ordered message, group-system,
  trigger, and event-namespace routing plus safe once/off/error handling.
- Add framework-neutral Webhook challenge/decryption/dedupe/dispatch and
  authenticated HTTPS callback registration through `client.Bot.Webhook`.
- Add generated protobuf Frame bindings and reconnecting, bounded-queue
  WebSocket sessions through `client.Bot.WebSocket`, including heartbeat, ACK,
  one-shot automatic callback-mode registration, and generation isolation.
- Add opt-in, closed-schema realtime diagnostics and safe error callbacks with
  credential, endpoint, callback-secret, frame, and payload redaction.
- Add synchronous WebSocket callback-mode registration for ordered operational
  restoration and reject every 3xx for endpoint/callback sensitive POSTs.
- Pin the only approved runtime dependencies: `github.com/coder/websocket`
  v1.8.15 and `google.golang.org/protobuf` v1.36.11.

The T4 implementation and local conformance matrix are complete. Real-service
WebSocket/Webhook smoke has not run, so this local-only build is not remote or
release ready.
