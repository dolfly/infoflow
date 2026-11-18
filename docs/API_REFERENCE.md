# Go SDK API 参考

本文以当前源码中的 exported API、请求类型和请求构造逻辑为准。示例均可作为调用骨架；`AppKey`、`AppSecret`、用户令牌和业务 ID 必须替换为调用方自己的值。除特别说明外，所有方法都接受 `context.Context`，`ctx` 不能为 `nil`。

## 公共约定

### 创建 Client 与认证

`NewClient` 只做配置校验和服务组装，不发起网络请求。

```go
client, err := infoflow.NewClient(infoflow.Config{
	AppKey: "your-app-key", AppSecret: "your-app-secret",
	User: user.Config{Robot2CToken: "robot2c-token", UGateToken: "ugate-token"},
})
if err != nil { log.Fatal(err) }
```

`Config` 的必填字段是 `AppKey`、`AppSecret`。`BaseURL` 默认是 `https://api.im.baidu.com/api/v1`；连接超时默认为 10 秒，请求超时默认为 30 秒，负数非法。`HTTPClient`、`Realtime.Observer`、`Realtime.Diagnostics` 和 `Realtime.OnError` 可选。`User` 中的 `Robot2CBaseURL`、`CalendarBaseURL`、`MeetingBaseURL` 为空时使用各包默认地址。

认证请求是 `POST /auth/app_access_token`，请求体为 `{"app_key":"...","app_secret":"..."}`。SDK 自动缓存并刷新 token，业务请求使用 `Authorization: Bearer ...`。不要记录凭据、token、回调密钥、一次性 WebSocket URL 或消息原文。

### 错误

可用 `errors.As` 判断 `*infoflow.ConfigError`（参数/配置）、`*infoflow.AuthError`（认证或 token 业务失败）、`*infoflow.HTTPError`（非 2xx）、`*infoflow.BusinessError`（平台业务码非成功）和 `*infoflow.MalformedResponseError`（响应结构无法解析）。`BusinessError` 可读取业务码、消息和 `RawJSON()`；其余类型按源码提供的 `Body()`、`RawJSON()` 或 `RawResponse()` 读取诊断信息。

成功响应通常是平台 envelope，例如 `{"code":"ok","data":{"msgkey":123}}` 或 `{"errcode":0,"errmsg":"ok"}`。业务失败不会只看 HTTP 状态，而会检查 envelope、`code`、`errcode`、`data.code` 或 `bizCode`。

## Bot IM：消息

入口为 `client.Bot.IM.Message`。`SendToUser` 和 `SendToGroup` 是最小文本 API；需要图片、Markdown、富文本、图文、回复或群消息元素时使用结构化方法。

### SendToUser

- 用途：向一个用户发送文本。
- 签名：`SendToUser(ctx context.Context, userID, text string) (SendToUserResult, error)`。
- 参数：`userID`、`text` 必填且不能是空白字符串。
- 请求：`POST /app/message/send`；请求 JSON：`{"touser":"u001","msgtype":"text","text":{"content":"你好"}}`。
- 返回：`SendToUserResult{MessageKey int64, InvalidUser string}`；典型响应：`{"code":"ok","data":{"msgkey":12345}}`。
- 错误：空参数为 `ConfigError`；平台返回非成功 envelope 为 `AuthError`、`BusinessError` 或 `MalformedResponseError`。

```go
r, err := client.Bot.IM.Message.SendToUser(ctx, "u001", "你好")
fmt.Println(r.MessageKey, err)
```

### SendToGroup

- 用途：向群发送文本机器人消息。
- 签名：`SendToGroup(ctx context.Context, groupID int64, text string) (SendToGroupResult, error)`。
- 参数：`groupID` 必须为正数，`text` 必填；客户端自动生成正数 `clientmsgid`。
- 请求：`POST /robot/msg/groupmsgsend`；请求 JSON：`{"message":{"header":{"toid":1001,"totype":"GROUP","msgtype":"TEXT","clientmsgid":1700000000000,"role":"robot"},"body":[{"type":"TEXT","content":"你好"}]}}`。
- 返回：`SendToGroupResult{MessageID, MessageSequenceID, CreatedAtMillis}`；典型 `data` 为 `{"messageid":123,"msgseqid":456,"ctime":1700000000000}`。
- 错误：ID 或文本非法为 `ConfigError`；缺字段或业务失败为 `MalformedResponseError`/`BusinessError`。

```go
r, err := client.Bot.IM.Message.SendToGroup(ctx, 1001, "群公告")
fmt.Println(r.MessageID, err)
```

### SendUserMessage

- 用途：发送用户文本、Markdown、图片、富文本或图文消息，并可携带回复信息。
- 签名：`SendUserMessage(ctx context.Context, request im.SendUserMessageRequest) (im.SendToUserResult, error)`。
- 参数：`ToUser`、`Type` 必填；`Type` 支持 `text`、`md`、`image`、`richtext`、`news`。对应必填字段分别为 `Text`、`Text`、`Image`、非空 `RichText`、非空 `Articles`；文章的 `Title` 和 `URL` 必填。`Reply` 可选。
- 请求：`POST /app/message/send`。例如文本 JSON 为 `{"touser":"u001","msgtype":"md","md":{"content":"**hi**"}}`；图片为 `{"touser":"u001","msgtype":"image","image":{"content":"image-key"}}`。
- 返回：`SendToUserResult`，典型 `{"code":"ok","data":{"msgkey":123}}`。
- 错误：不支持的类型、空内容或非法富文本节点为 `ConfigError`；平台业务失败见公共错误约定。

```go
r, err := client.Bot.IM.Message.SendUserMessage(ctx, im.SendUserMessageRequest{
	ToUser: "u001", Type: "text", Text: "可选类型消息",
})
```

### SendGroupMessage

- 用途：发送群文本、图片、Markdown、链接、@或快捷指令，并可回复消息。
- 签名：`SendGroupMessage(ctx context.Context, request im.SendGroupMessageRequest) (im.SendToGroupResult, error)`。
- 参数：`GroupID`、`Type`、非空 `Body` 必填；`Type` 为 `TEXT`、`IMAGE` 或 `MD`。`GroupBody.Type` 支持 `TEXT`、`IMAGE`、`MD`、`LINK`、`AT`、`CMD`，其内容字段按类型必填；`ClientMessageID` 可选，0 时自动生成；`Reply` 可选。
- 请求：`POST /robot/msg/groupmsgsend`；例如 `{"message":{"header":{"toid":1001,"totype":"GROUP","msgtype":"TEXT","clientmsgid":1,"role":"robot"},"body":[{"type":"TEXT","content":"hi"}]}}`。字段名由 `GroupBody` 的 JSON tag 拼接，`AtUserIDs`、`AtAgentIDs`、`TargetAgentID` 等不会被丢弃。
- 返回：`SendToGroupResult`，典型响应同 `SendToGroup`。
- 错误：文本最多 2000 字符，Markdown 最多 2048 字符；非法元素、ID 或类型为 `ConfigError`。

```go
r, err := client.Bot.IM.Message.SendGroupMessage(ctx, im.SendGroupMessageRequest{
	GroupID: 1001, Type: "TEXT", Body: []im.GroupBody{{Type: "TEXT", Content: "hi"}},
})
```

### SendGroupMarkdownWithMentions

- 用途：向群发送 Markdown 消息并对缺失的 @ 目标做匹配注入（@用户、@机器人、@全体），可携带回复。纯函数 `BuildGroupMarkdownMentionBody(markdown string, m im.GroupMarkdownMention) (string, []im.GroupBody)` 返回匹配注入后的 content 与 body（AT 块在前、MD 块在后），也可直接用于构造 `SendGroupMessageRequest`。
- 签名：`SendGroupMarkdownWithMentions(ctx context.Context, request im.SendGroupMarkdownMentionRequest) (im.SendToGroupResult, error)`。
- 参数：`GroupID`、非空 `Markdown` 必填；`Mentions.AtAll` 为 true 时 @ 全体并短路忽略用户/机器人名单；否则 `AtUserIDs`（≤50）与 `AtAgentIDs`（≤10）中字面量 `@<id>` 未出现在 Markdown 的目标按序补到内容末尾（已出现的保留原位，名单不去重、保序）。`SelfAgentID` 命中 `AtAgentIDs` 时拒绝（平台禁止 @ 自身）。`Reply`、`ClientMessageID` 可选，语义同 `SendGroupMessage`。
- 请求：`POST /robot/msg/groupmsgsend`，`msgtype` 为 `MD`；body 为 AT 块（如需）在前、MD 块在后。例如 `{"message":{"header":{"toid":1001,"totype":"GROUP","msgtype":"MD","clientmsgid":1,"role":"robot"},"body":[{"type":"AT","atuserids":["zhangsan"]},{"type":"MD","content":"hi @zhangsan"}]}}`。
- 返回：`SendToGroupResult`，典型响应同 `SendToGroup`。
- 错误：Markdown 为空、名单超限或 `SelfAgentID` 命中自身为 `ConfigError`；底层 `SendGroupMessage` 的校验（含 MD 最多 2048 字）不通过同样为 `ConfigError`。

```go
r, err := client.Bot.IM.Message.SendGroupMarkdownWithMentions(ctx, im.SendGroupMarkdownMentionRequest{
	GroupID:  1001,
	Markdown: "大家好",
	Mentions: im.GroupMarkdownMention{AtUserIDs: []string{"zhangsan", "lisi"}, AtAgentIDs: []int64{2712}},
})
```

### SendInteractionMessage

- 用途：向群发送交互消息，支持文本、图片、Markdown、链接、@和原始 JSON panel。
- 签名：`SendInteractionMessage(ctx context.Context, r im.InteractionMessageRequest) error`。
- 参数：`Header.ToType` 必须是 `group`，`Header.ToID` 为正数，`Body` 非空；`Panel` 可选但必须是合法 JSON，图片最多 5 个。
- 请求：`POST /robot/group/sendInteractionMsg`；JSON 直接序列化 `InteractionMessageRequest`，例如 `{"header":{"totype":"group","toid":1001},"body":[{"type":"TEXT","content":"选择"}]}`。
- 返回：无业务数据，成功返回 `nil`；平台通常为 `{"errcode":0,"errmsg":"ok"}`。
- 错误：无效 body 为 `ConfigError`，非零 `errcode` 为 `BusinessError`。

```go
err := client.Bot.IM.Message.SendInteractionMessage(ctx, im.InteractionMessageRequest{
	Header: im.InteractionHeader{ToType: "group", ToID: 1001},
	Body: []im.InteractionBody{{Type: "TEXT", Content: "选择"}},
})
```

### ListGroupMessages

- 用途：按群读取历史消息。
- 签名：`ListGroupMessages(ctx context.Context, r im.ListGroupMessagesRequest) (im.ListGroupMessagesResult, error)`。
- 参数：`ReceiverID`、`StartTime`、`EndTime` 必填，时间窗口不能超过 24 小时；`PageSize` 可选，默认 20、最大 50；`SortType` 为 `asc`/`desc`，默认 `desc`；`PageToken` 可选。
- 请求：`GET /im/message/list`；典型 query 为 `?receiverId=1001&startTime=1700000000000&endTime=1700003600000&sortType=desc&pageSize=20`，下一页追加 `pageToken`。
- 返回：`ListGroupMessagesResult{Items []MessageItem, HasMore bool, PageToken string}`，`RawJSON()` 保留原始响应；典型 data 为 `{"items":[{"msgid":"m1"}]}`。
- 错误：非法窗口/分页为 `ConfigError`，响应中的业务 `code` 非成功为 `BusinessError`。

```go
r, err := client.Bot.IM.Message.ListGroupMessages(ctx, im.ListGroupMessagesRequest{ReceiverID: 1001, StartTime: start, EndTime: end, PageSize: 20})
```

### GetMessage

- 用途：按发送者、接收者和消息 ID 获取单条或内层消息。
- 签名：`GetMessage(ctx context.Context, r im.GetMessageRequest) ([]im.MessageItem, error)`。
- 参数：`FromID`、`ReceiverID`、`MessageID` 必填；`ReceiverType` 只能为 2 或 7。四个 `Inner*` 字段要么全部省略，要么全部填写。
- 请求：`GET /im/message/getSingleMsg`；字段拼接为 `fromId`、`receiverId`、`receiverType`、`msgId`，内层字段为 `innerMsgId`、`innerMsgFromId`、`innerMsgReceiverId`、`innerMsgReceiverType`。
- 返回：`[]MessageItem`，典型成功路径为 `data.data.items`；业务成功码为外层 `code=ok` 且内层 `data.code=200`。
- 错误：参数不完整为 `ConfigError`；内层业务码非 200 为 `BusinessError`。

```go
items, err := client.Bot.IM.Message.GetMessage(ctx, im.GetMessageRequest{
	FromID: "u001", ReceiverID: 1001, ReceiverType: 2, MessageID: 123,
})
```

### work_card 归一化与文本行渲染

群消息读取（`ListGroupMessages` / `GetMessage`）返回的 `MessageItem.Content` 保持原始
`json.RawMessage`，但对 `app_msg` / `work_card` 提供一等化访问：

- `MessageItem.WorkCard() (im.WorkCardContent, bool)`：识别
  `{ "type": "work_card", "cardId" }`，返回结构化 `WorkCardContent{Type, CardID}`；
  `cardId` 缺失、为 `null` 或非字符串时 `CardID` 兜底为空串，仍识别为一等
  work_card（不落到 unknown）。
- `MessageItem.WorkCardText() string`：渲染占位文本 `[工作卡片 cardId=<cardId>]`，
  `cardId` 缺失/非法时渲染 `[工作卡片]`；非 work_card 返回空串。
- `MessageItem.WorkCardTextLine() string`：渲染完整文本行
  `[<time>] [msgId:<msgId>] bot#<agentId>: [工作卡片 cardId=<cardId>]`。
  `<time>` 为北京时区（UTC+8）的 `2006-01-02 15:04:05`；机器人发送者
  （`sender` 含 `agentId` 且无 `uuapId`）前缀 `bot#<agentId>`，真人发送者沿用
  `uuapId`；非 work_card 返回空串。

### RevokeUserMessage、RevokeGroupMessage、SetGroupMessagePin

- 用途：撤回用户消息、撤回群消息、置顶/取消置顶群消息。
- 签名：`RevokeUserMessage(ctx context.Context, r im.RevokeUserMessageRequest) error`；`RevokeGroupMessage(ctx context.Context, r im.RevokeGroupMessageRequest) error`；`SetGroupMessagePin(ctx context.Context, r im.PinGroupMessageRequest) error`。
- 参数：用户撤回需 `MessageKey`、正数 `AgentID`；群撤回需正数 `GroupID`、`MessageID`、`MessageSequenceID`；置顶需正数 `GroupID`、`MessageID`，`Operation` 为 1 或 2。
- 请求：分别为 `POST /app/message/revoke` JSON `{"msgkey":"k","agentid":1}`；`POST /robot/group/msgRecall` JSON `{"groupId":1001,"messageid":123,"msgseqid":456}`；`POST /robot/group/stickOnTop` JSON `{"groupId":1001,"opType":1,"msgId":123}`。
- 返回：无数据，成功 `nil`，典型响应 `{"errcode":0,"errmsg":"ok"}`。
- 错误：参数错误为 `ConfigError`；非零 `errcode` 为 `BusinessError`。

```go
err := client.Bot.IM.Message.SetGroupMessagePin(ctx, im.PinGroupMessageRequest{GroupID: 1001, Operation: 1, MessageID: 123})
```

### AddEmoji、DeleteEmoji

- 用途：为消息添加或删除表情。
- 签名：`AddEmoji(ctx context.Context, r im.EmojiRequest) error`；`DeleteEmoji(ctx context.Context, r im.EmojiRequest) error`。
- 参数：`FromUID`、`BaseMessageID` 必填，`ChatType` 为 2 或 7；添加表情还要求 `MessageID2` 和 `ReplyContent`，`ChatID` 等可选字段按服务要求填写。
- 请求：`POST /im/message/emoji/add` 或 `POST /im/message/emoji/del`，body 直接序列化 `EmojiRequest`，例如 `{"fromUid":"u001","chatType":2,"baseMsgId":"m1","msgId2":"m2","replyContent":"赞"}`。
- 返回：成功 `nil`；业务成功为外层 `code=ok` 且 `data.bizCode=200`。
- 错误：缺字段为 `ConfigError`，`bizCode` 非 200 为 `BusinessError`。

```go
err := client.Bot.IM.Message.AddEmoji(ctx, im.EmojiRequest{FromUID: "u001", ChatType: 2, BaseMessageID: "m1", MessageID2: "m2", ReplyContent: "赞"})
```

### TranscribeVoiceMessage

- 用途：查询语音转写结果。
- 签名：`TranscribeVoiceMessage(ctx context.Context, md5 string) (im.VoiceTranscriptionResult, error)`。
- 参数：`md5` 必填；以 form 字段 `md5` 发送。
- 请求：`POST /va/queryASRResult`，`application/x-www-form-urlencoded`：`md5=<value>`。
- 返回：`VoiceTranscriptionResult{Code, Content}`；典型响应 `{"code":"ok","data":{"code":200,"content":"转写文本"}}`。
- 错误：空 md5 为 `ConfigError`；`data.code` 非 200 为 `BusinessError`。

```go
r, err := client.Bot.IM.Message.TranscribeVoiceMessage(ctx, "voice-md5")
fmt.Println(r.Content, err)
```

### Streaming card 方法

`CreateStreamingCard(ctx, p json.RawMessage) (json.RawMessage, error)`、`UpdateUserStreamingCard(ctx, p json.RawMessage) (json.RawMessage, error)`、`UpdateGroupStreamingCard(ctx, p json.RawMessage) (json.RawMessage, error)` 分别调用 `POST /msg/sender/interactivity_msg`、`POST /msg/modifier/interactivity_personal_msg_content`、`POST /msg/modifier/dynamic_content`。`p` 必须是合法 JSON；字段由平台交互卡协议决定，SDK 原样转发并原样返回响应。典型响应为 `{"code":"ok","data":{...}}`；非法 JSON 为 `ConfigError`，响应业务失败为 `BusinessError`。

```go
raw, err := client.Bot.IM.Message.CreateStreamingCard(ctx, json.RawMessage(`{"content":"开始"}`))
```

### StreamingCardSession（推荐生命周期封装）

入口为 `client.Bot.IM.Message.NewStreamingCardSession`。它封装上面三个 raw 方法，
提供创建 → 追加 → 完成/失败的完整生命周期，并内置独立的失败路径（CARD-005 语义）：
调用 `Fail` 会把错误状态推到卡片（`status_info` 为错误信息、`think_status_color` 为
`#D03050`、`think_status_text` 为「生成失败」、`dc_print_end=1`），调用方可以回退到
普通消息。需要手工管理 `modifyToken` / 群聊 `version` 时仍可使用上面的 raw 方法。

- 签名：`NewStreamingCardSession(options im.StreamingCardSessionOptions) (*im.StreamingCardSession, error)`，不联网。
- `im.StreamingCardTarget`：`{Type im.StreamingCardTargetType, ID string}`；`Type` 为
  `im.StreamingCardTargetUser`（私聊，ID 为用户登录名）或 `im.StreamingCardTargetGroup`
  （群聊，ID 为群 ID）。
- 选项：`UserIDs []string`（私聊更新目标，默认 `[Target.ID]`）；`NotifyList []map[string]any`
  （群通知列表，默认 `[{"to_type":2,"to_ids":[Target.ID]}]`）；`AnswerFormat`
  （`markdown` 默认 / `text`，分别写 `ai_markdown` / `ai_text` 字段）；`InitialText string`
  （初始回答文本）。

生命周期方法（追加/完成/失败都要求先调用 `Start`；`Complete`/`Fail` 之后会话关闭）：

| 方法 | 签名 | 说明 |
|-|-|-|
| `Start` | `Start(ctx context.Context) error` | 发送初始占位卡 `POST /msg/sender/interactivity_msg` 并解析 `modify_token`（私聊 `data.modify_token`、群聊 `data.receivers[0].modify_token`） |
| `AppendText` | `AppendText(ctx context.Context, delta string) error` | 追加主回答文本并立即 flush |
| `AppendReasoning` | `AppendReasoning(ctx context.Context, delta string) error` | 追加推理/思考文本并立即 flush |
| `NotifyToolStart` | `NotifyToolStart(ctx context.Context, name, phase string) error` | 在思考区写入工具调用标记 |
| `NotifyToolResult` | `NotifyToolResult(ctx context.Context, text string) error` | 在思考区写入工具结果 |
| `Complete` | `Complete(ctx context.Context, doneLabel string) error` | 完成态 flush（默认文案「思考完成」）并关闭会话 |
| `Fail` | `Fail(ctx context.Context, message string) error` | 失败态 flush（独立失败路径）并关闭会话 |
| `State` | `State() im.StreamingCardSessionState` | 只读快照（target/modifyToken/version/done/failed/answerText/reasoningText 等） |

flush 请求对齐 Node/Python：回答区写 `ai_markdown`（或 `ai_text`）、思考区写
`thinking_aio`、状态区写 `status_info` / `think_status_text`，`dc_print_end` 为 `0`
（流式）或 `1`（完成/失败）。回答文本最多 6000 字符、思考文本最多 10000 字符，超长
从尾部截断。私聊更新走 `POST /msg/modifier/interactivity_personal_msg_content`；群聊
更新走 `POST /msg/modifier/dynamic_content` 且每次递增 `version`（初始 111）。
非法 target / answer format、未 Start 就追加、Start 两次、或会话已关闭后调用为
`ConfigError`；平台非成功为 `BusinessError`。

```go
session, err := client.Bot.IM.Message.NewStreamingCardSession(im.StreamingCardSessionOptions{
	Target: im.StreamingCardTarget{Type: im.StreamingCardTargetUser, ID: "zhangsan"},
})
if err != nil { log.Fatal(err) }
if err := session.Start(ctx); err != nil { log.Fatal(err) }
session.AppendText(ctx, "Hello ")
session.AppendText(ctx, "World!")
if err := session.Complete(ctx, "分析完成"); err != nil { log.Fatal(err) }
```

### RenderBubbleWorkflowCard（气泡工作流卡）

入口为 `client.Bot.IM.Message.RenderBubbleWorkflowCard`。通过 xplatform 中转服务发送
「外层气泡卡 + 内嵌工作卡」到 `POST /robot/card/bubble-workflow-card/render`，响应为
B-bubble envelope（`data.errcode`/`data.errmsg` + `data.data`）。与流式交互卡协议
互不兼容。

- 签名：`RenderBubbleWorkflowCard(ctx context.Context, options im.BubbleWorkflowCardRenderOptions) (im.BubbleWorkflowCardRenderResult, error)`。
- 布局约束（SDK 本地校验）：`card.components` 最多 8 个、`card.buttons` 最多 6 个、
  `componentId` 必须唯一；组件类型为 `text` / `input` / `single-select` / `multi-select` /
  `dropdown-single` / `dropdown-multi`；`text` 组件必填 `text`；选择类组件必填非空
  `options`。
- 请求：`card_id`、`card_type=general_form`、`caller.caller_name`（提交事件回显）、
  `receiver{type: group|user, id}`、`card{title?, components?, buttons?}`；可选
  `card_instance_id`、`question`、`refresh_default_intermediate_state`。
- 返回：`BubbleWorkflowCardRenderResult{OK, Created, Caller, Receiver,
  CardInfo{CardID,CardType,Revision}, Recognized, Updated}`；结果未确认
  `ok/recognized/updated` 或缺少 `created` 时为 `MalformedResponseError`，
  `data.errcode` 非 0 为 `BusinessError`。

```go
result, err := client.Bot.IM.Message.RenderBubbleWorkflowCard(ctx, im.BubbleWorkflowCardRenderOptions{
	CardID: "approval-42",
	Caller: im.BubbleWorkflowCardCaller{CallerName: "workflow-bot", RequestID: "req-1"},
	Receiver: im.BubbleWorkflowCardReceiver{Type: im.BubbleWorkflowCardReceiverGroup, ID: "12345"},
	Card: im.BubbleWorkflowCardDefinition{
		Title: "请确认审批",
		Components: []im.BubbleWorkflowCardComponent{{ComponentID: "reason", Type: im.BubbleWorkflowCardComponentInput, PlaceholderText: "请输入审批意见"}},
		Buttons: []im.BubbleWorkflowCardButton{{EventKey: "approve", Text: "同意"}},
	},
})
```

对应的用户提交事件见 `client.Bot.Events.OnInteractionSubmit`。

## Bot IM：文件（File API）

入口为 `client.Bot.IM.File`，负责把内存中的二进制文件上传到 BOS、为已上传的 object
key 获取/刷新临时 URL、把聊天文件消息中的 `fid` / `fileId` 换成临时下载 URL，并用当前
app access token 拉取文件字节。上传与 getUrl 固定走 BOS 网关
`https://infoflow-open-gateway.baidu.com`；聊天文件下载 URL 走开放平台
`/open-file-service/...`。

| 方法 | 签名 | 请求路径 |
|-|-|-|
| `Upload` | `Upload(ctx context.Context, options im.UploadFileOptions) (im.UploadFileResult, error)` | `POST https://infoflow-open-gateway.baidu.com/im/bos/upload`（multipart） |
| `GetURL` | `GetURL(ctx context.Context, options im.BosFileURLOptions) (im.BosFileURLResult, error)` | `GET https://infoflow-open-gateway.baidu.com/im/bos/getUrl` |
| `GetDownloadURLPrivate` | `GetDownloadURLPrivate(ctx context.Context, options im.FileDownloadURLOptions) (im.FileDownloadURLResult, error)` | `POST /open-file-service/file/get/download/url/robot-chat/byFid` |
| `GetDownloadURLGroup` | `GetDownloadURLGroup(ctx context.Context, options im.FileDownloadURLOptions) (im.FileDownloadURLResult, error)` | `POST /open-file-service/file/get/download/url/byFid` |
| `Download` | `Download(ctx context.Context, options im.DownloadFileOptions) (im.DownloadFileResult, error)` | 先解析下载 URL，再以 `x-openapi-gateway-identity: Bearer-<token>` 拉取字节 |

- `Upload`：`File []byte` 必填；`FileName` 必填且不能含回车换行，且不得含 `/`；
  `ObjectKey` 可选，提供时原样写入 multipart 字段（不添加额外前缀）；未提供时由 SDK
  生成 `sdk-uploads/{yyyyMMdd}/{uuid}{ext}`（`ext` 仅在 `FileName` 以
  `\.[A-Za-z0-9]{1,8}$` 结尾时安全提取，否则省略），并**始终**写入 multipart 的
  `objectKey` 字段；提供的 key 必须满足 `^[A-Za-z0-9._/-]+$`，不能以 `/` 开头、
  不能含 `//` 或 `..`，长度 ≤ 1024；`ExpirationSeconds` 为 1–86400，默认 3600。
  上传成功后 SDK 自动调用 `GetURL` 拿短期 presigned URL 一起返回。
  **上传后取 URL 失败不会重新上传**：返回 `*im.UploadURLRecoveryError`，其 `ObjectKey`
  字段携带已上传的 object key，调用方可据此恢复。上传是写请求，传输层不自动重试。
- `GetURL`：`ObjectKey` 必填；`ExpirationSeconds` 同上；可选
  `ResponseContentDisposition`（如 `attachment; filename*=UTF-8''<urlencoded>`）。
  返回 `URL` 与生效的 `ExpirationSeconds`。presigned URL 是短期 bearer-like 值，
  **不要记录或长期保存**；涉及 URL 的错误诊断会脱敏（`MalformedResponseError.RawResponse()` 不含 url）。
- `GetDownloadURLPrivate`：只需 `FID`；请求体 `{"fid":...,"chatType":1}`，单聊文件。
- `GetDownloadURLGroup`：群文件必填 `FID`、`ChatID`、`FileMsgID`；可选 `ExpSeconds`
  （正整数）。请求体 `{"fid":...,"chatType":2,"chatId":...,"fileMsgId":...,"expSeconds"?}`。
- `Download`：`ChatType` 为 `im.FileChatPrivate`（1）或 `im.FileChatGroup`（2），群文件
  还需 `ChatID`、`FileMsgID`；可选 `ExpSeconds`、`MaxBytes`（>0 时超出报错）。
  返回 `Content []byte`、`ContentType`、`FileName`、`FileSize`。
- 下载 URL 响应使用 B-file-url envelope：顶层 `code` 为 `ok`/缺省，`data.code` 为
  `0`/`200`/缺省，`data.data`（或 `data`）提供 `url`/`fileName`/`fileSize`。平台业务
  失败为 `BusinessError`。

```go
uploaded, err := client.Bot.IM.File.Upload(ctx, im.UploadFileOptions{
	File: fileBytes, FileName: "hello.txt", ObjectKey: "safe-disposable/hello.txt", ExpirationSeconds: 900,
})
// uploaded.ObjectKey / uploaded.URL（短期，勿落盘）

downloaded, err := client.Bot.IM.File.Download(ctx, im.DownloadFileOptions{
	FID: "FA62EE1945347CFD7BE37F44B40045B4", ChatType: im.FileChatPrivate, MaxBytes: 20*1024*1024,
})
// downloaded.Content / FileName / FileSize
```

## Bot IM：群生命周期

入口为 `client.Bot.IM.Group`。请求类型字段如下：`CreateGroupRequest.MemberList` 是 `[]string`；`MembersRequest.MemberList` 与 `MembersRequest.AgentIDList` 是平台约定的逗号分隔字符串；`GroupID` 为群 ID；`GroupOwner` 为群主；`Name`、`Bulletin`、`SidebarLink` 按名称填写；`SetManagersRequest` 同时携带用户和机器人管理员数组；`ModifyUserRemarkRequest` 携带群 ID、用户标识和备注。

| 方法 | 签名 | 请求路径与 JSON | 返回/必填校验 |
|-|-|-|-|
| `Create` | `Create(ctx context.Context, r im.CreateGroupRequest) (im.CreateGroupResult, error)` | `POST /robot/group/create`；body 直接序列化请求，例如 `{"groupName":"测试群","groupOwner":"owner@example.com","memberList":["u001"]}` | 返回 `GroupID`、`FailMembers`、`FailRobotIDs`；`GroupName`、`GroupOwner` 必填，平台字段按请求类型拼接。 |
| `Disband` | `Disband(ctx context.Context, r im.DisbandGroupRequest) (im.RawResult, error)` | `POST /robot/group/disband`；`{"groupId":1001,"groupOwner":"owner@example.com"}` | `GroupID`、`GroupOwner` 必填；成功返回 `RawResult`。 |
| `Quit` | `Quit(ctx context.Context, r im.GroupIDRequest) (im.RawResult, error)` | `POST /im/group/quit`；`{"groupId":1001}` | `GroupID` 必须为正数。 |
| `GetInfo` | `GetInfo(ctx context.Context, r im.GroupIDRequest) (im.RawResult, error)` | `POST /robot/group/getGroupInfo`；`{"groupId":1001}` | 返回原始 envelope；`GroupID` 必填。 |
| `Members` | `Members(ctx context.Context, r im.MemberListRequest) (im.RawResult, error)` | `POST /robot/group/memberList`；包含 `groupId`，可选 `recallType` | `GroupID` 必填；返回成员原始 JSON。 |
| `AddMembers` | `AddMembers(ctx context.Context, r im.MembersRequest) (im.RawResult, error)` | `POST /robot/group/addMembers`；包含 `groupId`、`memberList`、`agentIdList` | `GroupID` 和至少一种成员列表必填。 |
| `RemoveMembers` | `RemoveMembers(ctx context.Context, r im.MembersRequest) (im.RawResult, error)` | `POST /robot/group/removeMembers`；字段同 `AddMembers` | 只校验 `GroupID`；是否至少提供成员列表由平台处理。 |
| `UpdateName` | `UpdateName(ctx context.Context, r im.UpdateGroupNameRequest) (im.RawResult, error)` | `POST /robot/group/updateName`；`{"groupId":1001,"name":"新名称"}` | `GroupID`、`Name` 必填。 |
| `UpdateBulletin` | `UpdateBulletin(ctx context.Context, r im.UpdateBulletinRequest) (im.RawResult, error)` | `POST /robot/group/updateBulletin`；包含 `groupId`、`bulletin` | SDK 只校验 `GroupID`；`Bulletin` 空值也会发送，由平台解释。 |
| `SetSidebar` | `SetSidebar(ctx context.Context, r im.SidebarRequest) (im.RawResult, error)` | `PUT /im/group/sidebar`；包含 `groupId`、`sidebarLink`；`autoOpen`、`customSidebar` 为可选（nil = 不修改） | `GroupID` 必填。 |
| `AddChatTab` | `AddChatTab(ctx context.Context, r im.AddChatTabRequest) (im.RawResult, error)` | `POST /robot/group/addChatTab`；包含 `chatId`、`tabName`、`tabLink` | `ChatID`、`TabName`、`TabLink` 必填。 |
| `UpdateMenuVisible` | `UpdateMenuVisible(ctx context.Context, r im.UpdateMenuVisibilityRequest) (im.RawResult, error)` | `POST /robot/group/updateMenuVisible`；包含 `groupId` 和 `customMenus:{"operationType":"show|hide","mainMenuKeys":[],"subMenuKeys":[]}` | `GroupID` 必填，`OperationType` 必须为 `show` 或 `hide`。 |
| `SupportAuth` | `SupportAuth(ctx context.Context, r im.GroupIDRequest) (im.RawResult, error)` | `POST /robot/group/supportAuth`；`{"groupId":1001}` | `GroupID` 必填。 |
| `CancelAuth` | `CancelAuth(ctx context.Context, r im.GroupIDRequest) (im.RawResult, error)` | `POST /robot/group/cancelAuth`；`{"groupId":1001}` | `GroupID` 必填。 |
| `SetManagers` | `SetManagers(ctx context.Context, r im.SetManagersRequest) (im.RawResult, error)` | `POST /im/group/managers/set`；包含 `groupId`、`userEmails`、`robotAgentIds`，两个数组都会发送 | `GroupID` 必填，管理员总数为 1 到 19。 |
| `ModifyUserRemark` | `ModifyUserRemark(ctx context.Context, r im.ModifyUserRemarkRequest) (im.RawResult, error)` | `POST /im/group/modifyUserRemark`；包含 `toUserId`、`groupId`、`remark`，邮箱会先拼接为 UUAP 前缀 | 群、用户标识和备注必填。 |

群变更都会校验 `context`、正数 ID 和源码声明的必填字段；其中 `RemoveMembers` 不在 SDK 侧强制成员列表，`UpdateBulletin` 不在 SDK 侧强制公告非空。平台非成功 envelope 为 `BusinessError`，无法解析为 `MalformedResponseError`。`RawResult.RawJSON()` 返回防御性拷贝。最小示例：

```go
result, err := client.Bot.IM.Group.GetInfo(ctx, im.GroupIDRequest{GroupID: 1001})
if err == nil { fmt.Println(string(result.RawJSON())) }
```

## Bot 消息总结（通用异步任务）

入口为 `client.Bot.MessageSummary`。通用消息总结以异步任务模型工作：`CreateTask`
提交任务并返回 `taskId`（受理成功 `status=PENDING`），用返回的 `taskId` 轮询 `GetTask`
直到终态（`SUCCESS` / `PARTIAL_SUCCESS` / `FAILED`）。

| 方法 | 签名 | 请求路径 | 说明 |
|-|-|-|-|
| `CreateTask` | `CreateTask(ctx context.Context, r im.CreateMessageSummaryTaskRequest) (im.CreateMessageSummaryTaskResult, error)` | `POST /im/messageSummaryTasks` | 提交异步总结任务 |
| `GetTask` | `GetTask(ctx context.Context, r im.GetMessageSummaryTaskRequest) (im.GetMessageSummaryTaskResult, error)` | `GET /im/messageSummaryTasks?taskId=...` | 按 taskId 轮询任务 |

`CreateMessageSummaryTaskRequest` 字段：

| 字段 | 含义 | 必填/约束 |
|-|-|-|
| `UgateToken` | `Ugate-Token` | **必填**用户身份载体（POST 与 GET 均需，真实服务实测缺失返回 50013/52002）；禁止落日志 |
| `CorpID` | `X-OP-CORP-ID` | 可选/Deprecated；仅显式提供时发送，服务端不校验，取值 1 或 >= 2100000000 |
| `AgentID` | `X-OP-AGENT-ID` | 可选/Deprecated；仅显式提供时发送，服务端不校验，>= 1 |
| `AuthUser` | `X-OP-AUTH-USER` | 可选/Deprecated；仅显式提供时发送，服务端不校验，被总结用户 uuapId |
| `Intent` | 用户意图 | 必填，非空白，<= 500 字符 |
| `StartTime` / `EndTime` | 时间范围（Unix **秒**） | 可选；同时提供时需 `StartTime < EndTime` |
| `Keyword` | 搜索关键词 | 可选，<= 20 字符 |
| `GroupIDs` | 群 ID 列表 | 可选，<= 100 项 |
| `DoubleChatUserIDs` | 单聊对象列表 | 可选，<= 100 项，不能含当前用户 |
| `SpeakerUserID` | 只总结该发言人 | 可选，<= 128 字符 |

`CreateMessageSummaryTaskResult`：受理成功返回 `TaskID`、`Status=PENDING`、`ExpireAt`（毫秒）。
bizCode `730004`（受理状态未知）时 `AcceptanceUnknown=true`，仅返回 `TaskID`，应调用
`GetTask` 确认任务是否已创建，不要重复提交。其余非 200 业务码为 `BusinessError`。

`GetMessageSummaryTaskRequest` 包含 `UgateToken`（必填）与 `TaskID`（合法 UUID）；
`CorpID`、`AgentID`、`AuthUser` 为可选/Deprecated 遗留身份字段，仅显式提供时发送
`X-OP-*` 头（默认不发送，服务端不校验）。
`GetMessageSummaryTaskResult` 返回 `TaskID`、`Status`、`Summary`（SUCCESS/PARTIAL_SUCCESS）、
`ErrorMsg`（FAILED）与 `ExpireAt`。任务自身 `FAILED` 时 `bizCode` 仍为 200，需读 `Status`；
查询类业务错误（如 `730001`/`730002`）为 `BusinessError`。结果与错误的 `RawJSON()` 返回防御性拷贝。

```go
created, err := client.Bot.MessageSummary.CreateTask(ctx, im.CreateMessageSummaryTaskRequest{
	UgateToken: ugateToken, Intent: "重点总结风险和待办",
})
taskID := created.TaskID

task, err := client.Bot.MessageSummary.GetTask(ctx, im.GetMessageSummaryTaskRequest{
	UgateToken: ugateToken, TaskID: taskID,
})
fmt.Println(task.Status, task.Summary, task.ErrorMsg)
```

## Bot Robot：资料、菜单、快捷指令、回调

### 机器人资料与快捷指令

- `GetInfo(ctx context.Context) (robot.Result, error)`：用途是读取机器人资料；`POST /imRobot/detail`，JSON `{}`；返回 `robot.Result` 和原始 JSON；成功 envelope 为 `code=ok` 或 `errcode=0`。
- `GetShortcutCommands(ctx context.Context) (robot.Result, error)`：读取快捷指令；`POST /robot/commands/get`，JSON `{}`；返回原始 `robot.Result`。
- `SetShortcutCommands(ctx context.Context, commands []robot.ShortcutCommandGroup) (robot.Result, error)`：整体替换指令；`POST /robot/commands/set`，JSON `{"all_commands":[{"type":1,"commands":[{"name":"帮助","description":"查看帮助"}]}]}`。
- `AppendShortcutCommands(ctx context.Context, commands []robot.ShortcutCommandGroup) (robot.Result, error)`：追加指令；路径为 `POST /robot/commands/append`，JSON 与 `SetShortcutCommands` 相同。
- `GetUserFollowStatus(ctx context.Context, userIDs []string) ([]robot.FollowUser, error)`：查询 1 到 100 个用户；`POST /app/agent/isFollow`，JSON `{"userids":["u001"]}`；返回 `FollowUser{UserID, IsFollow, FollowTime}`。
- `IsUserFollowing(ctx context.Context, userID string) (bool, error)`：查询单个用户，内部复用上一个方法和同一路径；`userID` 必填，返回单个 `IsFollow`。

快捷指令的 `Type` 必须为 1 到 4，名称和描述各 1 到 100 个字符；空用户 ID、超过 100 个用户和空指令均为 `ConfigError`。业务错误为 `BusinessError`，结果的 `RawJSON()` 可保留完整响应。

```go
following, err := client.Bot.Robot.IsUserFollowing(ctx, "u001")
fmt.Println(following, err)
```

### 菜单

- `Get(ctx context.Context) (robot.MenuDefinitionResult, error)`：读取菜单；`GET /robot/menu/get`，无请求体；返回 `Menus []MenuItem`，典型 `{"errcode":0,"menus":[{"name":"帮助","action":{"dataType":"push"}}]}`。
- `Update(ctx context.Context, menus []robot.MenuItem) (robot.MenuUpdateResult, error)`：替换完整菜单；`POST /robot/menu/update`，JSON `{"menus":[...]}`；顶层至少 1 项、最多 20 项，子菜单最多 10 项且最多两层；叶子必须有 action，父项不能同时有 action 和 subMenus。

`MenuItem` 的 `Action.DataType` 只能是 `push` 或 `url`；`url` 必须提供非空 `PCURL`、`MobileURL`，`PCBrowser` 为 0-2，`MobileOpenType` 为 0-1。链接配置字段为 `pcUrl`、`pcBrowser`、`mobileUrl`、`mobileOpenType`、可选 `width`/`height`。非法菜单为 `ConfigError`，非零 `errcode` 为 `BusinessError`。

```go
result, err := client.Bot.IM.Menu.Update(ctx, []robot.MenuItem{{Name: "帮助", Action: &robot.MenuAction{DataType: "push"}}})
```

### UpdateCallbackURL

- 用途：一次性更新机器人回调配置。
- 签名：`UpdateCallbackURL(ctx context.Context, request robot.CallbackUpdateRequest) (robot.Result, error)`。
- 参数：`CallbackURL`、`Token`、`EncodingAESKey`、`ReceiveMsgState`、`EnableWebSocket` 均可选，但至少提供一个回调字段。
- 请求：`POST /imRobot/updateReCallUrl`；JSON 字段为 `recallUrl`、`token`、`encodingAesKey`、`receiveMsgState`、`enWebSocket`。
- 返回：`robot.Result`；典型成功 `{"errcode":0,"errmsg":"ok"}`。
- 错误：空更新为 `ConfigError`，非零业务码为 `BusinessError`。

```go
r, err := client.Bot.Robot.UpdateCallbackURL(ctx, robot.CallbackUpdateRequest{EnableWebSocket: 1})
```

## Bot Calendar / Meeting：机器人会议日程

机器人身份入口为 `client.Bot.Calendar` 和 `client.Bot.Meeting`，使用应用 token 和 qyOpenAPI base URL。显式设置 `Config.BaseURL` 时，SDK 使用该地址，便于离线测试和网关切换。

### Bot Calendar

- `CreateEvent(ctx, calendar.EventRequest) (calendar.EventResult, error)`：`POST /workflow/api/calendar/qyOpenApi/v1/createEvent`，从响应得到 `EventID`。
  `EventRequest.BodyType` 与 `EventRequest.Conference` 有安全默认值：真实服务会以
  业务码 `410` 拒绝缺失/为空的 `bodyType`（`BodyType参数错误`）、缺失/为 null 的
  `conference`（`conference不能为null`）以及 `conference.bzType != 1` 的请求。
  因此 SDK 在 `CreateEvent` / `UpdateEvent` 中对 `BodyType` 缺省为 `"Text"`，
  对 `Conference` 缺省为 `{"bzType":1}`；调用方显式设置的值原样保留。
- `UpdateEvent(ctx, calendar.EventRequest) (calendar.EventResult, error)`：`PUT .../updateEvent`，`CalendarID` 和 `EventID` 作为 query 标识。
- `DeleteEvent(ctx, calendar.EventRequest) (calendar.EventResult, error)`：`DELETE .../deleteEvent`，用于清理本次 smoke 创建的日程。
- `GetEvent(ctx, calendar.EventRequest) (calendar.EventResult, error)`：`GET .../getEvent`。
- `GetFreeBusy(ctx, calendar.FreeBusyRequest) (calendar.FreeBusyResult, error)`：`POST .../freeBusy`。
  平台会返回两种响应形态，SDK 均已归一化为 `FreeBusyResult.FreeBusyList []FreeBusyItem`：
  对象形态 `{"code":"ok","data":{"freeBusyList":[...]}}` 与数组形态
  `{"code":"ok","data":{"code":"ok","data":[...]}}`（`data.data` 数组 + snake_case 字段）。
  `FreeBusyItem` 字段对齐 Node 参考实现：`Email`、`Appointments []FreeBusyAppointment`
  （`start_time`/`end_time`，Unix 秒）、`BusySlots []BusySlot`（`start`/`end`）。
  当平台未直接返回 `busySlots` 时，SDK 由 `appointments` 派生；`busySlots` 恒为非空。

### Bot Meeting

- `CallMeeting(ctx, meeting.CallRequest) (meeting.Result, error)`：`POST .../callmeeting`，从响应得到后续接口所需的 `CID`。
- `BatchInvite(ctx, meeting.BatchInviteRequest) (meeting.BatchInviteResult, error)`：`POST .../batchInvite`。
- `JoinGroup(ctx, meeting.JoinGroupRequest) (meeting.JoinGroupResult, error)`：`POST .../joingroup`。
- `FetchASR(ctx, meeting.CIDRequest) (meeting.ASRResult, error)`：`POST .../fetchasr`。
- `FetchAIGC(ctx, meeting.AIGCRequest) (meeting.AIGCResult, error)`：`POST .../fetchAigc`。
- `FetchLinkInfo(ctx, meeting.CIDRequest) (meeting.LinkInfoResult, error)`：`POST .../fetchLinkInfo`。

真实 smoke 的会议顺序是 `CallMeeting -> BatchInvite -> FetchASR/FetchAIGC/FetchLinkInfo -> JoinGroup`，CID 只从机器人创建会议的响应中传递，不接受外部任意 CID。会议不设置结束/解散操作，按平台自动结束策略记录 `automatic_end_expected`；日程 mutation 走 `Create -> Get -> Update -> FreeBusy -> Delete`，删除失败必须阻塞后续批次。

## Bot Events：事件分发

入口为 `client.Bot.Events`，Webhook 和 WebSocket 共用同一个 dispatcher。

- `NewDispatcher(options events.Options) *events.Dispatcher`：创建 dispatcher，不联网；`Options` 可配置 `Observer`、`Diagnostics`、`OnError`、`Now`。
- `On(route string, handler events.Handler) error`：按注册顺序持续监听；`route` 非空，handler 非 nil。典型路由为 `private.text`、`group.text`、`event.message_summary_result`、`*`。
- `Once(route string, handler events.Handler) error`：注册一次性监听，第一次调用前移除；参数和 `On` 相同。
- `Off(route string, handler events.Handler) bool`：移除同一个函数值注册，返回是否移除。
- `Dispatch(ctx context.Context, route string, event events.Event) error`：发送已经标准化的事件，调用精确路由后再调用 `*`。
- `DispatchFrame(ctx context.Context, raw []byte) error`：解析 JSON 并自动识别 private/group/event/trigger 路由；`raw` 必须是 JSON 对象。
- `DispatchPayload(ctx context.Context, payload map[string]any, raw []byte) error`：用于传输层已解码的 payload，payload 和 raw 会复制后再交给 handler。
- `OnSummary(route string, options events.SummaryEventOptions, handler events.SummaryHandler) (func(), error)`：将摘要事件转换为 `SummaryEventResult`；返回幂等清理函数。`IncludeRaw` 为 true 才保留原始 payload。
- `OnInteractionSubmit(handler events.InteractionSubmitHandler) (func(), error)`：为
  `event.interaction_submit` 路由安装类型化 handler，handler 接收归一化后的
  `events.InteractionSubmitEvent`；返回幂等清理函数。`NormalizeInteractionSubmitEvent(event events.Event) (events.InteractionSubmitEvent, error)`
  把协议原始帧归一化为稳定字段（`EventID`、`CardInstanceID`、`UserID`、`Timestamp`、
  `GroupID`、`CardID`、`Version`、`Caller`、`Receiver`），`Submit` 为透传的不解释载荷；
  非交互提交帧返回 `*events.InteractionSubmitError`。

事件 handler 签名为 `func(context.Context, events.Event) error`。`Event.Payload()`、`RawPayload()` 都返回防御性拷贝。解析失败返回 `*events.ParseError`，handler 错误通过 `OnError`/诊断回调报告。最小示例：

```go
handler := func(ctx context.Context, event events.Event) error {
	log.Println(event.Type, event.MessageType)
	return nil
}
if err := client.Bot.Events.On("group.text", handler); err != nil { log.Fatal(err) }
```

## Bot Webhook

### NewReceiver、Handle、Register

- `NewReceiver(options webhook.Options) (*webhook.Receiver, error)`：创建框架无关接收器，不联网。`Dispatcher` 必填；`CheckToken`、`EncodingAESKey` 用于验签/解密；时间、去重容量、最大 body 和 `Executor` 可选。
- `Service.NewReceiver(options webhook.Options) (*webhook.Receiver, error)`：同上，但自动使用 `client.Bot.Events` 和 Client 的诊断配置。
- `Receiver.Handle(ctx context.Context, request webhook.Request) webhook.Response`：处理原始 HTTP 请求；`Request.Body` 必须是网络收到的完整字节，不能先由框架解析。支持 `application/x-www-form-urlencoded` 和 `text/plain`，返回 `Status` 与 `Body`，失败时可能返回 400、413 或 503。表单信封优先取 `messageJson`，缺失时回退到 XML `message`；卡片事件（精确 `EventType=INTERACTIVE_MSG_CARD_EVENT`）使用 `card_id:EventId` 去重 10 分钟，并在业务 handler 之前返回 200，默认 executor 下异步分发。
- `Receiver.Dispatcher() *events.Dispatcher`：取得共享 dispatcher，不联网。
- `Service.Register(ctx context.Context, registration webhook.Registration) (webhook.RegistrationResult, error)`：切换平台到 Webhook；`POST /imRobot/updateReCallUrl`，JSON `{"recallUrl":"https://example.test/callback","token":"token","encodingAesKey":"aes-key","receiveMsgState":1}`；要求 URL 是无用户名的绝对 HTTPS URL，Token 和 AES key 非空。`Registration` 没有 JSON tag，SDK 使用内部 wire struct 映射字段。

注册成功返回 `RegistrationResult{Code, Message, LogID}`，原始 JSON 可用 `RawJSON()` 获取；业务失败为 `BusinessError`。接收器默认同步执行 callback，配置 `Executor` 时，若 `Submit` 返回 false 会返回 503，让平台重试。默认 body 上限 20 MiB、验签时间容差和去重 TTL 均为 5 分钟。

```go
receiver, err := client.Bot.Webhook.NewReceiver(webhook.Options{CheckToken: "token", EncodingAESKey: "aes-key"})
if err != nil { log.Fatal(err) }
resp := receiver.Handle(ctx, webhook.Request{Method: http.MethodPost, Headers: headers, Body: body})
```

## Bot WebSocket

- `Service.NewSession(options websocket.SessionOptions) (*websocket.Session, error)`：校验并创建重连会话，不联网；默认由 Client 创建 endpoint allocator、dialer 和 WebSocket callback registrar。`Dispatcher` 可省略，自动使用共享事件分发器。`websocket.NewSession` 是无 Client 的低层入口，要求调用方在 options 中提供 `Dispatcher`、`EndpointAllocator`、`Dialer` 和 `Registrar`（或关闭自动注册）。
- `Service.Register(ctx context.Context, callbackToken string) error`：同步切换 WebSocket 回调模式；`POST /imRobot/updateReCallUrl`，JSON `{"enWebSocket":1,"receiveMsgState":1,"token":"32位小写十六进制"}`；callback token 必须正好 32 位小写十六进制。
- `NewEndpointAllocator(options websocket.EndpointOptions) (websocket.EndpointAllocator, error)`：创建独立 endpoint 分配器，不联网；实际分配时 `POST https://<gateway>/open/ws/endpoint`，JSON `{"app_key":"...","app_secret":"..."}`，返回一次性 `Endpoint{URL, Config}`。不要持久化或打印 URL。
- `Session.WaitRegistration(ctx context.Context) error`：等待自动注册结果；关闭自动注册时立即返回 nil。
- `Session.State() websocket.State`：读取当前状态；常见值为 `disconnected`、`connecting`、`connected`。
- `Session.Connect(ctx context.Context) error`：分配 endpoint、建立连接并等待连接阶段完成；可并发调用，调用会合并到一次尝试。
- `Session.Disconnect(ctx context.Context) error`：主动断开当前 generation 并取消重连；之后可重新 Connect。
- `Session.SendFrame(ctx context.Context, frame *websocket.Frame) error`：发送 protobuf frame；会复制 frame，必须已连接。
- `Session.SendData(ctx context.Context, payload any) error`：把 payload JSON 编码为 DATA frame；必须已连接，payload 为 nil 时编码为 `{}`。
- `EncodeFrame`/`DecodeFrame`：确定性 protobuf 编解码；`CloneFrame`：防御性复制；`EncodePayload`/`DecodePayload`：开放 JSON payload 编解码，解码大整数为 `json.Number`。`websocket.NewCoderDialer` 返回实现 `Dialer` 的传输适配器；`DialerFunc`、`EndpointAllocatorFunc`、`RegistrarFunc`、`SchedulerFunc` 是将函数适配为对应接口的 exported 类型。

典型连接顺序如下；`AutoRegister` 默认 true，仍应等待注册结果后再宣告 ready：

```go
session, err := client.Bot.WebSocket.NewSession(websocket.SessionOptions{})
if err != nil { log.Fatal(err) }
if err = session.Connect(ctx); err != nil { log.Fatal(err) }
if err = session.WaitRegistration(ctx); err != nil { log.Fatal(err) }
err = session.SendData(ctx, map[string]any{"type": "ping"})
defer session.Disconnect(context.Background())
```

连接阶段错误为 `*websocket.SessionError` 或 `*websocket.EndpointError`；常见 category 包括 `not_connected`、`queue_full`、`invalid_context`、`timeout`。HTTP endpoint 非成功、回调注册业务失败分别按 endpoint/session 错误或 `BusinessError` 处理。

## User API

User 服务通过 `infoflow.Config.User` 配置 Robot2C/UGate 身份，入口为 `client.User`。大多数返回类型为 `RawResult`，其 `Data` 是 SDK 提取的 data 节点，`RawJSON()` 保留完整响应。

### User IM

- `GetLatestContacts(ctx context.Context, r userim.LatestContactsRequest) (userim.LatestContactsResult, error)`：分页读取最近联系人；反复 `POST /im/get_latest_contacts`，JSON 从 `{"timestamp":0,"page_start":0}` 开始，以响应 timestamp 继续，`MaxPages` 默认 10，`Limit` 为 0 表示不限制，`IncludeRaw` 保存原始页。返回去重后的 `Contacts{ID,Type,Name,Raw}` 和数量文本；业务继续码为 210。
- `CreateSummary(ctx context.Context, r userim.CreateSummaryRequest) (userim.SummaryResult, error)`：创建消息摘要任务；`POST /im/robot/summary`，JSON `{"chatId":"1001","chatType":2,"startTime":1700000000000,"endTime":1700003600000}`；`ChatID`、`ChatType`（字符串 `1`、`2` 或 `7`）、递增毫秒窗口必填，返回 `RequestID`。
- `ImportSummary(ctx context.Context, r userim.ImportSummaryRequest) (userim.SummaryResult, error)`：导入/查询摘要任务；`POST /im/importMsgSummary`，body 包含字符串形式 `startQueryTime`、`endQueryTime` 以及可选 `topicType`、`intent`、`unreadType`；时间窗口必填，`topicType` 只能为 `1`/`2`，`unreadType` 只能为 `1`，intent 最多 50 字符。

```go
contacts, err := client.User.IM.Message.GetLatestContacts(ctx, userim.LatestContactsRequest{Limit: 20, IncludeRaw: true})
```

### User Calendar

- `CreateEvent(ctx context.Context, r usercalendar.CalendarRequest) (usercalendar.RawResult, error)`：创建日程；`POST /v1/calendar/events/v4`，body 为 `r.Body`，必须非空。
- `UpdateEvent(ctx context.Context, r usercalendar.CalendarRequest) (usercalendar.RawResult, error)`：更新日程；`POST /v1/calendar/events/v4/update`，将 `EventID` 拼入 body 的 `eventId`，EventID 必填。
- `DeleteEvent(ctx context.Context, r usercalendar.CalendarRequest) (usercalendar.RawResult, error)`：删除日程；`GET /v1/calendar/events/v4/delete?eventId=<id>`，EventID 必填。
- `GetEvent(ctx context.Context, r usercalendar.CalendarRequest) (usercalendar.RawResult, error)`：读取日程；`GET /calendar/openUGate/api/v1/events?eventId=<id>`，使用 Calendar/UGate base URL，EventID 必填。
- `QueryMyMeetingDetail(ctx context.Context, r usercalendar.MeetingListRequest) (usercalendar.RawResult, error)`：查询我的会议详情；`POST /workflow/api/meeting/inner/listall`，body 由会议查询字段拼接。
- `GetFreeBusy(ctx context.Context, r usercalendar.FreeBusyRequest) (usercalendar.RawResult, error)`：查询忙闲；`POST /v1/calendar/freebusy`，JSON `{"startTime":1700000000000,"endTime":1700003600000,"emails":["a@example.com"]}`；时间递增且 emails 非空。
- `QuerySchedule(ctx context.Context, r usercalendar.FreeBusyRequest) (usercalendar.RawResult, error)`：查询日程，当前实现复用 `GetFreeBusy`，路径和请求完全相同。

```go
result, err := client.User.Calendar.GetFreeBusy(ctx, usercalendar.FreeBusyRequest{StartTime: start, EndTime: end, Emails: []string{"a@example.com"}})
```

### User Meeting

- `ListMeetings(ctx context.Context, r usermeeting.ListRequest) (usermeeting.RawResult, error)`：分页列出会议；`POST /workflow/api/meeting/inner/listall`，JSON 字段为 `beginTime`、`endTime`、`timeZone`、`pageSize`、`showEnd`、`showCalendar`、`pageNumber`、`filterTypes`、`filterItems`。`BeginTime`、`TimeZone`、正数 `PageSize`/`PageNumber` 必填；数字时区字符串会拼接为 JSON 数字。
- `FetchASR(ctx context.Context, eventID string) (usermeeting.RawResult, error)`：获取会议 ASR；`POST /calendar/openUGate/api/v1/fetchasr`，JSON `{"eventId":"event-1"}`。
- `FetchAIGC(ctx context.Context, eventID string) (usermeeting.RawResult, error)`：获取会议 AIGC；`POST /calendar/openUGate/api/v1/fetchaigc`，JSON 同上。
- `FetchLinkInfo(ctx context.Context, eventID string) (usermeeting.RawResult, error)`：获取会议链接；`POST /calendar/openUGate/api/v1/fetchLinkInfo`，JSON 同上。
- `CallMeeting(ctx context.Context, r usermeeting.CallRequest) (usermeeting.RawResult, error)`：发起会议；`POST /calendar/openUGate/api/v1/callMeeting`，JSON `{"subject":"例会","inviteUsers":["a@example.com"],"startRecord":false,"startAigc":false}`；Subject 和 InviteUsers 必填。

```go
meetings, err := client.User.Meeting.ListMeetings(ctx, usermeeting.ListRequest{BeginTime: start, TimeZone: "8", PageSize: 20, PageNumber: 1})
```

### UserAuthorization

- `List(ctx context.Context, r userauthorization.ListRequest) (userauthorization.Result, error)`：查询授权关系；`GET /robot2c/auth/list`，可选 query `instanceId`、`status`、`st`、`pageSize`、`pageToken`。
- `Grant(ctx context.Context, r userauthorization.GrantRequest) (userauthorization.Result, error)`：授予权限；`POST /robot2c/auth/grant`，JSON `{"instanceId":"instance-1","user":"u001","permissions":["calendar.read"],"expiredDays":30}`；InstanceID、User、Permissions、正数 ExpiredDays 必填。
- `Revoke(ctx context.Context, r userauthorization.RevokeRequest) (userauthorization.Result, error)`：撤销权限；`POST /robot2c/auth/revoke`，JSON `{"instanceId":"instance-1","user":"u001"}`；InstanceID、User 必填。

```go
r, err := client.UserAuthorization.List(ctx, userauthorization.ListRequest{InstanceID: "instance-1", PageSize: 20})
```

用户接口使用 UGate/Robot2C headers 和配置的独立 base URL；空 token、参数非法为 `ConfigError`，平台 envelope 的 `code`、嵌套业务码失败为 `BusinessError`，无法解析为 `MalformedResponseError`。涉及创建、更新、删除或发起会议的接口会改变真实资源，调用方应使用已获批准的目标并自行清理。

## Bot 动态卡（`bot/dynamiccard`）

动态卡（AI dynamic message，scheme `"1"`）由公开包 `bot/dynamiccard` 承载，并通过
`client.Bot.IM.DynamicCard` 暴露发送与更新能力：内容与组件模型、响应判定、错误模型、
本地预检和标识符类型均在 `bot/dynamiccard` 中，网络与 token 细节收在
`internal/dynamiccard`。`Send`、`Update`、`NewStream` 流式会话、卡片事件接收与事件回执
（`Ack`）均已可用。基础层模型本身不发起网络请求、不读写凭据。

### 发送（`client.Bot.IM.DynamicCard.Send`）

`Send(ctx context.Context, request dynamiccard.SendRequest) (dynamiccard.CardRef, error)`
向 qyopenapi profile 的 `POST /app/message/sendUnifiedMsg` 发送一条动态消息：

- 目标二选一：`ToUser` 与 `GroupID` 必须且只能有一个非空白；SDK 据此写
  `message.header.toType`（`SINGLE` / `GROUP`）与 `message.header.toId`
  （`{"toUser":...}` 或 `{"groupId":...}`）。
- 固定字段：`message.header.msgType="INTERACTIVE"`、`message.header.role="robot"`、
  `message.contents.scheme="1"`。
- `SessionID` 非空时原样写入 `message.header.extra.sessionId`；`SessionID`、`At`、
  `Reply` 都为空时省略 `extra`。
- `At *dynamiccard.AtOptions`（可选）支持群成员 @（`Users`）与 @全体（`All`）。
  KU 接入指南 v37 把 @ 声明放在顶层 `message.body`（与 `contents` 平级）：
  `message.body = [{"type":"AT","atall":<bool>,"atusers":[...]}]`。`atall` 始终输出；
  `atusers` 在名单非空时输出，且**与 `atall` 相互独立**——v37 §8.3 的官方示例就是
  `atall:true` + 非空 `atusers`（@全体 + 指定成员），SDK 不把两者判为互斥。字段名必须
  是 `atusers`，旧的 `atuserids` / `atagentids` 不是文档契约，客户端不会显示 AT。
  `All=false` 且名单为空时不输出 `message.body`。名单含空白元素时在本地返回
  `validation`，不发请求。`Transport` 默认 `AtTransportBody`（即上述写法）；
  `AtTransportHeader` 是 legacy opt-in，只在需要兼容老调用时输出
  `message.header.extra.at`（`atAll` + `atUserIds`），默认路径不再生成该字段。
  `Transport` 现已不接受 `"both"`（v37 下无意义），传其他值本地返回 `validation`。
- `AutoMentionText`（默认 false）：AT 只声明接收者，正文里的 `@<uuapId>` 才决定显示位置。
  设为 true 时 SDK 在最靠前的 markdown 组件内容前补齐缺失的 `@<uuapId> `；保持 false
  且正文缺任一 `@uuapId` 时不改写正文，改为在返回的 `CardRef.Warnings` 中给出
  `at_mention_text_missing` 告警（不阻塞请求）。
- `AtAgentIDs` 保留为已废弃字段（`Deprecated: 文档 v37 未定义`），不再产生任何 wire
  输出，仅用于源码兼容。
- 写入 AT 节点时，若 `Raw` 已提供数组形式的 `raw.message.body`，SDK 保留该数组并把
  AT 节点追加到末尾；该位置不是数组时本地返回 `validation`。没有 `At`（或 body 无
  可写内容）时不输出 `message.body`。
- `Reply *dynamiccard.ReplyOptions`（可选）写入 `message.header.extra.reply`；
  `MessageID` 必填且始终以字符串发送（19 位 ID 不丢精度），空白时本地返回
  `validation`；`FormatReplyMessageID` 可把数字 ID 转成该字符串形式，负数或超出
  IEEE-754 安全整数范围（`MaxSafeReplyMessageID`）时本地拒绝。`Digest` /
  `SenderName` 为空时不输出对应 key。
- `At` / `Reply` 只影响首次 `sendUnifiedMsg`；后续 enable / append / finish / update
  不能修改消息 header。`SessionID`、`At`、`Reply` 同时存在时三者全部保留。
- `Content` 必填且组件非空，发送前执行 `ValidateContent`（P0/P1）；本地失败返回
  `validation` 类 `DynamicCardError`，不发起请求。
- `Raw` 只补充 SDK 未设置的字段：同名已知字段优先，对象逐层合并，`toId` 完全由
  SDK 派生；强类型 `At` / `Reply` 与 raw 同路径冲突时强类型字段优先，raw 只能补充
  其中未知的子字段；非法 raw JSON 在本地拒绝。无 `At` / `Reply` 时请求体与旧版完全
  一致。
- 结果 `CardRef` 的 `CardID` 取 `data.data.extra.card_id`，缺失时回退
  `data.data.card_id`，仍缺失则返回 `server` 类 `DynamicCardError` 并保留原始响应；
  `MessageID` 依次取 `data.data.partial.data.messageid`、`data.data.messageid` /
  `message_id`、`partial.data.msgid` / `msg_id`，最后回退 `partial.msgkey`（按十进制
  字符串输出）；`MsgSeqID` / `CTime` 同样优先 `partial.data.*`，再取顶层
  `msgseqid` / `msg_seq_id` 与 `ctime` / `c_time`。null 或缺省值会被跳过，不会变成
  `"null"` / `"<nil>"`；全部按字符串无损输出（19 位 ID 不丢位）。
- 发送不自动重试，包括 `6001` 与 `9001`；失败按 `DynamicCardError` 分类返回。
- 动态卡 transport 自行解析 base URL：`INFOFLOW_DYNAMIC_CARD_BASE_URL` 环境变量覆盖
  默认生产地址，不复用 `Config.BaseURL`；token 走 `POST /auth/app_access_token`，
  header 使用 `Authorization: Bearer-{token}` 与 `X-OP-ACCESS-CHANNEL: go_sdk`。

### 更新（`client.Bot.IM.DynamicCard.Update`）

`Update(ctx context.Context, request dynamiccard.UpdateRequest) (dynamiccard.UpdateResult, error)`
向 `POST /interactive/message/update` 更新已有动态卡：

- `UpdateType` 判别模式，且在类型层与请求字段互斥：
  - `full`：`CardContent` 必填且组件非空，`Updates` 必须为空；发送前执行
    `ValidateContent`（含 63,000 字节）。
  - `element`：`Updates` 必填且非空，`CardContent` 必须为 nil；每项是**完整组件
    对象**（顶层 `type` + `id`），嵌套组件同样必须带 `type` + `id`；同请求内全部
    `id` 唯一，且不得出现祖先/后代同时更新。
- 两种模式都**不产生 `sequence` 字段**；请求体也不会出现另一模式的字段。
- 可选 `CurrentContent` 基线校验（仅 element）：目标 `id` 必须存在、组件原 `type`
  不得改变、跨层级祖先/后代不得同时更新；替换 `row`/`column` 时若基线有子组件而
  请求省略 `children`，返回 `container_children_missing` 告警但仍发送。
- 成功结果：`CardID`（响应缺失时回填请求值）、`Sequence`（响应缺失时为 0 并附
  `sequence_missing` 告警）、`Message`、`Warnings`、`Raw`。
- 错误映射：`1001` → `validation`，`3003` → `ownership`，`9001` → `server`；
  `1010` → `stream_state`，`Message` 必含「请先 finish 流式消息」，
  并在响应携带时填充 `CurrentSequence` / `CurrentContent`。
- 更新不自动重试，也不自动 finish 流式消息（finish 属于业务决策）。

### 流式会话（`client.Bot.IM.DynamicCard.NewStream`）

`NewStream(ctx context.Context, options dynamiccard.StreamOptions) (*dynamiccard.DynamicCardStream, error)`
先复用 `Send` 创建一张只含一个 markdown 组件的卡，然后返回 `created` 状态的会话：

- create 复用发送能力，骨架是 `{"type":"markdown","id":<elementId>,"content":<initialMarkdown>}`；
  `ElementID` 为空时默认 `"desc"`，`InitialMarkdown` 为空时首屏为空；成功后
  `Sequence=1`，**不自动 enable**。
- `ThrottleMs *int`：nil 默认 `1000`，`0` 关闭节流（用 `dynamiccard.Int(0)` 显式关闭）；
  负数本地拒绝。
- `At` / `Reply` / `Raw` 只作用于 create 阶段那次 `sendUnifiedMsg`：`At` 默认写入顶层
  `message.body` 的 AT 节点、`Reply` 写入 `message.header.extra.reply`，校验规则与
  `Send` 完全一致（含 `autoMentionText` 与 `at_mention_text_missing`）；
  `Raw` 复用 `SendRequest.Raw` 的补充语义（已知字段优先，强类型 `At` / `Reply`
  优先）。create 成功后无法再修改消息 header，enable / append / finish 的公共类型
  也不提供 AT 入参。
- `Enable(ctx, content)`：`POST /interactive/message/modifyStreaming`，
  `action="enable"`、`streaming_elements` 至少一项且目标必须是 markdown；空 `content`
  复用当前快照。成功后 `streaming`。重复 enable 由服务端 `1010` 兜底并进入 `failed`，
  SDK 不会自动 finish 后重开。
- `Append(ctx, snapshot)`：`POST /interactive/message/streaming/append`，`chunk` 是**完整
  快照**而非增量；`AppendDelta(ctx, delta)` 在 Unicode 字符串层维护快照后调用 `Append`，
  空 delta 不发请求，混用 `Append` 时以完整快照覆盖内部快照。
- `Finish(ctx, body, options)`：`action="finish"`，`body` 只含 `children`，**永不输出
  `scheme`**；存在 `agent-flow-stream` 结构（含 markdown content 内的 JSON 骨架）时把
  `status` 置为 `completed`；markdown 内容中的 `agent-flow-stream` 围栏头同样只把首个
  `status="..."` 改为 `status="completed"`，缩进与其他字节保持不变；
  `body` 为 nil 或空时使用当前快照；`options.DelayMs > 0`
  先按毫秒等待再发送；已 `finished` 再调用返回幂等结果且不发请求。
- 同卡串行、跨卡并行：同一 `cardId` 的 append/enable/finish 串行（进程内），
  节流窗口内的多次 `Append` 合并为一次并只发最新快照，每个等待者以覆盖它的那次
  请求结果完成；进入 finish 后拒绝新 append。Session 是进程内对象，跨进程/多实例
  仍需业务协调。
- `sequence` 只用成功响应值覆盖，从不本地 `+1`；成功响应缺 `sequence` 记为
  `protocol` 语义的 `server` 错误并让会话进入 `failed`。
- `1009`：若响应携带 `current_sequence` 就覆盖本地值，抛 `sequence_conflict`
  （`RequestSequence` / `CurrentSequence` / `CurrentContent` 判空后填充），不自动重放，
  会话保持 `streaming`。
- `6001`：最多重试 1 次，固定退避 1000ms（共享 transport 未透出 `Retry-After`，
  使用契约的无头兜底），只重发最新快照且 `sequence` 不变；二次失败抛 `rate_limited`。
- `1010`：重复 enable、未 enable 直接 append → `failed`；已在流式中的 append/finish
  → `expired`。create 起 60 分钟（`StreamLifetime`）后 `State()` 返回 `expired`，
  append/finish 本地快速失败且不发请求；SDK 不主动关闭服务端流。
- `1001` / `3003` / `401` → 会话 `failed`；`9001` / 5xx 抛错但会话状态不变。
- `State()` 返回 `created` / `streaming` / `finished` / `expired` / `failed`；
  `Sequence()`、`Snapshot()`、`CardID()`、`ElementID()`、`ThrottleMs()`、`StartedAt()`
  为只读访问器。旧 `client.Bot.IM.Message.NewStreamingCardSession` 行为与路由不变。

```go
stream, err := client.Bot.IM.DynamicCard.NewStream(ctx, dynamiccard.StreamOptions{
    ToUser:          "zhangsan",
    InitialMarkdown: "# 正在生成\n\n",
})
if err != nil { return err }
if _, err := stream.Enable(ctx, ""); err != nil { return err }
for _, chunk := range chunks {
    if _, err := stream.AppendDelta(ctx, chunk); err != nil { return err }
}
_, err = stream.Finish(ctx, nil, dynamiccard.FinishOptions{DelayMs: 4000})
```

### 事件接收（`client.Bot.Webhook.NewReceiver` + `client.Bot.Events.OnCardEvent`）

交互卡片事件复用现有 Webhook 接收链路，不新增 HTTP 入口：

- 解析优先级：表单 `messageJson` > 表单 `message`（XML 信封，兼容 `<Encrypt>` /
  `<encrypt>`）> `text/plain` 原始密文；`messageJson` 存在时 `message` 被忽略。
- 信封必须携带 `Encrypt`；解密为 AES-128-ECB / PKCS5，密钥是
  `EncodingAESKey + "=="` 的 URL-safe Base64 解码结果（16 字节），密文为 URL-safe
  Base64。解密后先按 JSON、再按 XML 解析。
- 外层 `EventType` **精确等于** `INTERACTIVE_MSG_CARD_EVENT` 时进入卡片事件分支
  （区分大小写，且优先于群/私聊消息分支）；归一化为 `dynamiccard.CardEvent`。
- 去重键 `card_id + ":" + EventId`，TTL 10 分钟；重复事件返回 200 `success` 且不再
  分发。
- 返回 200 在解密、解析、去重之后、业务处理之前：默认 executor 下事件交给 goroutine
  异步分发，handler 不阻塞响应；注入 `CallbackExecutor` 时由调用方控制调度。
- 非法帧（`EventData` / `card_id` 缺失、`current_msg_content` 或 `client_context`
  结构错误）丢弃、记录错误并仍返回 200。
- 路由键 `event.interactive_msg_card_event`，同时触发 `event.*` 与 `*`；
  `client.Bot.Events.OnCardEvent(handler)` 注册强类型 handler。Webhook 响应本身不是 ACK，
  业务语义回执需另调 `client.Bot.IM.DynamicCard.Ack`（见下一节）。
- `action_bar` 的赞踩产生 `action.type="footer_operation"`
  （`dynamiccard.CardEventActionFooterOperation`）：`ElementID` 是 action_bar 的 `id`，
  `ActionValue` 是被点击的操作 key（`vote_up` / `vote_down`），`ClientContext` 保留平台
  原始对象数组，可读取 `source_platform`、`state`（1 选中 / 0 取消）与 `biz_extra`。
  `ClientContext` 的每个值是 `ClientContextValue`：`Text` 暴露字符串/标量文本，`Values`
  暴露字符串数组，`Raw` 保留原始 JSON，因此数字 `state` 不会退化成带引号字符串。
- 安全：不记录事件明文、密文、token 或 `encodingAesKey`；`current_msg_content`
  可能很大，完整帧不得整体打印。多实例下进程内去重只能降低重复率，业务仍需按
  `EventId` 自行幂等。

### 事件回执（`client.Bot.IM.DynamicCard.Ack`）

`Ack(ctx context.Context, request dynamiccard.AckRequest) (dynamiccard.AckResult, error)`
以独立 HTTP 调用 `POST /interactive/message/card_event_ack` 回执一次交互事件；
`AckEvent(ctx, event dynamiccard.CardEvent, ackData dynamiccard.AckData)` 是它的
`CardEvent` 快捷形式（只取 `CardID` / `EventID`，不会从事件的
`CurrentContent` 自动推导 `card_update`）。两者都不是 Webhook 响应，也不使用事件中心
ACK token。

- 请求体固定为 `{card_id, event_id, ack_data:{code, mode, toast?, card_update?}}`，
  `ack_data` 始终是 JSON 对象而不是字符串。
- `AckModeSync`（`sync`）：业务已在 ACK 前完成，可带 `toast` 与可选 `card_update`。
  `AckModeAsync`（`async`）：只表达「已受理」，`code` 必须为 `0`，`card_update` 一律
  本地拒绝，`toast` 不得暗示业务已完成。
- `card_update` 等同更新请求去掉 `card_id` / `sequence`：`full` 要求 `scheme="1"` 的
  `CardContent` 且不含 `updates`；`element` 要求完整组件（`type` + `id`）、同请求 id
  唯一、不得祖先/后代同时出现。`CardUpdate` 的 JSON 编码在类型层就不输出
  `card_id` / `sequence`。
- 本地校验矩阵（全部返回 `validation`，不发请求）：`cardId` / `eventId` 空白、
  `ackData` 缺失（`mode` 缺失即视为缺失）、`mode` 非 `sync`/`async`、`code != 0` 且
  `async`、`code != 0` 且无 `toast`、`code != 0` 且带 `card_update`、`async` 带
  `card_update`、`toast` 无 `content`、`toast.level` 非法、`card_update` 的
  `update_type` 与内容不匹配、`element` 更新项缺 `type`/`id`。
- 4 秒 / 10 分钟窗口以 `AckRequest.ReceivedAt`（事件接收时刻，零值表示「刚刚」）起算，
  返回值带 `AgeMs`：`sync` 且 `AgeMs > 4000` 仍发送并附 `ack_window_exceeded_toast_suppressed`
  告警（平台应用 `card_update` 但不下发 toast）；`async` 超 4 秒默认仍发送并由服务端
  返回 `1011`，`StrictWindow=true` 时本地直接拒绝；`AgeMs > 600000` 一律本地拒绝，
  返回 `ack_expired` 且不发请求。
- 结果 `AckResult`：`Accepted` 等价于 `code == 0`；`AlreadyProcessed` 表示平台返回
  `ack already processed`，仍视为成功（该次 `toast` / `card_update` 不生效）；
  `Sequence` 仅在应用了 `card_update` 时存在；`ProceedWithBusiness` **仅** `async` 且
  成功时为 `true`，其余一律 `false`；失败时仍填充 `Mode`、`AgeMs`、`Message` 与 `Raw`。
- 错误映射：`1001` → `validation`，`1010` → `stream_state`（不携带 `current_*` 字段，
  本次 `toast` 与 `card_update` 均不下发），`1011` → `ack_expired`（保留平台原始
  message，两种 `card event ack window closed` 文案都归入此类别），`3003` →
  `ownership`，`9001` → `server`，`401` → `auth`，网关字符串 code → `gateway`
  （不当作成功）。
- 回执不自动重试（包括 `9001`）；幂等键 `card_id + event_id` 由平台保证生效一次，
  业务仍需按 `EventId` 自行保证只执行一次业务。

```go
result, err := client.Bot.IM.DynamicCard.AckEvent(ctx, event, dynamiccard.AckData{
    Code:  0,
    Mode:  dynamiccard.AckModeSync,
    Toast: &dynamiccard.AckToast{Level: dynamiccard.AckToastLevelSuccess, Content: "已提交"},
    CardUpdate: &dynamiccard.CardUpdate{
        UpdateType: dynamiccard.UpdateTypeElement,
        Updates:    dynamiccard.ComponentList{dynamiccard.NewMarkdown("desc", "已提交")},
    },
})
if err != nil { return err }
if result.AlreadyProcessed {
    // 平台已处理过该事件；本次 toast / card_update 未生效。
}
```

### 内容与组件模型

- `NewContent(children ...Component) *DynamicCardContent`：构造
  `{"scheme":"1","body":{"children":[...]}}`；`Scheme` 为空时编码自动补 `"1"`。
- `DynamicCardContent{Scheme string; Body CardBody; Footer *Footer}`；
  `CardBody{Config *CardConfig; Children ComponentList}`；
  `Footer{Config *CardConfig; Children ComponentList}`（`NewFooter(children...)`，
  编码为 `contents.footer` —— 与 `contents.body` **平级**，不在 body 内部；结构与 body
  完全一致，省略时不输出 `footer`）；
  `CardConfig{MaxWidthDesktop *int; Spacing *int}`（JSON `max_width_desktop`、`spacing`，
  桌面最大宽度仅在正数时生效）。
- `Component` 接口要求 `ComponentType() string` 和 `json.Marshaler`。8 个实现：
  `MarkdownComponent`（`NewMarkdown`）、`TextComponent`（`NewText`）、
  `ButtonComponent`（`NewButton`）、`InputComponent`（`NewInput`）、
  `RadioGroupComponent`（`NewRadioGroup`）、`RowComponent`（`NewRow`）、
  `ColumnComponent`（`NewColumn`）、`ActionBarComponent`（`NewActionBar`）。
  `row`/`column` 的 `children` 可嵌套。
- `ActionBarComponent{Type; ID string; Actions []ActionBarItem}`：`action_bar` 是纯声明，
  图标、布局、展示顺序（`ai_tag` → `tts` → `vote_up` → `vote_down` → `forward`）和赞踩
  状态都由客户端固定，推荐挂在 `contents.footer.children`。`ActionBarItem` 字段为
  `Key`（`ActionKeyAITag` / `ActionKeyTTS` / `ActionKeyVoteUp` / `ActionKeyVoteDown` /
  `ActionKeyForward`，未知 key 原样透传）、`Platform`（`ActionPlatformAll` / `PC` /
  `Mobile`）、`EventEnabled *bool`（仅控制是否产生 `footer_operation` 事件，不影响可点）、
  `BizExtra`（原样 JSON 字符串）、`Title map[string]string`（如
  `{"zh_cn":"有用","en_us":"Helpful"}`，`ai_tag` 必填）、`Color`、`URL`、`OpenType`
  （`ActionOpenTypeNavigation` / `WebView` / `Sidebar`）与 `Extra map[string]any`
  未知字段透传。本地只校验结构最小集：`id` 非空、`actions` 非 nil、每项 `key` 非空、
  `ai_tag` 带 `title`；未知 key/字段不拒绝。
- `RawComponent`（`NewRaw(componentType, id, extra)`）：未知类型、无 id 或不建模的
  组件原样透传；`type`/`id` 优先于同名 extra 字段。
- 每个组件都有 `Extra map[string]json.RawMessage`（`json:"-"`）：已知字段之外的字段
  平铺序列化，同名时已知字段优先；解码时未知字段回填 `Extra`，往返不丢字段。
- `ComponentList` 按 `type` 分发解码，未知类型退化为 `RawComponent`；nil 编码为 `[]`
  而不是 `null`。
- 可选字段用指针表达显式零值：`dynamiccard.Bool(v)`、`dynamiccard.Int(v)`；未设置时
  省略，显式 `false` / `0` 会保留。`RadioValue` 同时支持单选字符串和多选数组，
  同时设置时 `Multiple` 优先。
- 引用回复的数字 ID：`dynamiccard.FormatReplyMessageID(v int64)` 返回可直接赋给
  `ReplyOptions.MessageID` 的十进制字符串；负数或大于
  `dynamiccard.MaxSafeReplyMessageID`（`2^53-1`）时返回 `validation` 错误，19 位 ID
  请直接使用字符串形式。
- 枚举常量：`ButtonMethodDataSubmit` / `ButtonMethodQuery` / `ButtonMethodJSSDK`；
  `ButtonStylePrimary` / `ButtonStyleSuccess` / `ButtonStyleDanger`；`ButtonWidthWrap`；
  `DirectionRow` / `DirectionColumn`。
- `ButtonAction.UnmarshalJSON` 同时接受文档的字符串数组和单字符串两种 `views` 形式。
  `column` 不支持 `wrap`，解码时丢弃该字段。

### Markdown 渲染助手（`dynamiccard.Markdown`）

`var Markdown = dynamiccard.MarkdownHelper{}` 是与语言无关的 markdown 语法片段纯字符串
渲染助手（无网络、无状态）。每个方法返回 `(string, error)`，参数非法时返回 `validation`
类 `DynamicCardError`；输出字节与跨语言 markdown spec 一致：

- `AtUser(userID)` → `@userId`；`AtAgent(agentID)` 接受 string 或安全非负整数 →
  `@agentId`；`AtAll()` → `@all`。
- `Color(text, color)` → `<font color="red">text</font>`，颜色仅限
  `MarkdownColorRed` / `Green` / `Gray` / `Blue` / `Orange`。
- `Link(text, url)` → `[text](url)`；`Image(url, *MarkdownImageOptions)` →
  `![alt](url "title")`（`Alt` / `Title` 为空时省略对应部分）。
- `Divider()` → `---`。
- `CodeBlock(language, code)` → 自适应反引号围栏（长度至少 3，且大于代码中最长反引号串
  的长度）。
- `Table(rows)` → 首行表头 + `| --- |` 分隔行；行数非空且每行列数必须一致。
- `ReplyQuote(MarkdownReplyQuoteOptions)` → `reply` 围栏块，`s_basemsgid` 与
  `SenderName` 必填，`MsgID2` / `SimType` 可选；属性值转义 `"`，摘要不得包含三连
  反引号。
- `ButtonGroup(buttons, *MarkdownButtonGroupOptions)` → `[::button-group ...]` 块，
  `Layout` 取 `flow` / `equant` / `vertical` / `banner`，`GapH` / `GapV` / `Radius`
  非负；每个按钮必须有非空 `Label` 和至少一个事件字段，`OpenType` 取
  `MarkdownOpenTypeN` / `S` / `A` / `B`。
- `AgentFlowStream(AgentFlowOptions)` → `agent-flow-stream` 围栏结构：头部
  `status="streaming|completed"`、`thinking_expanded="true|false"`；块类型为
  `AgentFlowBlockKindReasoning`（`:::reasoning`）或 `AgentFlowBlockKindTool`
  （`:::tool name="..." status="..."`，`Input` / `Output` 序列化后以 `---` 分隔）。
- `Join(parts ...string)` 过滤空串后用空行（`\n\n`）连接。

`Bool` / `Float` 用于表达可选布尔/数值的显式零值（如 `ThinkingExpanded`、`Radius`）。

```go
body, err := dynamiccard.Markdown.AgentFlowStream(dynamiccard.AgentFlowOptions{
    ThinkingExpanded: dynamiccard.Bool(true),
    Blocks: []dynamiccard.AgentFlowBlock{
        {Kind: dynamiccard.AgentFlowBlockKindReasoning, Text: "先读文件"},
        {Kind: dynamiccard.AgentFlowBlockKindTool, Name: "read_api",
            Status: dynamiccard.AgentFlowToolStatusCompleted,
            Input:  map[string]any{"file": "a.txt"},
            Output: map[string]any{"result": "ok"}},
    },
})
content, err := dynamiccard.Markdown.Join(body, "最终答案")
```

### 响应判定

- `DecodeResponse(raw []byte) CardResponse` 执行三层 envelope 判定：
  非 JSON 对象 → `gateway`；`{"code":"ok"}` → 取 `data`，业务码为 `payload.code`；
  数字 `code` → 平铺业务失败；其他字符串 `code` → `gateway`；其余 → `unknown`。
  `unknown` 永远不按成功处理。
- `CardResponse{Shape ResponseShape; Code string; Message string; Payload; Raw json.RawMessage;
  HTTPStatus int; LogID string}`；`Shape` 取 `ResponseSuccess` / `ResponseBusiness` /
  `ResponseGateway` / `ResponseUnknown`。`OK()`、`RawJSON()`（防御性拷贝）、
  `WithHTTPStatus(status)`、`WithLogID(logID)`。
- `CardResponse.Error(operation Operation) *DynamicCardError`：成功返回 nil；类别先按
  业务码、再回退到 HTTP 状态，无法识别时为 `CategoryUnknown`。

### 错误模型

`DynamicCardError` 字段：`Code any`（`int64` 业务码或 gateway 原始字符串）、
`Category`、`Message`、`HTTPStatus`、`Operation`、`Retryable`、`CardID`、`EventID`、
`RequestSequence *int64`、`CurrentSequence *int64`、`CurrentContent *DynamicCardContent`、
`LogID`、`Cause error`、`Raw json.RawMessage`。错误信息只包含操作、类别、code 和简短
message，不包含凭据、token 或卡片内容。业务逻辑应按 `Category` 或 `Code` 分支，不要
匹配 message。

12 个类别由 `ClassifyCode(code int64)` / `ClassifyHTTPStatus(status int)` 返回
`ErrorClassification{Category, Retryable, Known}`；`Known=false` 时调用方必须按
`CategoryUnknown` 处理，不能当作成功。对应的 `Is*` 判定方法：
`IsValidation`、`IsSequenceConflict`、`IsStreamState`、`IsAckExpired`、`IsOwnership`、
`IsRateLimited`、`IsAuth`、`IsNotFound`、`IsDuplicate`、`IsServer`、`IsGateway`、
`IsUnknown`。

业务码：

| code | 类别 | 可重试 |
|-|-|-|
| `1001`、`1002` | `validation` | 否 |
| `1009` | `sequence_conflict` | 否（采纳 `current_sequence`，不盲目重放） |
| `1010` | `stream_state` | 否 |
| `1011` | `ack_expired` | 否 |
| `3003` | `ownership` | 否 |
| `6001` | `rate_limited` | 是（退避后只重发最新快照，不整体回滚顺序） |
| `9001` | `server` | 是 |

HTTP 状态：

| status | 类别 | 可重试 |
|-|-|-|
| `400`、`408`、`411` | `validation` | 否 |
| `401` | `auth` | 否（不要无限重试） |
| `403`、`410` | `ownership` | 否 |
| `404`、`405` | `not_found` | 否 |
| `406` | `stream_state` | 否 |
| `407` | `sequence_conflict` | 否 |
| `409` | `duplicate` | 是（按幂等成功处理） |
| `429` | `rate_limited` | 是 |
| `500`、`501` | `server` | 是 |

### 本地预检

`ValidateContent(content *DynamicCardContent) ([]Warning, error)`，以及带
`ValidationOptions{SkipSizeCheck, SkipDepthCheck}` 的
`ValidateContentWithOptions`。P0/P1 失败返回 `*DynamicCardError`（`CategoryValidation`）：

- P0：`component.type` 非空；`markdown.content` 必须是字符串；整个卡片（含嵌套）的
  `id` 必须唯一；`scheme` 只能为 `"1"` 或空；组件和 content 不能为 nil。
- P1：编码后 UTF-8 字节数 ≤ `MaxContentBytes`（63,000）；JSON 深度 ≤
  `MaxContentDepth`（14，根对象为第 1 层，每层嵌套组件增加 2 层）。
- P2 只返回 warning，从不阻塞请求：组件数 > `RecommendedMaxComponents`（200）为
  `WarningTooManyComponents`；组件嵌套层数 > `RecommendedMaxNesting`（3）为
  `WarningDeepNesting`。服务端仍是最终权威。

### 标识符

`ID int64`：19 位平台 ID 在内部按 `int64` 保存，`MarshalJSON` 始终输出 JSON 字符串，
避免 53 位精度丢失；`UnmarshalJSON` 同时接受数字、十进制字符串和 `null`。
`NewID(value)`、`ParseID(text)`（拒绝空值和非法数字）、`Int64()`、`String()`。

## 公开请求与结果字段清单

下面的清单以当前 Go exported facade、service 方法签名和实际 JSON/query 构造为准。标记为“否”的字段可以使用零值；`omitempty` 只表示零值可能不出现在 JSON 中，不改变服务端对业务必填字段的校验。

### Bot IM 消息类型

#### `im.SendUserMessageRequest`

| 字段 | Go 类型 | 必填性与约束 | JSON/query 字段 |
|-|-|-|-|
| `ToUser` | `string` | 必填，不能全为空白 | `touser` |
| `Type` | `string` | 必填；`text`、`md`、`image`、`richtext`、`news` | `msgtype` |
| `Text` | `string` | `Type=text` 或 `md` 时必填；不能为空白 | 由 SDK 按类型嵌套为 `text.content` 或 `md.content`，不是直接字段 |
| `Image` | `string` | `Type=image` 时必填；不能为空白 | `image.content` |
| `RichText` | `[]im.RichTextNode` | `Type=richtext` 时非空；每个节点必须符合 `Type` 规则 | `richtext.content` |
| `Articles` | `[]im.NewsArticle` | `Type=news` 时非空；每项 `Title`、`URL` 必填 | `news.articles` |
| `Reply` | `[]im.UserReply` | 可选；每项字段按回复协议填写 | `reply` |

`RichTextNode` 字段为 `Type string`（必填，`text` 或 `a`）、`Text string`（`text` 节点必填，JSON `text`）、`Href string`（`a` 节点必填，JSON `href`）、`Label string`（`a` 节点必填，JSON `label`）。`NewsArticle` 字段为 `Title string`（必填，`title`）、`Description string`（可选，`description`）、`URL string`（必填，`url`）、`PictureURL string`（可选，`picurl`）。`UserReply` 字段为 `Content string`（`content`）、`UID string`（`uid`）、`MessageID string`（`msgid`）、`MessageID2 string`（`msgid2`）；回复数组可选，服务端按消息类型解释字段。

#### `im.SendGroupMessageRequest`

| 字段 | Go 类型 | 必填性与约束 | JSON/query 字段 |
|-|-|-|-|
| `GroupID` | `int64` | 必填，必须大于 0 | `message.header.toid` |
| `Type` | `string` | 必填；`TEXT`、`IMAGE`、`MD`（大小写不敏感） | `message.header.msgtype`，SDK 转为大写 |
| `Body` | `[]im.GroupBody` | 必填且非空 | `message.body` |
| `Reply` | `*im.GroupReply` | 可选；非 nil 才发送 | `message.reply` |
| `ClientMessageID` | `int64` | 可选；0 由 SDK 生成，负数非法 | `message.header.clientmsgid` |

`GroupBody` 字段为：`Type string`（必填，`TEXT`/`IMAGE`/`MD`/`LINK`/`AT`/`CMD`，JSON `type`）；`Content string`（TEXT/IMAGE/MD 时必填，`content`）；`Href string`（LINK 时必填，`href`）；`Label string`（可选，`label`）；`AtAll bool`（AT 可选，`atall`）；`AtUserIDs []string`（AT 可选，`atuserids`，最多 50）；`AtAgentIDs []int64`（AT 可选，`atagentids`，最多 10）；`CommandName string`（CMD 必填，`commandname`）；`TargetAgentID int64`（CMD 必填且大于 0，`targetAgentId`）。TEXT 最多 2000 个 Unicode 字符，MD 最多 2048 个。`GroupReply` 字段为 `MessageID string`（`messageid`）、`Preview string`（`preview`）、`IMID string`（`imid`）、`ReplyType string`（可选，`replytype`）。

`SendToUserResult` 字段为 `MessageKey int64`（成功响应 `data.msgkey`，必有）和 `InvalidUser string`（失败用户标识，`data.invaliduser`，可为空）；`SendToGroupResult` 字段为 `MessageID int64`（`data.messageid`）、`MessageSequenceID int64`（`data.msgseqid`）、`CreatedAtMillis int64`（`data.ctime`），三者均要求成功响应存在。结果还提供 `RawJSON() json.RawMessage`，返回完整响应的防御性拷贝。

#### `im.GroupMarkdownMention` 与 `im.SendGroupMarkdownMentionRequest`

`GroupMarkdownMention` 字段为：`AtAll bool`（true 时 @ 全体并短路，用户/机器人名单被忽略）；`AtUserIDs []string`（@ 用户 ID 列表，最多 50）；`AtAgentIDs []int64`（@ 机器人 agentId 列表，最多 10）。`SendGroupMarkdownMentionRequest` 字段为：`GroupID int64`（必填且大于 0，`message.header.toid`）；`Markdown string`（必填非空白，`message.body` 的 MD 块 content，最多 2048 字）；`Mentions im.GroupMarkdownMention`（@ 目标，由 `BuildGroupMarkdownMentionBody` 注入）；`SelfAgentID int64`（当前机器人 agentId，命中 `AtAgentIDs` 时拒绝，0 表示未提供）；`Reply *im.GroupReply`（可选，`message.reply`）；`ClientMessageID int64`（可选，0 由 SDK 生成，`message.header.clientmsgid`）。

#### 其他 `im.MessageService` 请求与结果

| 类型 | 字段（Go 类型；必填/约束） | JSON/query 字段 |
|-|-|-|
| `ListGroupMessagesRequest` | `ReceiverID int64`（必填且大于 0）；`StartTime int64`、`EndTime int64`（必填，递增且窗口不超过 24h）；`SortType string`（可选，`asc`/`desc`）；`PageSize int`（可选，默认 20，最大 50）；`PageToken string`（可选） | query `receiverId`、`startTime`、`endTime`、`sortType`、`pageSize`、`pageToken` |
| `GetMessageRequest` | `FromID string`、`ReceiverID int64`、`ReceiverType int`、`MessageID int64`（必填；`ReceiverType` 为 2 或 7）；`InnerMessageID/InnerFromID/InnerReceiverID int64`、`InnerReceiverType int`（可选，但必须全部填写或全部省略） | query `fromId`、`receiverId`、`receiverType`、`msgId`；内层为 `innerMsgId`、`innerMsgFromId`、`innerMsgReceiverId`、`innerMsgReceiverType` |
| `RevokeUserMessageRequest` | `MessageKey string`（必填，`msgkey`）；`AgentID int64`（必填且大于 0，`agentid`） | JSON `msgkey`、`agentid` |
| `RevokeGroupMessageRequest` | `GroupID int64`、`MessageID int64`、`MessageSequenceID int64`（均必填且大于 0） | JSON `groupId`、`messageid`、`msgseqid` |
| `PinGroupMessageRequest` | `GroupID int64`、`MessageID int64`（必填且大于 0）；`Operation int`（必填，1=置顶，2=取消） | JSON `groupId`、`msgId`、`opType` |
| `EmojiRequest` | `FromUID string`、`BaseMessageID string`（必填）；`ChatType int`（必填，2 或 7）；`ChatID int64`（可选，`chatId`）；`MessageID2`、`ReplyContent`、`ReplyDescription string`（添加表情时 `MessageID2`、`ReplyContent` 必填，其余可选） | JSON `fromUid`、`chatType`、`chatId`、`baseMsgId`、`msgId2`、`replyContent`、`replyDesc` |
| `ListGroupMessagesResult` | `HasMore bool`（`data.hasMore`）；`PageToken string`（`data.pageToken`）；`Items []im.MessageItem`（`data.items`） | 结果字段按 JSON 解码；另有 `RawJSON() json.RawMessage` |
| `MessageItem` | `MessageType string`（`msgType`）；`SubType string`（`subType`）；`MessageID string`（`msgId`）；`Sender json.RawMessage`（`sender`，开放对象）；`SendTime int64`（`sendTime`）；`DeletedFlag int`（`deletedFlag`）；`Content json.RawMessage`（`content`，按消息类型展开）；`Ext json.RawMessage`（`ext`，开放对象） | JSON 字段见左列 |
| `WorkCardContent` | `Type string`（`type`，`work_card`）；`CardID string`（`cardId`，缺失/`null`/非字符串时兜底为空串） | `MessageItem.WorkCard()` 返回的归一化 work_card 结构；另有 `MessageItem.WorkCardText()` / `WorkCardTextLine()` 渲染方法（见上文 work_card 小节） |
| `VoiceTranscriptionResult` | `Code string`（`data.code`）；`Content string`（`data.content`） | JSON 结果字段；另有 `RawJSON() json.RawMessage` |

`CreateStreamingCard`、`UpdateUserStreamingCard`、`UpdateGroupStreamingCard` 的参数 `p json.RawMessage` 必须是合法 JSON；SDK 不展开其平台定义的嵌套结构，原样作为请求 body 发送并返回 `json.RawMessage`。这三个方法的参数不是 `map[string]any`，调用示例应使用 `json.RawMessage` 或 `json.Marshal` 的结果。

### Bot IM 文件、StreamingCardSession 与 Bubble 卡类型

#### `im.FileService` 请求与结果

| 类型 | 字段（Go 类型；必填/约束） | JSON/query 字段 |
|-|-|-|
| `UploadFileOptions` | `File []byte`（必填，非空）；`FileName string`（必填，不能含 `\r`/`\n`，且不得含 `/`）；`ObjectKey string`（可选，缺省时 SDK 生成 `sdk-uploads/{yyyyMMdd}/{uuid}{ext}`；提供时须满足 `^[A-Za-z0-9._/-]+$`，不能以 `/` 开头、不能含 `//` 或 `..`，长度 ≤ 1024）；`ExpirationSeconds int`（1–86400，0 表示默认 3600） | multipart 字段 `file`（filename=`FileName`）与 `objectKey`（**始终发送**） |
| `UploadFileResult` | `ObjectKey string`（`data.object_key`，必有）；`ETag string`（`data.e_tag`，可选）；`URL string`（上传后 getUrl 得到的短期 URL）；`ExpirationSeconds int` | 结果字段按 JSON 解码；另有 `RawJSON() json.RawMessage` |
| `BosFileURLOptions` | `ObjectKey string`（必填，非空白）；`ExpirationSeconds int`（1–86400）；`ResponseContentDisposition string`（可选） | query `objectKey`、`expirationSeconds`、`responseContentDisposition` |
| `BosFileURLResult` | `ObjectKey string`；`URL string`（`data.url`，必有）；`ExpirationSeconds int`（`data.expiration_seconds`，缺省用请求值） | 结果字段按 JSON 解码；另有 `RawJSON() json.RawMessage` |
| `FileDownloadURLOptions` | `FID string`（必填，非空白）；`ChatID string`、`FileMsgID string`（群路由必填）；`ExpSeconds int`（可选，正整数） | 私聊 body `fid`、`chatType=1`；群 body `fid`、`chatType=2`、`chatId`、`fileMsgId`、可选 `expSeconds` |
| `FileDownloadURLResult` | `URL string`（必有）；`FileName string`（可选）；`FileSize int64`（可选） | B-file-url envelope 的 `data.data`；另有 `RawJSON() json.RawMessage` |
| `DownloadFileOptions` | `FID string`（必填）；`ChatType im.FileChatType`（`FileChatPrivate=1`/`FileChatGroup=2`）；`ChatID string`、`FileMsgID string`（群必填）；`ExpSeconds int`（可选）；`MaxBytes int64`（可选，>0 时超出报错） | 先走下载 URL 路由，再以 `x-openapi-gateway-identity` 拉取字节 |
| `DownloadFileResult` | `URL string`；`FileName string`；`FileSize int64`；`Content []byte`；`ContentType string`（响应 `Content-Type`） | 下载字节与元数据 |

`Upload` 在 getUrl 失败时返回 `*im.UploadURLRecoveryError`，其 `ObjectKey` 字段携带已上传
的 object key（不会重新上传）；`errors.As` 可提取。涉及 URL 的错误会把 URL 从
`MalformedResponseError.RawResponse()` 中脱敏。

#### `im.StreamingCardSession` 类型

| 类型 | 字段（Go 类型；必填/约束） | 说明 |
|-|-|-|
| `StreamingCardTargetType` | `string`；`im.StreamingCardTargetUser` / `im.StreamingCardTargetGroup` | 会话接收方类型 |
| `StreamingCardTarget` | `Type StreamingCardTargetType`（必填）；`ID string`（必填，非空白） | 私聊为用户登录名，群聊为群 ID |
| `StreamingCardSessionOptions` | `Target StreamingCardTarget`（必填）；`UserIDs []string`（默认 `[Target.ID]`）；`NotifyList []map[string]any`（默认群 `[{"to_type":2,"to_ids":[Target.ID]}]`）；`AnswerFormat string`（`markdown`/`text`）；`InitialText string` | `NewStreamingCardSession` 入参 |
| `StreamingCardSessionState` | `Target StreamingCardTarget`；`ModifyToken string`；`Version int`；`Started bool`；`Done bool`；`Failed bool`；`UpdateFails int`；`AnswerText string`；`ReasoningText string` | `State()` 只读快照 |

#### `im.BubbleWorkflowCard*` 类型

| 类型 | 字段（Go 类型；必填/约束） | JSON 字段 |
|-|-|-|
| `BubbleWorkflowCardRenderOptions` | `CardID string`（必填）；`CardInstanceID string`（可选）；`Caller BubbleWorkflowCardCaller`（`CallerName` 必填）；`Question string`（可选）；`Receiver BubbleWorkflowCardReceiver`（`Type` 必填 `group`/`user`，`ID` 必填）；`Card BubbleWorkflowCardDefinition`（`Components` ≤ 8、`Buttons` ≤ 6）；`RefreshDefaultIntermediateState *bool`（可选） | `card_id`、`card_type=general_form`、`card_instance_id`、`caller{caller_name,request_id}`、`question`、`receiver{type,id}`、`card`、`refresh_default_intermediate_state` |
| `BubbleWorkflowCardDefinition` | `Title string`（可选）；`Components []BubbleWorkflowCardComponent`；`Buttons []BubbleWorkflowCardButton` | `card.title`、`card.components`、`card.buttons` |
| `BubbleWorkflowCardComponent` | `ComponentID string`（必填且唯一）；`Type BubbleWorkflowCardComponentType`（`text`/`input`/`single-select`/`multi-select`/`dropdown-single`/`dropdown-multi`）；`Text string`（text 必填）；`Description string`（可选）；`PlaceholderText string`（input/dropdown 可选）；`Options []BubbleWorkflowCardOption`（选择类必填非空，每项 `Value` 非空） | `component_id`、`type`、`text`、`description`、`placeholder_text`、`options[{value}]` |
| `BubbleWorkflowCardButton` | `EventKey string`（必填）；`Text string`（必填） | `event_key`、`text` |
| `BubbleWorkflowCardRenderResult` | `OK bool`；`Created bool`；`Caller *BubbleWorkflowCardCaller`；`Receiver *BubbleWorkflowCardReceiver`；`CardInfo *BubbleWorkflowCardCardInfo`（`CardID`/`CardType`/`Revision *int`）；`Recognized bool`；`Updated bool` | 结果字段按 B-bubble envelope 解码；另有 `RawJSON() json.RawMessage` |

#### `events.InteractionSubmitEvent`

`InteractionSubmitEvent` 字段为 `EventID string`（`MsgId`）、`CardInstanceID string`、
`UserID string`、`Timestamp time.Time`（`Time` 毫秒）、`GroupID string`（`groupId`）、
`CardID string`（`cardId`）、`Version string`、`Caller events.InteractionSubmitCaller`
（`callerName`/`requestId`）、`Receiver events.InteractionSubmitReceiver`（`id`/`type`）、
`Submit any`（透传不解释）、`Payload map[string]any`（原始载荷防御性拷贝）。非交互提交
载荷的归一化返回 `*events.InteractionSubmitError`。

### Bot IM 群请求与结果

| 类型 | 字段（Go 类型；必填/约束） | JSON 字段 |
|-|-|-|
| `CreateGroupRequest` | `GroupName string`、`GroupOwner string`（必填且非空）；`MemberList []string`、`RobotList []int64`、`Managers []string`、`RobotManagers []int64`（可选）；`FriendlyLevel int`（必填，1-3）；`SearchAbility int`（必填，0-1）；`GroupSidebar *im.GroupSidebar`（可选） | `groupName`、`groupOwner`、`memberList`、`robotList`、`friendlyLevel`、`searchAbility`、`managers`、`robotManagers`、`groupSidebar` |
| `GroupSidebar` | `AutoOpen int`（`autoOpen`）；`CustomSidebar int`（`customSidebar`）；`Link string`（`link`） | 嵌套于 `groupSidebar` |
| `CreateGroupResult` | `GroupID int64`（`groupId`，成功时大于 0）；`FailMembers []string`（`failMembers`）；`FailRobotIDs []int64`（`failRobotIds`） | JSON result；另有 `RawJSON() json.RawMessage` |
| `GroupIDRequest` | `GroupID int64`（必填且大于 0） | `groupId` |
| `DisbandGroupRequest` | `GroupID int64`（必填且大于 0）；`GroupOwner string`（必填且非空） | `groupId`、`groupOwner` |
| `MemberListRequest` | `GroupID int64`（必填且大于 0）；`RecallType int`（可选） | `groupId`、`recallType` |
| `MembersRequest` | `GroupID int64`（必填且大于 0）；`MemberList string`、`AgentIDList string`（至少一个非空；平台约定为逗号分隔字符串） | `groupId`、`memberList`、`agentIdList` |
| `UpdateGroupNameRequest` | `GroupID int64`（必填且大于 0）；`Name string`（必填且非空） | `groupId`、`name` |
| `UpdateBulletinRequest` | `GroupID int64`（必填且大于 0）；`Bulletin string`（SDK 不强制非空） | `groupId`、`bulletin` |
| `SidebarRequest` | `GroupID int64`（必填且大于 0）；`SidebarLink string`（由平台解释）；`AutoOpen *int`、`CustomSidebar *int`（nil = 不修改；显式 0/1 时平台按字段出现即设置） | `groupId`、`sidebarLink`、`autoOpen`（omitempty）、`customSidebar`（omitempty） |
| `AddChatTabRequest` | `ChatID int64`（必填且大于 0）；`TabName string`、`TabLink string`（必填且非空） | `chatId`、`tabName`、`tabLink` |
| `MenuVisibility` | `OperationType string`（必填，`show` 或 `hide`）；`MainMenuKeys []string`、`SubMenuKeys []string`（可选；nil 会编码为空数组） | `customMenus.operationType`、`customMenus.mainMenuKeys`、`customMenus.subMenuKeys` |
| `UpdateMenuVisibilityRequest` | `GroupID int64`（必填且大于 0）；`CustomMenus im.MenuVisibility`（必填） | `groupId`、`customMenus` |
| `SetManagersRequest` | `GroupID int64`（必填且大于 0）；`UserEmails []string`、`RobotAgentIDs []int64`（至少一个管理员，总数 1-19） | `groupId`、`userEmails`、`robotAgentIds` |
| `ModifyUserRemarkRequest` | `ToUserID string`、`GroupID string`、`Remark string`（均必填且非空；`GroupID` 的公开 Go 类型是 `string`） | `toUserId`、`groupId`、`remark` |

群方法 `Create` 返回 `CreateGroupResult`，其余群方法返回 `RawResult`；`RawResult.RawJSON() json.RawMessage` 是完整 envelope 的防御性拷贝。`AddMembers`/`RemoveMembers` 使用同一个 `MembersRequest`，不会把 `MemberList` 自动转换为 `[]string`。

### Bot Robot、Webhook 与 WebSocket 类型

| 类型 | 字段（Go 类型；必填/约束） | JSON/query 或运行时含义 |
|-|-|-|
| `robot.ShortcutCommandGroup` | `Type int`（1-4）；`Commands []robot.ShortcutCommand`（非空） | `type`、`commands`；每个 `ShortcutCommand` 的 `Name string`（必填，JSON `name`，1-100 字符）和 `Description string`（必填，JSON `description`，1-100 字符） |
| `robot.FollowUser` | `UserID string`、`IsFollow bool`、`FollowTime string` | `userId`、`isFollow`、`followTime` |
| `robot.MenuItem` | `Name string`（非空）；`SubMenus []robot.MenuItem`（顶层最多 20、子菜单最多 10，最多两层）；`Action *robot.MenuAction`（叶子必须有 action，父项不得有 action） | `name`、`subMenus`、`action` |
| `robot.MenuAction` | `DataType string`（必填，`push` 或 `url`）；`LinkConfig *robot.MenuLinkConfig`（仅 `url` 必填，`push` 禁止）；`EventKey string`（当前实现不校验且不用于 action 判定） | `dataType`、`linkConfig`、`eventkey` |
| `robot.MenuLinkConfig` | `PCURL string`、`MobileURL string`（`url` action 必填）；`PCBrowser int`（0-2）、`MobileOpenType int`（0-1）；`Width *int`、`Height *int`（可选） | `pcUrl`、`pcBrowser`、`mobileUrl`、`mobileOpenType`、`width`、`height` |
| `robot.CallbackUpdateRequest` | `CallbackURL`、`Token`、`EncodingAESKey string`（可选，但更新至少一个字段）；`ReceiveMsgState`、`EnableWebSocket int`（可选开关） | `recallUrl`、`token`、`encodingAesKey`、`receiveMsgState`、`enWebSocket` |
| `webhook.Registration` | `CallbackURL string`（绝对 HTTPS）；`Token string`、`EncodingAESKey string`（非空） | POST body `recallUrl`、`token`、`encodingAesKey`，注册接口固定发送 `receiveMsgState=1` |
| `webhook.RegistrationResult` | `Code int64`、`Message string`、`LogID string` | 返回字段；另有 `RawJSON() json.RawMessage` |
| `webhook.Request` | `Method string`；`Query url.Values`；`Headers http.Header`；`Body []byte`（必须是网络原始 bytes） | 框架适配输入，不直接序列化为 API JSON |
| `webhook.Response` | `Status int`；`Body string` | 框架适配输出；超 body 413，拒绝请求 400，异步队列拒绝 503 |
| `webhook.Options` | `Dispatcher *events.Dispatcher`（必填）；`CheckToken string`、`EncodingAESKey string`、`Observer`、`Diagnostics`、`OnError`、`Executor`（可选）；`TimestampTolerance`、`DedupeTTL time.Duration`、`MaxBodySize int`、`DedupeCapacity int`（非负，零取默认值）；`Now func() time.Time`（可选） | 接收器运行时配置，不是远端请求字段 |
| `websocket.Endpoint` | `URL string`（一次性 endpoint，不得持久化或打印）；`Config websocket.ServerConfig` | endpoint 分配结果 |
| `websocket.ServerConfig` | `ReconnectCount *int`；`ReconnectInterval`、`ReconnectNonce`、`PingInterval *time.Duration`（nil 表示服务端未提供） | endpoint 返回的连接策略 |
| `websocket.EndpointOptions` | `AppKey`、`AppSecret string`（必填）；`Gateway string`、`Timeout time.Duration`、`HTTPClient *http.Client`、`Observer`、`LookupEnv func(string)(string,bool)`、`Now func() time.Time`（可选） | endpoint 分配配置；`Timeout` 非负，零取默认值 |

`websocket.SessionOptions` 的字段为：`Gateway string`、`EndpointTimeout time.Duration`、`HTTPClient *http.Client`、`Dispatcher *events.Dispatcher`、`EndpointAllocator`、`Dialer`、`Registrar`、`Scheduler`、`Observer`、`Diagnostics`、`OnError`、`ConnectDomain`、`XSource`（可选，默认 `go_sdk`，显式前缀归一为 `{prefix}_go_sdk`，已带后缀的完整值原样保留）；`ConnectTimeout`、`WriteTimeout`、`ReconnectInterval`、`AutoRegisterDelay time.Duration`（非负）；`MaxReconnectAttempts *int`（nil 使用默认策略）；`HeartbeatInterval time.Duration`；`AutoAck *bool`、`AutoRegister *bool`（nil 默认 true，可显式 false）；`Queues websocket.QueueCapacities`（其 `Control`、`Receive`、`Send`、`Callback`、`Writer` 均为 `int`，零取默认值，负数非法）；`Jitter websocket.JitterFunc`（可选，接收 `max time.Duration` 并返回 `[0,max]` 延迟）；`TokenReader io.Reader`、`LookupEnv func(string)(string,bool)`、`Now func() time.Time`（可选）。这些字段控制本地 session，不编码成平台 API body。`websocket.Frame` 是生成的 protobuf alias，使用 `EncodeFrame`/`DecodeFrame`；`Session.SendData` 的 payload 是 `any`，SDK 用 JSON 编码，nil 编成 `{}`。

`events.Event` 字段为 `Type string`、`ChatType events.ChatType`、`MessageType string`、`EventType string`、`TriggerType string`、`Timestamp time.Time`；`Payload()` 返回 `map[string]any` 防御性拷贝，`RawPayload()` 返回 `[]byte` 防御性拷贝。`events.Options` 字段为 `Observer`、`Diagnostics`、`OnError` 回调和 `Now func() time.Time`。`Dispatcher.OnCardEvent(handler)` 在 `event.interactive_msg_card_event` 路由注册强类型 `dynamiccard.CardEvent` handler 并返回幂等清理函数；归一化失败通过 `OnError` 上报，不记录原始帧。`events.SummaryEventOptions` 只有 `IncludeRaw bool`；`SummaryEventResult` 字段为 `Type`、`EventType`、`Outcome string`，`Correlation SummaryCorrelation`，`Subjects []SummarySubject`，`SubjectSummaries []SummarySubjectSummary`，`HasMore *bool`，`NextBaseMsgID string`，`Events []SummaryEventItem`，`Statistics SummaryStatistics`，`Text string`，`Diagnostics SummaryDiagnostics`，`RawPayload json.RawMessage`。其中 `SummarySubject{Title, Abstract, Scheme string}`、`SummarySubjectSummary{Title, Abstract string}`、`SummaryEventItem{Topic, Summary, Reason string}`、`SummaryCorrelation{Kind, ID string}`、`SummaryDiagnostics{Code, Message, LogID string}`、`SummaryStatistics{EventCount int, SessionCount *int, MessageCount *int}`；`RawPayload` 仅在 `IncludeRaw=true` 时填充。

### Bot 动态卡类型（`bot/dynamiccard`）

| 类型/函数 | 字段（Go 类型；必填/约束） | 编码含义 |
|-|-|-|
| `dynamiccard.Service` | `Send(ctx, dynamiccard.SendRequest) (dynamiccard.CardRef, error)`；`Update(ctx, dynamiccard.UpdateRequest) (dynamiccard.UpdateResult, error)`；`NewStream(ctx, dynamiccard.StreamOptions) (*dynamiccard.DynamicCardStream, error)`；`Ack(ctx, dynamiccard.AckRequest) (dynamiccard.AckResult, error)`；`AckEvent(ctx, dynamiccard.CardEvent, dynamiccard.AckData) (dynamiccard.AckResult, error)` | `client.Bot.IM.DynamicCard` 的能力对象 |
| `dynamiccard.SendRequest` | `ToUser string`、`GroupID string`（二选一，非空白）；`SessionID string`（可选）；`At *dynamiccard.AtOptions`（可选）；`Reply *dynamiccard.ReplyOptions`（可选）；`Content *dynamiccard.DynamicCardContent`（必填）；`Raw map[string]json.RawMessage`（可选补充） | `message.header.toType`/`toId`/`msgType`/`role`/`extra`（`sessionId`/`reply`）、`message.body`（AT 节点）、`message.contents`；raw 只补充未设置字段，强类型 at/reply 优先 |
| `dynamiccard.AtOptions` | `All bool`；`Users []string`（非空时输出 `atusers`，与 `All` 相互独立）；`AutoMentionText bool`（默认 false）；`Transport string`（`body` 默认 / `header` legacy）；`AtAgentIDs []string`（Deprecated，不产生 wire） | body：顶层 `message.body = [{"type":"AT","atall":<bool>,"atusers":[...]}]`，与 `contents` 平级，`atall` 始终输出、空名单省略 `atusers`（`atall` 与 `atusers` 可同时出现）；header legacy：`message.header.extra.at`（`atAll` + `atUserIds`）；空白 uuapId、`"both"` 或其他 transport 本地 `validation` |
| `dynamiccard.CardRef.Warnings` | `[]dynamiccard.Warning` | 发送成功的 advisory 告警，例如 `at_mention_text_missing`；不阻塞请求 |
| `dynamiccard.ReplyOptions` | `MessageID string`（必填，始终按字符串发送）；`Digest string`（空省略）；`SenderName string`（空省略） | `message.header.extra.reply`；19 位 ID 用字符串，`FormatReplyMessageID` 转换安全数字 ID |
| `dynamiccard.CardRef` | `CardID`、`Secret`、`MsgKey`、`MsgSeqID`、`MessageID`、`CTime string`；`Raw json.RawMessage` | 发送结果；全部按字符串无损输出，`Secret` 敏感不得打印，`RawJSON()` 为防御性拷贝 |
| `dynamiccard.UpdateRequest` | `CardID string`（必填）；`UpdateType dynamiccard.UpdateType`（`full`/`element`）；`CardContent *dynamiccard.DynamicCardContent`（full 必填）；`Updates dynamiccard.ComponentList`（element 必填）；`CurrentContent *dynamiccard.DynamicCardContent`（可选基线） | `card_id`、`update_type`、`card_content` 或 `updates`；永不输出 `sequence` |
| `dynamiccard.UpdateResult` | `CardID string`；`Sequence int64`；`Message string`；`Warnings []dynamiccard.Warning`；`Raw json.RawMessage` | 更新结果；`Sequence` 是新的卡片版本，可作为后续流式的 `enable.sequence`；`Sequence=0` 且含 `sequence_missing` 告警表示响应缺省，`RawJSON()` 为防御性拷贝 |
| `dynamiccard.StreamOptions` | `ToUser string`、`GroupID string`（二选一，非空白）；`SessionID string`；`At *dynamiccard.AtOptions`；`Reply *dynamiccard.ReplyOptions`；`InitialMarkdown string`；`ElementID string`（空默认 `desc`）；`ThrottleMs *int`（nil 默认 1000，`0` 关闭）；`Raw map[string]json.RawMessage` | create 复用 `Send`：只构造 markdown 骨架，不自动 enable；At/Reply/Raw 仅作用于 create 的首次 send |
| `dynamiccard.DynamicCardStream` | 只读访问器 `CardID()`、`Sequence()`、`State()`、`ElementID()`、`ThrottleMs()`、`Snapshot()`、`StartedAt()`；方法 `Enable(ctx, content)`、`Append(ctx, snapshot)`、`AppendDelta(ctx, delta)`、`Finish(ctx, body, options)` | 流式会话；同卡串行 + latest-wins，`sequence` 只用响应值覆盖 |
| `dynamiccard.StreamState` | `created` / `streaming` / `finished` / `expired` / `failed` | `State()` 返回值；60 分钟（`StreamLifetime`）后惰性转 `expired` |
| `dynamiccard.StreamBody` | `Children dynamiccard.ComponentList` | finish 的 `body`；永不输出 `scheme` |
| `dynamiccard.FinishOptions` | `DelayMs int`（毫秒，默认 0） | finish 前等待；不会自动 sleep |
| `dynamiccard.StreamResult` | `CardID string`；`Sequence int64`；`State dynamiccard.StreamState`；`Raw json.RawMessage` | 单步结果；`RawJSON()` 为防御性拷贝 |
| `dynamiccard.CardEvent` | `EventType`、`DeliverType`、`EventID`、`LogID`、`CreateTime string`；`CardID string`；`CurrentSequence int64`；`CurrentContent *dynamiccard.DynamicCardContent`；`MsgBaseMsgID`、`MsgMsgID2`、`SessionID`、`ChatID`、`FromID`、`FromUserID`、`FromUserName`、`ElementID`、`ActionType`、`ActionValue string`；`ChatType int64`；`ClientContext []map[string]dynamiccard.ClientContextValue`；`Raw json.RawMessage` | 归一化事件；19 位 ID 为字符串，`ClientContext` 保留 string / 标量 / string[] 形状，`footer_operation` 时 `ElementID` 是 action_bar id、`ActionValue` 是操作 key，`RawJSON()` 为防御性拷贝且不得整体打印 |
| `dynamiccard.ClientContextValue` | `Text string` 或 `Values []string`；`Raw json.RawMessage` | `client_context` 的单个值：序列化为原始 JSON；数字（如 `state`）保留为数字，不变成带引号字符串 |
| `dynamiccard.CardEventError` | `Field string`；`Reason string` | `NormalizeCardEvent` 的强类型错误，不携带事件体或密文 |
| `dynamiccard.AckRequest` | `CardID string`、`EventID string`（必填非空白）；`AckData dynamiccard.AckData`（必填）；`ReceivedAt time.Time`（零值表示「刚刚」）；`StrictWindow bool` | `card_id`、`event_id`、`ack_data`；`ReceivedAt` / `StrictWindow` 只用于本地窗口判定，不编码 |
| `dynamiccard.AckData` | `Code int64`；`Mode dynamiccard.AckMode`；`Toast *dynamiccard.AckToast`；`CardUpdate *dynamiccard.CardUpdate` | `ack_data.code` / `mode` / `toast` / `card_update` |
| `dynamiccard.AckMode` | `AckModeSync`（`sync`）/ `AckModeAsync`（`async`） | `ack_data.mode` |
| `dynamiccard.AckToast` | `Level string`（`AckToastLevelSuccess`/`Info`/`Warning`/`Error`，空省略表示 `info`）；`Content string`（必填） | `ack_data.toast.level` / `content` |
| `dynamiccard.CardUpdate` | `UpdateType dynamiccard.UpdateType`；`CardContent *dynamiccard.DynamicCardContent`（full 必填）；`Updates dynamiccard.ComponentList`（element 必填） | `ack_data.card_update`；JSON 编码永不输出 `card_id` / `sequence` |
| `dynamiccard.AckResult` | `Accepted`、`AlreadyProcessed bool`；`CardID`、`EventID`、`Message string`；`Sequence *int64`；`Mode dynamiccard.AckMode`；`AgeMs int64`；`ProceedWithBusiness bool`；`Warnings []string`；`Raw json.RawMessage` | 回执结果；`AlreadyProcessed` 仍为成功，`ProceedWithBusiness` 仅 `async` 成功时为 `true`，`RawJSON()` 为防御性拷贝 |
| `dynamiccard.DynamicCardContent` | `Scheme string`（空或 `"1"`）；`Body dynamiccard.CardBody`；`Footer *dynamiccard.Footer`（可选） | `scheme`（空自动补 `"1"`）、`body`、`footer`（与 `body` 平级） |
| `dynamiccard.CardBody` | `Config *dynamiccard.CardConfig`；`Children dynamiccard.ComponentList` | `config`、`children` |
| `dynamiccard.Footer` | `Config *dynamiccard.CardConfig`；`Children dynamiccard.ComponentList`（`NewFooter`） | `contents.footer`；结构与 `body` 一致，nil 时不输出 |
| `dynamiccard.ActionBarComponent` | `Type`、`ID string`；`Actions []dynamiccard.ActionBarItem`；`Extra map[string]json.RawMessage`（`NewActionBar`） | `type=action_bar`、`id`、`actions`；`id` 非空、`actions` 非 nil，未知字段透传 |
| `dynamiccard.ActionBarItem` | `Key string`；`Platform string`；`EventEnabled *bool`；`BizExtra string`；`Title map[string]string`；`Color`、`URL`、`OpenType string`；`Extra map[string]any` | `key` / `platform` / `event_enabled` / `biz_extra` / `title` / `color` / `url` / `open_type`；未知 key/字段透传，`ai_tag` 必须有 `title` |
| `dynamiccard.CardConfig` | `MaxWidthDesktop *int`；`Spacing *int`（可选） | `max_width_desktop`、`spacing` |
| `dynamiccard.MarkdownComponent` / `TextComponent` | `Type`、`ID`、`Content string`；`Extra map[string]json.RawMessage` | `type`、`id`、`content`，额外字段平铺 |
| `dynamiccard.Markdown`（`MarkdownHelper`） | `AtUser`、`AtAgent`、`AtAll`、`Color`、`Link`、`Image`、`Divider`、`CodeBlock`、`Table`、`ReplyQuote`、`ButtonGroup`、`AgentFlowStream`、`Join` | 纯字符串渲染，均返回 `(string, error)`；非法参数为 `validation` |
| `dynamiccard.AgentFlowOptions` / `AgentFlowBlock` | `ThinkingExpanded *bool`（nil 默认 true）；`Status`（`streaming` / `completed`，空默认 streaming）；`Blocks []AgentFlowBlock`；块 `Kind`（`reasoning` / `tool`）、`Text`、`Name`、`Status`、`Input`、`Output any` | 渲染为 `agent-flow-stream` 围栏结构 |
| `dynamiccard.MarkdownReplyQuoteOptions` | `SenderName`、`BaseMsgID`（必填）；`MsgID2`、`SimType`、`Digest string` | `reply` 围栏属性与摘要 |
| `dynamiccard.MarkdownButton` / `MarkdownButtonGroupOptions` | 按钮 `Label`（必填）、`URL`、`OpenType`、`Interact`、`Query`、`QuerySend`、`QuerySendAt`、`QueryToInput`、`Style`；组 `Layout`、`GapH`、`GapV`、`Radius *float64` | `[::button-group ...]` 块；`Float` 表达显式 0 |
| `dynamiccard.MarkdownImageOptions` | `Alt string`、`Title string`（均可选） | `Image` 的 `![alt](url "title")` 片段 |
| `dynamiccard.ButtonComponent` | `Type`、`ID`、`Text string`；`Style`、`Width`、`Value string`（可选）；`Enabled *bool`；`Action *dynamiccard.ButtonAction` | `type`、`id`、`text`、`style`、`width`、`enabled`、`value`、`action` |
| `dynamiccard.ButtonAction` | `Method string`；`Views []string`（单字符串形式也可解码）；`Value string`；`Silent *bool` | `method`、`views`、`value`、`silent` |
| `dynamiccard.InputComponent` | `Type`、`ID string`；`Value`、`Placeholder string`；`Max`、`Lines *int`；`Enabled *bool` | `type`、`id`、`value`、`placeholder`、`max`、`lines`、`enabled` |
| `dynamiccard.RadioGroupComponent` | `Type`、`ID string`；`Multiple *bool`；`Value *dynamiccard.RadioValue`；`Direction string`；`Enabled *bool`；`Options []dynamiccard.RadioOption` | `type`、`id`、`multiple`、`value`、`direction`、`enabled`、`options` |
| `dynamiccard.RadioOption` | `Text string`（必填）；`Value string`；`Enabled *bool` | `text`、`value`、`enabled` |
| `dynamiccard.RadioValue` | `Single string` 或 `Multiple []string`（同时设置时 `Multiple` 优先） | 单个 JSON 字符串或字符串数组 |
| `dynamiccard.RowComponent` / `ColumnComponent` | `Type`、`ID string`；`Children dynamiccard.ComponentList`；`MainAxis`、`CrossAxis string`；`Spacing *int`；`RowComponent.Wrap *bool` | `type`、`id`、`children`、`main_axis`、`cross_axis`、`spacing`、`wrap`（column 不支持 `wrap`） |
| `dynamiccard.RawComponent` | `Type string`、`ID string`、`Extra map[string]json.RawMessage` | 原样透传；`type`/`id` 优先于同名 extra |
| `dynamiccard.CardResponse` | `Shape dynamiccard.ResponseShape`；`Code`、`Message string`；`Payload`、`Raw json.RawMessage`；`HTTPStatus int`；`LogID string` | 返回值，不直接编码为请求；`RawJSON()` 为防御性拷贝 |
| `dynamiccard.DynamicCardError` | `Code any`；`Category dynamiccard.Category`；`Message string`；`HTTPStatus int`；`Operation dynamiccard.Operation`；`Retryable bool`；`CardID`、`EventID`、`LogID string`；`RequestSequence`、`CurrentSequence *int64`；`CurrentContent *dynamiccard.DynamicCardContent`；`Cause error`；`Raw json.RawMessage` | 统一错误；`Unwrap()` 暴露 `Cause`，`CodeString()` 无损输出 code |
| `dynamiccard.Warning` | `Code string`；`Message string` | `ValidateContent` 的 advisory 结果，不阻塞请求 |
| `dynamiccard.ID` | 内部 `int64`；`NewID`、`ParseID`、`Int64()`、`String()` | 序列化为 JSON 字符串；反序列化接受数字、字符串和 `null` |

`Component`、`ComponentList`、`ValidationOptions`（`SkipSizeCheck` / `SkipDepthCheck`）以及 `dynamiccard.Bool` / `dynamiccard.Int` / `dynamiccard.Float` 均为本地模型与预检辅助，不产生独立请求字段。`dynamiccard.CardEventType`、`dynamiccard.CardEventRoute`、`dynamiccard.CardEventActionFooterOperation`、`dynamiccard.IsCardEvent` 与 `dynamiccard.NormalizeCardEvent(payload, raw)` 是事件接收常量与归一化入口，不直接编码为请求；`Dispatcher.OnCardEvent` 为其强类型注册封装。`dynamiccard.WarningAtMentionTextMissing`（`at_mention_text_missing`）是 AT 正文缺失告警码。`dynamiccard.AckWindowMs`（4000）、`dynamiccard.AckEventTTL`（10 分钟）与 `dynamiccard.WarningAckWindowExceeded`（`ack_window_exceeded_toast_suppressed`）是回执窗口常量与告警码，不产生独立请求字段。

### User 请求与结果类型

| 类型 | 字段（Go 类型；必填/约束） | JSON/query 或返回字段 |
|-|-|-|
| `user.Config` | `Robot2CToken`、`UGateToken`、`Robot2CBaseURL`、`CalendarBaseURL`、`MeetingBaseURL string`（token 可省略，base URL 可覆盖默认值） | 本地 runtime 配置，不直接发送 |
| `user.RuntimeOptions` | `ConnectTimeout`、`RequestTimeout time.Duration`（非负，零取 transport 默认）；`HTTPClient *http.Client`、`Tokens user.TokenProvider`（可选；token provider 提供 `Token(context.Context) (string,error)`） | 本地 runtime 配置 |
| `userim.LatestContactsRequest` | `Limit int`（非负，0 不限制）；`MaxPages int`（非负，0 默认 10）；`IncludeRaw bool` | 本地分页策略；请求固定发送 `timestamp`、`page_start` |
| `userim.Contact` | `ID`、`Type`、`Name string`；`Raw json.RawMessage`（仅 `IncludeRaw=true` 时填充） | 从最近联系人响应解析；无独立请求字段 |
| `userim.LatestContactsResult` | `Contacts []userim.Contact`；`Text string`；`RawPages []json.RawMessage`（可选） | SDK 聚合结果 |
| `userim.CreateSummaryRequest` | `ChatID string`、`ChatType string`（必填；`ChatType` 为 `1`/`2`/`7`）；`StartTime`、`EndTime int64`（必填，毫秒且递增） | body `chatId`、`chatType`、`startTime`、`endTime` |
| `userim.ImportSummaryRequest` | `StartQueryTime`、`EndQueryTime int64`（必填，毫秒且递增）；`TopicType string`（可选，`1`/`2`）；`Intent string`（可选，最多 50 字符）；`UnreadType string`（可选，仅 `1`） | body `startQueryTime`、`endQueryTime`（字符串）、`topicType`、`intent`、`unreadType` |
| `userim.SummaryResult` | `RequestID`、`TaskID string`（可能为空，取决于平台返回层级）；`RawJSON() json.RawMessage` | 从响应的 `requestId`/`request_id`、`taskId`/`task_id` 提取 |
| `usercalendar.CalendarRequest` | `EventID string`（更新/删除/查询必填）；`Body map[string]any`（创建必填；更新可选，SDK 会写入 `eventId`）；`Query url.Values`（当前公开方法不使用，保留兼容字段） | 创建/更新 body 直接使用 `Body`；删除/查询 query `eventId` |
| `usercalendar.FreeBusyRequest` | `StartTime`、`EndTime int64`（必填，毫秒且递增）；`Emails []string`（必填且非空） | JSON `startTime`、`endTime`、`emails` |
| `usercalendar.MeetingListRequest` / `usermeeting.ListRequest` | `BeginTime`、`EndTime int64`（查询会议时 `BeginTime` 必填，`EndTime` 可选）；`TimeZone string`（ListMeetings 必填，数字字符串会转 JSON number）；`PageSize`、`PageNumber int`（ListMeetings 必填且大于 0）；`ShowEnd bool`、`ShowCalendar *bool`、`FilterTypes []string`、`FilterItems []string`（可选） | JSON `beginTime`、`endTime`、`timeZone`、`pageSize`、`showEnd`、`showCalendar`、`pageNumber`、`filterTypes`、`filterItems` |
| `usermeeting.CallRequest` | `Subject string`（必填且非空）；`InviteUsers []string`（必填且非空）；`StartRecord bool`、`StartAIGC bool`（可选开关） | JSON `subject`、`inviteUsers`、`startRecord`、`startAigc` |
| `userauthorization.ListRequest` | `InstanceID`、`Status`、`PageToken string`（可选）；`ST`、`PageSize int`（可选，0 不发送） | query `instanceId`、`status`、`st`、`pageSize`、`pageToken` |
| `userauthorization.GrantRequest` | `InstanceID`、`User string`（必填且非空）；`Permissions []string`（必填且非空）；`ExpiredDays int`（必填且大于 0） | JSON `instanceId`、`user`、`permissions`、`expiredDays` |
| `userauthorization.RevokeRequest` | `InstanceID`、`User string`（必填且非空） | JSON `instanceId`、`user` |
| `userauthorization.Result` | `Data json.RawMessage`（SDK 提取的 data 节点，可能为空）；`RawJSON() json.RawMessage`（完整响应防御性拷贝） | 返回值，不直接编码为请求 |

User Calendar、Meeting 的 `RawResult` alias 的公开字段为 `Data json.RawMessage`；`RawJSON()` 返回完整响应。`map[string]any`、`url.Values`、`json.RawMessage` 均保持开放结构，文档示例应显式构造对应 Go 值，不能把它们写成未定义的匿名类型或把 `json.RawMessage` 当作字符串。

## 最小完整示例

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/dolfly/infoflow"
)

func main() {
	client, err := infoflow.NewClient(infoflow.Config{AppKey: "app-key", AppSecret: "app-secret"})
	if err != nil { log.Fatal(err) }
	r, err := client.Bot.IM.Message.SendToUser(context.Background(), "user-id", "hello")
	if err != nil { log.Fatal(err) }
	fmt.Println(r.MessageKey)
}
```

事件分发、Webhook、WebSocket 的完整方法契约见本文「Bot Events：事件分发」「Bot Webhook」「Bot WebSocket」，错误类型见本文「错误」；验证命令和安全线上 smoke 边界见 [测试指南](TESTING.md)。
