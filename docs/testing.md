# 分层验证

2026-11-09 当前私有 `.5` 的身份与证据见[交付范围](../tasks/cross-cli-hardening-20261105/docs/delivery-scope.md#交付身份与验收边界)。Go normal/race 各1,659pass（659顶层、1,000子测试）、0fail、8个既有opt-in skip，vet/全模块build通过；首次normal的1个AGY 2秒probe_error失败原样保留，随后5次focused及fresh normal/race均通过，未改超时。149个生产材料与包源码逐文件一致。见[.5回归记录](../tasks/cross-cli-hardening-20261105/data/public-evidence/T12-regression-r5-root.json)、[源码绑定](../tasks/cross-cli-hardening-20261105/data/public-evidence/T12-source-binding-r5.json)及[REG独立审查](../tasks/cross-cli-hardening-20261105/reviews/T12-regression-r5-review.md)；上述运行已通过，REG独审与root签收已完成。

同一 `.5` 实际安装binary的Python回归301pass/7native opt-in skip/0fail/0error；JUnit SHA256 `4947c51c3ba81c88fa3259d4b64773e1a6d0871308757374cb41e1160722a00f`。Codex/Claude metadata各8工具、完整source proof相等、无owned残留，OS禁止联网；Claude normal模式检查7个managed路径/2个defaults域，未发送模型turn。PKG-01～04独审/root通过，单次build/package均成功且0失败，见[.5包记录](../tasks/cross-cli-hardening-20261105/data/public-evidence/T11-package-r5-root.json)和[包审查](../tasks/cross-cli-hardening-20261105/reviews/T11-package-r5-review.md)。这些是离线/metadata层证据：历史 `.4`、`.3` 及其失败保留；新 `.5` 真实模型LIVE=NOT_RUN，core/T12/T13 product与native push保持BLOCKED，LIVE-03=NOT_EXERCISED。Claude保留cli-default及来源模型ENV，Codex原试验使用gpt-6-luna/low。

## 1. 协议、状态与安全边界

在 `tools/orchestrator`：

```sh
go test -count=1 ./...
go test -race -count=1 ./...
go build ./...
go vet ./...
```

覆盖协议分片/大小/会话/终态顺序、owner peer、durable ACK、DAG 与预算、unknown 资源保留、版本化迁移和事务回滚。Host 的 profile 测试执行真实 macOS sandbox 和 Git linked worktree，不调用模型。

## 2. 多进程 CLI、Python 与打包

Go 的 CLI 测试会构建并运行真实 orchestrator/Host/Worker 进程；其中 Worker 是明确标记的 fixture。运行测试期间不要同时修改 Go 源码，因为套件会在中途再次调用 `go build`。

从仓库根目录：

```sh
python3 -m pytest -p no:cacheprovider tools/native-product-bridge tools/real-user-trial -q -rs
```

需要 pytest 和 PyYAML。打包测试：

```sh
export G4_TEST_PYTHON="$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.executable).resolve())')"
sh tasks/g1-g4-delivery/scripts/test-package-executable-modes.sh
```

实际 install/doctor 使用[安装说明](installation.md)中对应包的入口。集成 bridge 测试需显式设置 `ORCHESTRATOR_PRODUCT_BINARY` 为同一候选包实际安装的 Go 二进制；缺失时相关测试会 skip，不能当作安装验收。macOS 的 AF_UNIX 路径有长度限制，测试使用独立的短 `TMPDIR`/pytest `--basetemp`，保留原安装安全模式。测试不注册登录服务、不更改 Provider 账号。

历史 `tasks/g0-*` 套件按各自合同分别运行；一些依赖固定解释器、websockets 16.0、指定的实验材料与验收目录，不等于默认核心套件。`run_runtime_tests.py` 是扩大检查的复现入口，失败会原样返回非零，不删除或弱化旧安全门槛。

## 3. 实际 Provider

从 `tools/orchestrator` 显式运行（会使用当前 Claude 账号和模型额度）：

```sh
ORCHESTRATOR_REAL_CLAUDE=1 go test -count=1 ./cmd/orchestrator \
  -run TestRealClaudeReviewerProfile -v
```

默认 CI 跳过此测试。此测试校验真实二进制版本/摘要、profile 启动、结构化最终结果和标记，未覆盖模型编辑任务或原生 TUI。

## 4. 性能与原生验收

CI 的 macOS 镜像会[在全局与系统级安装 Git LFS](https://github.com/actions/runner-images/blob/main/images/macos/scripts/build/install-git.sh)。候选开发测试拒绝任何 Git 执行扩展，因此工作流仅在一次性 runner 中移除预置 `filter.lfs` 配置，再运行原有正向与拒绝测试。该步骤不修改产品 guard，也不是供用户关闭 Git 扩展的安装命令。

```sh
go test ./internal/events -run '^$' -bench BenchmarkPendingSpool -benchmem -benchtime=300ms -count=3
go test ./internal/store -run '^$' -bench BenchmarkQuotaAdmission -benchmem -benchtime=300ms -count=3
```

设置过程不计时；基准固定 1000 条 spool 事件（990 ACK）、5000 条数据库事件。比较相同机器与参数，不能把某个快查询的收益当成整个调度器的加速比例。

真实 native owner attachment、TUI 可见结果、history proof 和清理验收仍按 `tasks/g1-g4-delivery` / `tools/real-user-trial` 的 opt-in 合同执行。本轮未执行这些原生 TUI 验收。
