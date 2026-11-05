# T08 Claude stdio source-auth 上下文兼容独立审查

VERDICT=PASS

## 检查范围

- 冻结审查树：`/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t08-claude-context-review`；Git root 与 worktree 一致，detached `HEAD=ce4a9c78570240bfa71771e15d8e26fb49c226ba`。
- 任务分支：`work/t08-claude-source-context-20261105`；任务 worktree 为 `/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t08-claude-source-context`，分支 HEAD 同为上述 base，实现尚为 3 个 tracked 修改 + 1 个 untracked 新计划文件。
- 4/4 文件 SHA-256 在测试前后均与 `t08-claude-context-evidence/data/source-manifest.json` 一致；manifest base 与审查 HEAD 一致。
- 审查生产改动、直接调用链及对应测试：`sourceAuthEnvironmentName` 只在 exact-name switch 中排除 `CLAUDE_CODE_SESSION_ID` 和 `CLAUDE_PROJECT_DIR`；其他 `CLAUDE_*`、`ANTHROPIC_*` 和原 OpenAI/Codex、Grok/XAI、AGY/Antigravity、Google/Gemini/Vertex、AWS、Azure/Foundry、XDG/proxy/CA 家族仍进入 length-prefixed SHA-256 投影。
- 核对 proof `version=1`、algorithm ID、constant-time compare、managed owner 验证、workspace/config、token、Host auth guard 与 dispatch 次序均未修改；仓内搜索确认两个 Claude 字段未用于 owner、session、workspace 或 native 身份证明。
- 复核旧产品红测及真实 `.2` control-only 报告：失败差异只新增上述两个名字；18 个公开 sentinel 均继承、8 tools 加载，未请求 user/turn/model。该历史材料只用于定位，不替代本次修复测试。
- `git diff --check` 通过；两个变更 Go 文件 `gofmt -d` 无输出。

## BLOCKERS

无。未发现置信度大于 80% 的认证降级、契约不兼容、测试失真或明显回归。

## NON_BLOCKING

无。

## 独立验证

工具链：Go `go1.26.10 darwin/arm64`；`GOENV=off`、`GOTOOLCHAIN=local`、`GOPROXY=off`、`TMPDIR=/private/tmp`、`GOMAXPROCS=2`，normal/race 使用独立 GOCACHE。未启动实际 worker/model，未读取凭据，未发起 HTTP。

focused 集合包含：managed-source 必需门禁、projection、37 个额外 route/cloud 名字、真实多进程 controller→stdio MCP admission。

1. Normal：

   `env GOENV=off GOTOOLCHAIN=local GOPROXY=off TMPDIR=/private/tmp GOMAXPROCS=2 GOCACHE=/private/tmp/t08c-review-go-normal.VZTJ6R /private/tmp/go1.26.10-sdk/bin/go test -count=1 -v -run '^(TestMCPDispatchRequiresManagedSourceEnvironment|TestSourceAuthEnvironmentProjection|TestSourceAuthEnvironmentAdditionalRoutes|TestMCPManagedSourceEnvironmentAdmission)$' ./cmd/orchestrator`

   结果：136 passed nodes（4 top-level + 132 subtests），0 failed，0 error，0 skipped。子节点分布：projection 10、additional routes 37、multi-process admission 85。

2. Race：

   `env GOENV=off GOTOOLCHAIN=local GOPROXY=off TMPDIR=/private/tmp GOMAXPROCS=2 GOCACHE=/private/tmp/t08c-review-go-race.jqNlV1 /private/tmp/go1.26.10-sdk/bin/go test -race -count=1 -v -run '^(TestMCPDispatchRequiresManagedSourceEnvironment|TestSourceAuthEnvironmentProjection|TestSourceAuthEnvironmentAdditionalRoutes|TestMCPManagedSourceEnvironmentAdmission)$' ./cmd/orchestrator`

   结果：136 passed nodes（4 top-level + 132 subtests），0 failed，0 error，0 skipped，0 race reports；子节点分布同 normal。

关键行为：

- 两个精确 Claude stdio 上下文字段可在 proof 捕获后注入，摘要仍匹配。
- `CLAUDE_CODE_SESSION_ID_FUTURE_AUTH`、`CLAUDE_PROJECT_DIR_FUTURE_AUTH` 仍返回 `mcp_source_environment_changed`，证明未扩大为前缀豁免。
- missing/changed/new/unset、invalid/missing proof、unknown version/algorithm/bad digest 均继续 fail-closed，并返回安全恢复提示。
- 多进程正例只到 `mcp_provider_not_configured`；测试确认未创建 dispatch state，因此未递归启动 CLI、worker 或模型。

## 剩余风险

- 未对本次 4 文件 diff 运行全仓 normal/race、vet、build；现有 `.2` 的 1,624 pass + 8 opt-in skip、vet/build 发生在本修复之前，未作为本次通过依据。
- 按约束未重新调用真实 Claude 2.1.316、未发起 HTTP，也未运行实际 worker/model；官方字段语义采用计划中已引用的文档和已保留的 control-only 运行事实。
- `.3` clean-source 包尚未构建/安装；最终 source/binary/manifest/receipt、8 tools control-only 复验和真实包边界仍需 root 后续验收。
