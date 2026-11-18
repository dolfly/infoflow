# InfoFlow Go SDK

InfoFlow OpenAPI 的 Go 客户端。SDK 负责 app token 的获取与刷新、请求鉴权、响应信封
解码和类型化错误；业务代码面向 `infoflow.NewClient` 一个门面，不必自己处理原始 HTTP
协议、鉴权头和 token 缓存。

## 能力概览

- **认证**：自动获取、缓存并提前 60 秒刷新 app token，并发安全。
- **错误模型**：统一 envelope 解码，提供 `ConfigError`、`AuthError`、`HTTPError`、
  `BusinessError`、`MalformedResponseError`，可用 `errors.As` 判断。
- **Bot IM**：文本、图片、Markdown、富文本、图文、文件上传下载、流式卡片、AI 动态卡。
- **事件接收**：Webhook 与 WebSocket 共用事件 dispatcher，支持通配符事件。
- **身份与日程**：用户身份、机器人日历与会议、消息总结。
- **零第三方依赖**：只使用 Go 标准库。

## 环境要求

Go 1.25 或更高版本（`go.mod` 声明 `go 1.25`）。

## 安装

```bash
go get github.com/dolfly/infoflow@v0.2.2
```

版本选择、`go.mod` 写法和安装校验见[安装与版本选择](install.md)。

## 最小示例

```go
package main

import (
	"fmt"

	infoflow "github.com/dolfly/infoflow"
)

func main() {
	_, err := infoflow.NewClient(infoflow.Config{})
	fmt.Println(err)
}
```

空配置必然返回
`invalid InfoFlow configuration for AppKey: must not be blank`；`NewClient` 在发起任何
网络请求之前完成校验。

## 文档

| 主题 | 文档 |
|-|-|
| 安装与版本选择 | [install.md](install.md) |
| 快速开始：建 Client、发消息、收事件 | [quickstart.md](quickstart.md) |
| 常用能力与限制 | [usage.md](usage.md) |
| AI 动态卡 | [dynamic-card.md](dynamic-card.md) |
| 完整 API 参考 | [API_REFERENCE.md](API_REFERENCE.md) |
| 常见问题 | [faq.md](faq.md) |
| 测试与验证 | [TESTING.md](TESTING.md) |

## 设计文档

- [CONTRACT.md](https://github.com/dolfly/infoflow/blob/main/CONTRACT.md)：公开行为契约。
- [ARCHITECTURE.md](https://github.com/dolfly/infoflow/blob/main/ARCHITECTURE.md)：运行时结构。
- [CHANGELOG.md](https://github.com/dolfly/infoflow/blob/main/CHANGELOG.md)：变更历史。
- [spec/realtime.md](https://github.com/dolfly/infoflow/blob/main/spec/realtime.md)：实时事件矩阵。
