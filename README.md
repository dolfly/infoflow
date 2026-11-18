# InfoFlow Go SDK

InfoFlow OpenAPI 的 Go 客户端。SDK 负责 app token 的获取与刷新、请求鉴权、响应信封解码和
类型化错误；业务代码面向 `infoflow.NewClient` 一个门面，不必自己处理原始 HTTP 协议、
鉴权头和 token 缓存。

- Go 1.25 或更高版本
- 仅依赖标准库
- 当前版本：`v0.2.2`

## 安装

```bash
go get github.com/dolfly/infoflow@v0.2.2
```

也可在 `go.mod` 中写 `require github.com/dolfly/infoflow v0.2.2`
后执行 `go mod tidy`。版本选择与安装校验见 [docs/install.md](docs/install.md)。

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

空配置必然返回 `invalid InfoFlow configuration for AppKey: must not be blank`；`NewClient`
在发起任何网络请求之前完成校验，不会触达线上 API。仓库内等价示例见
`example_test.go:ExampleConfigError`。

## 文档

| 主题 | 文档 |
|-|-|
| 安装与版本选择 | [docs/install.md](docs/install.md) |
| 快速开始：建 Client、发消息、收事件 | [docs/quickstart.md](docs/quickstart.md) |
| 常用能力与限制 | [docs/usage.md](docs/usage.md) |
| AI 动态卡 | [docs/dynamic-card.md](docs/dynamic-card.md) |
| 完整 API 参考 | [docs/API_REFERENCE.md](docs/API_REFERENCE.md) |
| 常见问题 | [docs/faq.md](docs/faq.md) |
| 测试与验证 | [docs/TESTING.md](docs/TESTING.md) |
| 公开行为契约 | [CONTRACT.md](CONTRACT.md) |
| 运行时结构 | [ARCHITECTURE.md](ARCHITECTURE.md) |
| 变更历史 | [CHANGELOG.md](CHANGELOG.md) |

文档站：<https://dolfly.github.io/infoflow/>（GitHub Pages 启用后生效）。

## GitHub 镜像

GitHub 镜像 `github.com/dolfly/infoflow` 的 module path 由 `scripts/upstream_push` 在发布时
从主干自动改写生成；镜像内容不要手工修改，也不要合并回主干。

## 开发

```bash
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
```

仓库契约支持 Go 1.25.x 和 1.26.x；完整验证说明见 [docs/TESTING.md](docs/TESTING.md)。
