# 测试与验证

本仓库的验证以本地、确定性检查为主：普通单元测试和静态检查不应发起网络请求。

跨仓库回归计划与线上 smoke 属于发布流程，由受控环境执行，不在普通单元测试范围内；
它们不会改变本地检查的通过标准。

## 本地检查

使用 `GOTOOLCHAIN=local`，避免检查过程静默下载其他 toolchain。仓库契约支持
Go 1.25.x 和 1.26.x；准备发布时应使用每个受支持的 toolchain 执行：

```bash
GOTOOLCHAIN=local go test ./...
GOTOOLCHAIN=local go vet ./...
GOTOOLCHAIN=local go test -race ./...
GOTOOLCHAIN=local go test ./... -cover
git diff --check
```

`gofmt -l .` 应无输出。修改 Go 源码时，测试前格式化修改文件：

```bash
gofmt -w path/to/changed.go path/to/changed_test.go
```

根目录的可执行示例也由 `go test ./...` 覆盖：

- `example_test.go:ExampleNewClient` 验证最小 Client 创建路径。
- `example_test.go:ExampleConfigError` 验证 typed configuration error。

## 提交与代码规范

提交变更前先格式化改动的 Go 文件，再执行上面的本地检查，保持 `git diff --check` 无输出，
并在 Go 1.25.x 和 1.26.x 两个受支持工具链上执行。编码风格以 `gofmt` 和仓库现有代码为准。

## 文档

文档改动不影响 Go 测试，但需要确认：

- 相对链接指向存在的文件。
- 示例代码可以编译；完整示例以 `API_REFERENCE.md` 和 `example_test.go` 为准。
- 不写入凭据、token、回调密钥或消息正文。
