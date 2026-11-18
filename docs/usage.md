# 使用指南

## 常用能力

| 能力 | 方法全路径 | 文档 |
|-|-|-|
| 发文本（用户 / 群） | `client.Bot.IM.Message.SendToUser` / `.SendToGroup` | [API 参考](API_REFERENCE.md)「SendToUser」 |
| 发结构化消息（图片、Markdown、富文本、图文） | `client.Bot.IM.Message.SendUserMessage` / `.SendGroupMessage` | [API 参考](API_REFERENCE.md)「SendUserMessage」 |
| MD 群消息 @ 匹配注入（用户 / 机器人 / @全体） | `client.Bot.IM.Message.SendGroupMarkdownWithMentions` | [API 参考](API_REFERENCE.md)「SendGroupMarkdownWithMentions」 |
| 读群消息、单条消息 | `client.Bot.IM.Message.ListGroupMessages` / `.GetMessage` | [API 参考](API_REFERENCE.md)「ListGroupMessages」 |
| 消息管理（撤回、置顶、表情、语音转写） | `client.Bot.IM.Message.RevokeGroupMessage` / `.SetGroupMessagePin` / `.TranscribeVoiceMessage` | [API 参考](API_REFERENCE.md)「RevokeUserMessage、RevokeGroupMessage、SetGroupMessagePin」 |
| 文件上传与下载 | `client.Bot.IM.File.Upload` / `.GetURL` / `.Download` | [API 参考](API_REFERENCE.md)「Bot IM：文件（File API）」 |
| 流式卡片、气泡工作卡片 | `client.Bot.IM.Message.NewStreamingCardSession` / `.RenderBubbleWorkflowCard` | [API 参考](API_REFERENCE.md)「StreamingCardSession（推荐生命周期封装）」 |
| 机器人日历与会议 | `client.Bot.Calendar.CreateEvent` / `client.Bot.Meeting.CallMeeting` | [API 参考](API_REFERENCE.md)「Bot Calendar / Meeting」 |
| 发 AI 动态卡（单聊 / 群聊） | `client.Bot.IM.DynamicCard.Send` | [AI 动态卡](dynamic-card.md) |
| 更新 AI 动态卡（全量 / 组件级） | `client.Bot.IM.DynamicCard.Update` | [AI 动态卡](dynamic-card.md) |
| 流式动态卡会话（enable / append / finish） | `client.Bot.IM.DynamicCard.NewStream` | [AI 动态卡](dynamic-card.md) |
| Markdown 片段渲染（@ / 颜色 / 代码块 / 表格 / 引用 / 按钮组 / Agent 流） | `dynamiccard.Markdown` | [AI 动态卡](dynamic-card.md) |
| 接收 AI 动态卡交互事件 | `client.Bot.Webhook.NewReceiver` + `client.Bot.Events.OnCardEvent` | [AI 动态卡](dynamic-card.md) |
| 回执 AI 动态卡交互事件（sync / async） | `client.Bot.IM.DynamicCard.Ack` / `.AckEvent` | [AI 动态卡](dynamic-card.md) |
| 事件分发、实时接收 | `client.Bot.Events.On` / `client.Bot.Webhook.NewReceiver` / `client.Bot.WebSocket.NewSession` | [API 参考](API_REFERENCE.md)「Bot Events：事件分发」 |

用户身份能力（`client.User.*`、`client.UserAuthorization`）与机器人消息总结
（`client.Bot.MessageSummary`）见 [API 参考](API_REFERENCE.md)「User API」「Bot 消息总结」。

## 事件接收

- **Webhook**：`client.Bot.Webhook.NewReceiver` 创建 receiver，把框架解析前的原始请求体
  交给 `receiver.Handle`。适合已有 HTTP 服务的场景。
- **WebSocket**：`client.Bot.WebSocket.NewSession` 只组装会话，
  `session.Connect(ctx)` 才真正连接并自动注册回调。适合没有公网回调地址的场景。
- **事件分发**：两种模式共用 `client.Bot.Events.On` 注册的 handler；支持具体事件名
  （如 `private.text`、`group.text`）和通配符 `*`。

日志只打印 `event.Type`、`event.MessageType` 这类分类字段；正文
（`event.Payload()` / `event.RawPayload()`）、凭据与回调密钥都不要落日志。

## 不支持或不在 SDK 范围内

- 单聊 `file` 消息发送：本地拒绝。
- 动态卡 `action_bar` 的渲染与赞踩状态由客户端固定，SDK 只做声明与事件解析；老客户端的
  兼容话术不在 SDK 处理范围。
- 实时侧不提供 HTTP 框架适配器、通用任意路由 client、内置 logger，也不自动恢复回调模式。
