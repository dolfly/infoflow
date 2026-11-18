# 快速开始

构造 Client、发一条消息、收一个事件：

```go
package main

import (
	"context"
	"log"

	infoflow "github.com/dolfly/infoflow"
	"github.com/dolfly/infoflow/bot/events"
	"github.com/dolfly/infoflow/bot/websocket"
)

func main() {
	client, err := infoflow.NewClient(infoflow.Config{
		AppKey:    "your-app-key",
		AppSecret: "your-original-app-secret",
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	// 收事件：handler 注册一次，Webhook 与 WebSocket 两种接收方式共用。
	// 只打印分类字段；消息正文不要写进日志。
	if err := client.Bot.Events.On("private.text", func(ctx context.Context, event events.Event) error {
		log.Println("event received", event.Type, event.MessageType)
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	// 建立接收链路：NewSession 只组装会话，Connect 才真正连接并自动注册回调。
	session, err := client.Bot.WebSocket.NewSession(websocket.SessionOptions{})
	if err != nil {
		log.Fatal(err)
	}
	if err := session.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer session.Disconnect(context.Background())

	// 发消息：token 由 SDK 自动获取并刷新。
	result, err := client.Bot.IM.Message.SendToUser(ctx, "zhangsan", "Hello!")
	if err != nil {
		log.Fatal(err)
	}
	log.Println(result.MessageKey)
}
```

## Webhook 模式

Webhook 模式不建 session：用 `client.Bot.Webhook.NewReceiver` 创建 receiver，把框架解析
前的原始请求体交给 `receiver.Handle`。

```go
receiver, err := client.Bot.Webhook.NewReceiver()
if err != nil {
	log.Fatal(err)
}
// 在 HTTP handler 中：
// err := receiver.Handle(r.Context(), rawBody)
```

## 配置默认值

`Config` 的必填字段只有 `AppKey` 和 `AppSecret`：

| 字段 | 默认值 | 说明 |
|-|-|-|
| `BaseURL` | `https://api.im.baidu.com/api/v1` | 平台 API 地址 |
| 连接超时 | 10 秒 | `HTTPClient` 未注入时生效 |
| 请求超时 | 30 秒 | `HTTPClient` 未注入时生效 |
| token 刷新提前量 | 60 秒 | 自动刷新 |
| 消息发送重试 | 不重试 | L1 消息发送不自动重试 |

注入 `HTTPClient` 时，其 transport、proxy、TLS 由调用方负责。

## 下一步

- [常用能力与限制](usage.md)
- [AI 动态卡](dynamic-card.md)
- [完整 API 参考](API_REFERENCE.md)
