# 分层验证

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
python3 -m pytest -p no:cacheprovider tools/native-product-bridge tools/real-user-trial \
  tasks/brain-control-hardening/scripts/test_package_skills.py \
  tasks/provider-runtime-completion/scripts/test_runtime_layout.py -q -rs
```

需要 pytest 和 PyYAML。打包测试：

```sh
export G4_TEST_PYTHON="$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.executable).resolve())')"
sh tasks/g1-g4-delivery/scripts/test-package-executable-modes.sh
```

实际 install/doctor 演示可运行 `tasks/provider-runtime-completion/scripts/verify_install.py <built-binary> <new-empty-task-dir>`。它不注册登录服务、不更改 Provider 账号。

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
