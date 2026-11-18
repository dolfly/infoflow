# 常见问题

## `NewClient` 返回 `invalid InfoFlow configuration for AppKey: must not be blank`

`AppKey`、`AppSecret` 不能为空白。这是本地校验，与网络无关。

## 需要自己取 token 吗

不需要。SDK 自动获取、缓存并提前 60 秒刷新 app token，并发安全。

## 注册了 handler 却收不到事件

确认注册的事件名与平台下发的事件类型匹配（`private.text`、`group.text`、`*`），并确认
平台回调配置已切到对应模式：

- Webhook：用 `client.Bot.Webhook.Register` 注册。
- WebSocket：`NewSession` 后再调 `session.Connect(ctx)` 才连接并自动注册。

日志只打印 `event.Type`、`event.MessageType` 这类分类字段；正文
（`event.Payload()` / `event.RawPayload()`）、凭据与回调密钥都不要落日志。

## 长连接模式下能收到工作卡交互回调事件吗

能。工作卡交互回调事件在 WebSocket 与 Webhook 两种模式下都能收到；管理后台标注的
「暂不支持工作卡消息」与工作卡交互回调事件无关。

## `go get` 失败或报模块找不到

- 确认 module path 与版本号拼写正确。
- 确认当前 Go 版本不低于 1.25。
- 如果所在环境使用 Go module proxy，确认该模块没有被 proxy 规则拦截；私有模块需要按
  环境配置 `GOPRIVATE` / `GONOPROXY`。
- 公开镜像与主干的 module path 不同；import 路径以实际安装的模块为准，不要混用。

## 能把镜像仓库的改动合并回主干吗

不能。GitHub 镜像是从主干重新生成的派生内容，每次发布都会覆盖；改动请提交到主干，
再通过发布流程重新生成镜像。
