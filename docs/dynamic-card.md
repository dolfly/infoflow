# AI 动态卡

AI 动态卡（AI dynamic message，scheme `"1"`）通过 `client.Bot.IM.DynamicCard` 使用。完整
请求、响应和错误字段见 [API 参考](API_REFERENCE.md)「Bot 动态卡」。

## 发送与更新

- `client.Bot.IM.DynamicCard.Send`：发送单聊 / 群聊动态消息。
- `client.Bot.IM.DynamicCard.Update`：全量或按组件更新已有卡片。
- `SendRequest.At` / `.Reply` 与 `StreamOptions.At` / `.Reply` 支持群聊结构化 @ 与引用回复。
- `StreamOptions.Raw` 复用发送的 raw 逃生口。

发送时 `ToUser` / `GroupID` 必须且只能有一个非空，SDK 据此派生 `message.header.toType` /
`toId`；`SessionID` 原样透传到 `message.header.extra.sessionId`，为空时省略。
`msgType=INTERACTIVE`、`role=robot`、`contents.scheme="1"` 由 SDK 固定。
`SendRequest.Raw` 只补充 SDK 未设置的字段，已知字段优先；`header.toId` 完全由 SDK 派生，
raw 不能注入另一种目标类型。

本地会校验目标和内容（含 63,000 UTF-8 字节限制），失败返回 `validation` 类
`DynamicCardError`，不会发出请求。发送不会自动重试，包括 `6001` / `9001`。
`CardRef.Secret` 是敏感值，不要记录到日志。

## 流式会话

`client.Bot.IM.DynamicCard.NewStream` 建立 create → enable → append → finish 的流式会话：

- 每次 append 使用完整快照。
- 同一张卡上的操作串行执行。
- `sequence` 只用响应值覆盖，不自行推导。

`UpdateResult.Sequence` 可以作为后续流式会话的 `enable.sequence`；`AckResult.Sequence`
保持可空（只有应用了 `card_update` 时才存在）。

## 事件与回执

动态卡交互事件经现有 Webhook 链路接收，用 `client.Bot.Events.OnCardEvent` 注册处理器；
再用 `client.Bot.IM.DynamicCard.Ack` / `.AckEvent` 按 `sync` / `async` 语义回执：

- `sync` 窗口 4 秒，`async` 窗口 10 分钟。
- 可选 toast 与组件级 `card_update`。
- 赞踩事件以 `footer_operation` 回传 `element_id` / `action.value` / 原始 `client_context`。

## @ 与引用回复（v37 语义）

- @ 默认写顶层 `message.body` 的
  `{"type":"AT","atall":...,"atusers":[...]}`，与 `contents` 平级。
- 正文需含 `@uuapId` 才会显示和通知；`AutoMentionText` 可自动补齐。
- `AtAgentIDs` 已废弃且不再产生 wire 字段。
- `Transport` 默认 `body`；`header` 是 legacy opt-in。
- 引用回复写 `message.header.extra.reply`，只在首次 send 生效。

## 卡片模型

- 内容与组件模型、`DecodeResponse` 响应判定、`DynamicCardError` 错误模型。
- `ValidateContent` 本地预检（63,000 UTF-8 字节、JSON 深度 14、告警不阻断）。
- 19 位 `ID` 类型：内部按 `int64` 存储，JSON 始终输出字符串。
- `action_bar` 操作栏（`ActionBarComponent` / `ActionBarItem`）与顶层
  `contents.footer` 区域。

## Markdown 渲染助手

`dynamiccard.Markdown` 提供纯字符串渲染助手，覆盖：

- @ 提及
- 颜色
- 代码块
- 表格
- 引用
- 按钮组
- `agent-flow-stream`

## 限制

- 单聊 `file` 消息发送不在 SDK 范围内，本地拒绝。
- `action_bar` 的渲染与赞踩状态由客户端固定，SDK 只做声明与事件解析。
