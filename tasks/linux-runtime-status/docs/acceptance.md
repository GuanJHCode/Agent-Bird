# Linux plugin-run runtime-status

2026-10-12，基线 `51df862`，仅 Linux portable admission 增加 `runtime-status`；Darwin allowlist 和其他平台 guard 不变。复用 `InstallPortableCollectOnly`，`runtime_status.go` 无修改：SQLite `mode=ro`、1 秒查询 deadline、汇总 metadata；不启动 Provider/Coordinator、不迁移数据库，不构成升级锁。

launcher 仍进行受控私有安装；只读承诺仅指状态查询。显式状态路径必须分别传给 launcher 和实际 command：

```sh
codex-orchestrator plugin-run --plugin-root "$PLUGIN" --install-root "$INSTALL" --state-dir "$STATE" -- runtime-status --state-dir "$STATE" --run-id existing
```

验收使用 Go `1.26.8 darwin/arm64`，`TMPDIR=/private/tmp`：

- TDD RED：`go test -count=1 ./cmd/orchestrator -run '^TestPortableRunPolicy$' -v`。新增 Linux runtime-status 因 `portable_action_unsupported` 失败；其余 8 个 policy 子用例通过，见 [raw](../data/policy-red.log)。
- GREEN：`go test -count=1 ./cmd/orchestrator -run '^(TestPortableRunPolicy|TestPluginRun.*|TestRuntimeStatus.*)$' -v`。5 个顶层测试、9 个 policy 子用例全部通过，0 fail/error/skip，包含既有 run-id observation 和缺失数据库不创建状态测试，见 [raw](../data/focused-green.log)。
- Linux arm64 binary 与独立 task harness 各 cross-build 一次；使用既有 `package-plugin.sh --kind collect-only-hooks` 打包一次。缓存 image `sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99`，`--pull=never`，无网络、只读 root、无 capability、nobody 用户、私有临时安装。输入 binary/archive/harness hash、portable manifest 与 Docker create 命令见 [receipt](../data/native-receipt.json)。
- 成功 native 验收 9 条真实 CLI 命令：check 私有安装；从已安装 binary exec 的 runtime-status 返回预期 JSON（ready task/reserved host 各 1、exact run/task/attempt 各 1）；DB SHA256 前后同为 `7f0a86fc3bc118c1b8772cfdf1dbb0f7fc2ccd00fd6c63a432fd9cdbe45076b6`，WAL/SHM 前后均不存在；缺失 DB 返回 `runtime_unavailable` 且 state 不创建；provider/config-apply/collect/controller/serve/unknown 6 种 action 在安装前拒绝，install/state 均不创建。见 [raw](../data/native.log)。每条命令等待原始 child；容器自然退出 0、OOMKilled=false、Pid=0，删除后 inspect=1。
- 首次 native 运行执行 2 条 CLI，check 安装成功，但 Docker tmpfs 默认 noexec 使已安装 binary exec 返回 permission denied。仅修正验收 mount 显式 `exec,nosuid,nodev`，未改生产代码。失败 raw/receipt 保留为 `failed-1-*`；两次容器均已删除。

本次验证命令计数：2 次 focused Go test、2 次 cross-build、1 次打包、2 次 native 容器验收；容器内共 11 条 CLI（成功轮 9、失败轮 2）。未运行全量/race、native Provider、真实用户安装修改、网络或 pull；合成 fixture 不证明正在写入的 WAL 场景或其他 Linux 架构/Provider 支持。独立审查由主会话安排，尚未在本报告宣称通过。

保留脚本、raw 日志和 receipt；已清理本任务生成的 binary、harness、package 及 tar 临时产物，输入摘要保留在 receipt 中。重跑需重新构建/打包，并将证据放入新 task 目录，脚本拒绝覆盖既有证据。
