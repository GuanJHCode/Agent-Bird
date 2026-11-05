# T11 installed manifest 位置绑定独立审查

VERDICT=PASS

## 检查范围

- 冻结审查树：`/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t11-location-review`，Git root 与 worktree 一致；detached `HEAD=ba6629867a21be0c14fd36665b7c125845c12414`。
- 任务分支：`work/t11-location-binding-20261105`，任务 worktree 为 `/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t11-location-binding`，分支 HEAD 同为 `ba6629867a21be0c14fd36665b7c125845c12414`；实现尚为工作区 diff。
- 最终候选范围：12 个 tracked 修改 + 2 个 untracked 新文件，共 14 个文件；14/14 SHA-256 在测试前后均与 `t11-location-evidence/data/source-manifest.json` 一致，manifest base 亦为上述 base。
- 审查了 installed manifest schema2 的生成与绝对 `installed_root` 绑定、schema1 空位置兼容、Doctor/Pin/CodexGuard/卸载读取、quarantine 合法恢复、v2 原 manifest bytes + typed manifest 一致性、legacy v1 精确 `MarshalIndent` 重建、原 receipt 删除前 version/hash 校验、原 FD/inode 删除协议未改动，以及 Go nativebridgecli、packaged Python bridge、Python entry plan 三个实际入站消费者。
- 全仓搜索 installed `manifest.json`/`schema_version`/`readManifest`/`validateLaunch`/`validate_package` 的生产消费者，未发现遗漏的受影响入站读取路径；portable/runtime manifest、MCP configure schema、SQLite 均未被本 diff 升级。
- `git diff --check` 通过；5 个变更 Go 文件 `gofmt -d` 无输出。

## BLOCKERS

无。实现满足本次位置绑定合同，未发现置信度大于 80% 的需求不满足、行为错误、测试失真或明显回归。

## NON_BLOCKING

- LOW — `plugins/codex-orchestrator/**/__pycache__/`、`tasks/**/__pycache__/`、`tools/native-product-bridge/__pycache__/`：任务分支 worktree 当前存在多组未跟踪 Python cache；它们不在 14 文件冻结审查快照中，也未影响本次测试。风险是后续使用宽泛 stage/package 操作时误纳入。建议 root 集成时只 stage source manifest 列出的 14 个文件，并从 clean source 构建 `.2` 候选。

## 独立验证

工具链：Go `go1.26.10 darwin/arm64`；Python `3.14.3`。所有测试使用 `/private/tmp`，Go normal/race 分别使用独立 GOCACHE，pytest 使用短独立 basetemp；未接触凭据、模型、HTTP 或全局安装。

1. Go normal：

   `env GOENV=off GOTOOLCHAIN=local GOPROXY=off TMPDIR=/private/tmp GOMAXPROCS=2 GOCACHE=/private/tmp/t11-review-go-normal.e4QoXm /private/tmp/go1.26.10-sdk/bin/go test -count=1 -v ./internal/install ./internal/nativebridgecli`

   结果：67 passed nodes（38 top-level + 29 subtests），0 failed，0 error，0 skipped；两包均 PASS。

2. Go race：

   `env GOENV=off GOTOOLCHAIN=local GOPROXY=off TMPDIR=/private/tmp GOMAXPROCS=2 GOCACHE=/private/tmp/t11-review-go-race.tzAyCQ /private/tmp/go1.26.10-sdk/bin/go test -race -count=1 -v ./internal/install ./internal/nativebridgecli`

   结果：67 passed nodes（38 top-level + 29 subtests），0 failed，0 error，0 skipped；两包均 PASS，无 race 报告。

3. Python 新位置矩阵 focused：

   `env TMPDIR=/private/tmp PYTHONDONTWRITEBYTECODE=1 /private/tmp/agent-bird-cross-cli-5c9jyn8o/python-venv/bin/python -m pytest -q --basetemp=/private/tmp/t11-review-py.Q6x68C/b tools/native-product-bridge/test_entrypoint.py::test_packaged_entrypoint_requires_pinned_interpreter_package_and_g0_runtime tools/native-product-bridge/test_private_product_plan.py::test_distribution_plan_accepts_only_manifest_bound_runtime`

   结果：18 passed parameterized nodes，0 failed，0 error，0 skipped。

4. Python 两个受影响测试文件全量：

   `env TMPDIR=/private/tmp PYTHONDONTWRITEBYTECODE=1 /private/tmp/agent-bird-cross-cli-5c9jyn8o/python-venv/bin/python -m pytest -q -p no:cacheprovider --basetemp=/private/tmp/t11-review-py.Q6x68C/full tools/native-product-bridge/test_entrypoint.py tools/native-product-bridge/test_private_product_plan.py`

   结果：24 passed item nodes（18 个位置参数化 case + 6 个非参数化 item），0 failed，0 error，0 skipped；1 条既有 `native_product_case.py:1348` SyntaxWarning，不影响结果且非本 diff 引入。

## 剩余风险

- 未运行整个 repository 的 Go/Python 全量回归、build 或 vet；本审查按任务要求覆盖两个受影响 Go package 和两个受影响 Python 测试文件。
- 未重放真实私有 `.1` 包，也未构建/安装 `.2` 候选；PKG-01～04、clean-source 打包及实际候选 receipt 绑定仍需 root 后续验收。
- 按约束未执行 native TUI、真实模型、HTTP、凭据或全局安装检查；本 PASS 仅绑定上述 14 文件冻结源码与确定性本地验证。
