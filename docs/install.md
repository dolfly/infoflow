# 安装与版本选择

## 环境要求

- Go 1.25 或更高版本（`go.mod` 声明 `go 1.25`）。
- 能访问模块所在的代码托管地址。

## 安装

```bash
go get github.com/dolfly/infoflow@v0.2.2
```

也可以先写进 `go.mod`：

```text
require github.com/dolfly/infoflow v0.2.2
```

再执行 `go mod tidy` 让工具链解析。

SDK 以 semver tag 发布，没有额外的打包或上传步骤。如果所在环境使用 Go module proxy
或私有模块规则，请按环境配置 `GOPROXY` / `GOPRIVATE`。

## 版本选择

`<module>` 指 `github.com/dolfly/infoflow`。

| 渠道 | 版本形式 | 安装命令 | 说明 |
|-|-|-|-|
| 正式版 | `v0.2.2` | `go get <module>@v0.2.2` | 当前正式版；`@latest` 会解析到它 |
| Beta | `v0.2.3-beta.1`（形态示例） | `go get <module>@v0.2.3-beta.1` | 必须显式指定；存在正式版 tag 时 `@latest` 不会选中 |

tag 不可重写，修复须发新版本；主版本 `v2` 起 module path 必须带 `/v2` 后缀。

## 安装校验

在临时模块中执行：

```bash
mkdir /tmp/infoflow-check && cd /tmp/infoflow-check
go mod init scratch
go get github.com/dolfly/infoflow@v0.2.2
```

写入下面的 `main.go` 并执行 `go run .`：

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

能打印
`invalid InfoFlow configuration for AppKey: must not be blank`，说明模块已解析、编译并
运行通过；`NewClient` 在发起任何网络请求之前完成校验，不会触达线上 API。

仓库内的等价示例见 `example_test.go:ExampleConfigError`，也可以用
`go test ./... -run ExampleConfigError -v` 验证。

## GitHub 镜像

公开镜像的 module path 是 `github.com/dolfly/infoflow`，由发布工具从主干自动改写生成：

```bash
go get github.com/dolfly/infoflow@v0.2.2
```

镜像内容是派生结果，不要手工修改，也不要合并回主干。
