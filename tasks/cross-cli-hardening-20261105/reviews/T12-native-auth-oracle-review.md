# T12 native auth oracle 与文档窄审

VERDICT=PASS

## 检查范围

- Git root 为冻结 snapshot `/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t12-native-oracle-review`；HEAD `d700356c9ee497952b22ec9220d1fde111f71848`、tree `5b04a4d7fb49c47a5f16ebe26a04ddc3a1ee509a`，处于 detached HEAD。最终工作区只有 4 个目标文件变化。
- 最新 `native-oracle-source-manifest-r2.json` SHA-256 为 `ef538e0b9e0c162597c7cea31756badbf4174f7db0bcd9394bba8b5a8863f4a3`，4/4 文件 hash 匹配；未使用旧三文件 manifest。
- 最终 diff 仅含 `tools/orchestrator/internal/host/claude_native_test.go`、`docs/testing.md`、`docs/installation.md` 和任务计划；没有 production Go、插件、runtime 或 `.3` 包字节变化。`git diff --check` PASS，`gofmt -d` 无输出。
- 固定 Claude binary 为 2.1.316，SHA-256 `64c327e92b2c5e0f78c0a980c5a518121e1f0d140ca771ca5dd4488d1a2dcb3f`。
- 审查 test oracle 的认证选择、header/model/tool/Write/freeze 守卫，并在隔离环境分别运行 token 与 API-key 两种 native loopback 模式。
- 核对文档删除的两个 Python 测试路径和 `verify_install.py` 确实不存在，保留的两个 Python 目录及 package-mode 脚本确实存在；核对安装文档要求 owner-only `0700` 新目录和 `tar -xzpf`，没有建议更改全局 `umask` 或放宽产品 guard。

## BLOCKERS

无。测试 oracle 的修复精确绑定到既有 `apiKey` 分支：先清空两个 Anthropic 认证变量，再只设置选定的一个。未改变产品实现，原有安全和行为断言完整保留，且两个认证模式均通过固定 CLI 的实际 owned-loopback 测试。

## NON_BLOCKING

- LOW — `docs/installation.md:3`、`docs/testing.md:3`：既有状态文字仍称 PKG-01～04/最终包验收待完成，但最终 `.3` 包已通过独审。风险是读者把已完成的包验收与仍未执行的 live-provider/TUI 验收混为一谈。建议最终文档同步时只更新包状态，同时保留 live-provider 仍未完成的说明；这不影响本次 oracle 或 `.3` 产品字节。

## 独立测试

两次命令均使用 Go 1.26.10、独立 `GOCACHE`、`TMPDIR=/private/tmp`、`GOPROXY=off` 和 `env -i`：

```sh
env -i HOME=<review-home> USER=review LOGNAME=review PATH=/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin \
  GOENV=off GOTOOLCHAIN=local GOPROXY=off GOMODCACHE=/Users/guanjunhui/go/pkg/mod \
  GOCACHE=<token-cache> TMPDIR=/private/tmp GOMAXPROCS=2 \
  ORCHESTRATOR_CLAUDE_NATIVE_BINARY=<claude-2.1.316> \
  /private/tmp/go1.26.10-sdk/bin/go test -count=1 -v \
  -run '^(TestNativeClaudeGatewayReadOnlyTransport|TestNativeClaudeGatewayManagedWriteTransport)$' ./internal/host

env -i HOME=<review-home> USER=review LOGNAME=review PATH=/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin \
  GOENV=off GOTOOLCHAIN=local GOPROXY=off GOMODCACHE=/Users/guanjunhui/go/pkg/mod \
  GOCACHE=<api-key-cache> TMPDIR=/private/tmp GOMAXPROCS=2 \
  ORCHESTRATOR_CLAUDE_NATIVE_BINARY=<claude-2.1.316> ORCHESTRATOR_CLAUDE_NATIVE_AUTH=api-key \
  /private/tmp/go1.26.10-sdk/bin/go test -count=1 -v \
  -run '^(TestNativeClaudeGatewayReadOnlyTransport|TestNativeClaudeGatewayManagedWriteTransport)$' ./internal/host
```

精确结果：

| 模式 | top-level passed | subtests | failed | errors | skipped |
| --- | ---: | ---: | ---: | ---: | ---: |
| token | 2 | 0 | 0 | 0 | 0 |
| API-key | 2 | 0 | 0 | 0 | 0 |
| 合计 | 4 | 0 | 0 | 0 | 0 |

旧失败证据 `native-claude-transport.jsonl`（SHA-256 `6ce0fa44cf43e7f376f139b983760d174f677cd1bbc80abb9990bc1471c6815a`）显示 API-key 旧产品下 readonly PASS、managed-write FAIL、package FAIL；修后同一固定 CLI 的 API-key 两个测试均 PASS。token 两个测试也均 PASS。

测试仅使用 fixture 内生成的 `SYNTHETIC_GATEWAY_CREDENTIAL` 和 `httptest` owned loopback；没有读取用户 source/session/credential，没有远端 HTTP、真实 Provider、model 或递归 CLI。

## 原 guard 保留情况

- 精确 Authorization/X-Api-Key：`claude_native_test.go:85-90`。
- 请求 model：`:93-106`。
- Read/Write 和 forbidden tool 边界：`:108-113`。
- OAuth/cloud auth 清空：`:215-217`。
- candidate freeze artifact：`:227-229`。
- Write 文件内容与 transport 完整性：`:231-238`。
- 本次唯一逻辑差异为 managed fixture 的 selected-auth 初始化：`:206-214`。

## 剩余风险

- 按任务允许选择 normal 模式，未运行 `-race`；这次变化是非并发测试环境选择。
- 未因四文件 test/docs delta 独立重跑 full Go/Python；复用 root 的 post-change full normal/race 与 301 passed/7 native opt-in skipped 证据，不将其冒充本审查执行。
- 未执行真实 credential、远端 Provider/model、native TUI 或付费 live 验收；本结论只覆盖测试 oracle 与文档修正。
