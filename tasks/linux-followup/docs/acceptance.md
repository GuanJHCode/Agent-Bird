# Linux packaged owner followup

基线 b7ba13b。Linux `plugin-run` 新增 `status/summary/collect/wait-events`，以及精确 `task inspect/status/collect/wait`。按照完整 argv 检查子命令；没有整体放开 `task`。Darwin 原准入规则不变，执行、提交、ACK、接受、返工、恢复、配置仍不从此 Linux 插件入口准入。

既有 handler 与 run-control 验证不变。collect/wait 会记录交付元数据，因此不是只读数据库操作；本变更属于已有任务观察/收集，不会 ACK 或业务接受结果，不启动 Host/Provider。launcher 仍按既有协议私有安装和 exec。显式 state 需同时传给 launcher 与顶层命令，task 命令从原 handle 读取 state：

```sh
codex-orchestrator plugin-run --plugin-root "$PLUGIN" --install-root "$INSTALL" --state-dir "$STATE" -- collect --state-dir "$STATE" --task-id "$TASK" --control-file "$CONTROL"
codex-orchestrator plugin-run --plugin-root "$PLUGIN" --install-root "$INSTALL" --state-dir "$STATE" -- task inspect --handle "$HANDLE"
```

最小验收（Go 1.26.8）：

- RED：原 policy 对新增的 status/summary/collect/wait-events 四个动作均拒绝；记录 `data/policy-red.log`。
- 定向 Go：`go test -json -count=1 -run '^(TestPortableRunPolicy|TestPluginRun|TestLinuxPortableTaskFollowupPolicy)' ./cmd/orchestrator`，4 顶层、16 子例 PASS，0 fail/skip，stderr 空。覆盖精确 task 子命令与执行/变体拒绝。
- Linux arm64 离线 nobody 容器，实际 collect-only 包安装后执行 16 条 CLI。真实 coordinator + 原进程 UDS owner bind + Submit 生成一个 ready 任务；查询/汇总/空收集/1ms 等待、四个 task followup 成功，错误 owner token 拒绝，六种执行类 task 命令在安装前拒绝，重复动作不替换已安装二进制。每个 CLI 原始 Cmd.Wait 完成，coordinator 取消并 drain；容器 exit0/OOM=false 并已删除。raw 与 binary/harness/archive 摘要见 `data/native.log` / `native-receipt.json`。
- 首轮 native 的测试错误地期待无依赖任务保持 queued，真实 scheduler 正确置 ready；仅修正测试预期，生产代码未改。失败 log/receipt 保留 `fixture-state-failed-*`。

未跑全量/race/真实 Provider；本轮没有 pending-result 分页、原 Host 或跨架构验收，不把空队列收集当作这些证明。复用底层已验证收集实现，公共 handler 无修改。独立审查待主会话安排。临时二进制、包和归档完成后清理，保留脚本/原始日志/摘要。
