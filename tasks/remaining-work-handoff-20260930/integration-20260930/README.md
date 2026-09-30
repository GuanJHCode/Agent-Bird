# N 系列联合候选集成记录（2026-09-30，主会话执行）

授权：用户在 root 三轮复核全部通过后经问询明确选择「叠加源码 + 提交；stop_timing 外部变体覆盖并留档」。

## 提交内容

- 源码叠加（8 文件，SHA 均与 v3 receipt finalSnapshot 逐字节一致）：
  - 新增 `tools/native-product-bridge/n02_owner_readiness_observer.py`（`96260b2e…`）
  - 新增 `tools/native-product-bridge/test_n02_owner_readiness_observer.py`
  - 修改 `tools/native-product-bridge/test_local_service_chain.py`（`12cce6f7…`，D1 消费改接）
  - 新增 `tools/orchestrator/internal/host/n04_pipe_sensitive_test.go`（`c73c5251…`，R3 组合判据）
  - 覆盖 `tools/orchestrator/internal/host/stop_timing_test.go`（联合版 `1322b337…`＝A 原文逐字）
  - 新增 `tools/orchestrator/internal/host/progress_pipe_hold_test.go`（`5fbe31b2…`，B 保留）
  - 新增 `tools/orchestrator/internal/store/progress_guards_test.go`（`9f58b9e9…`，B 保留 M1/M2）
  - 修改 `tasks/g0-completion/tests/test_owner_helper_production_pipeline.py`（`2d7665cd…`，D1 消费改接）
- 联合候选完整快照 `tasks/remaining-work-handoff-20260930/joint-candidate-20260930/`（679 文件，
  含第 1–3 轮证据、双 manifest、REQUIRED 23、receipt/index v3 链；原位闭包 verify `ok=true`，
  root 2026-09-30 实测）。

## observed 漂移重取（D4：保留不回滚不盲合并，只登记）

- `tools/orchestrator/internal/host/stop_timing_test.go` 外部活跃变体 `12e012a775f508e5…`
  （mtime 2026-09-30 14:47:48，未跟踪）：为 A 的 held-pipe 测试追加 release-leader/stop-holder
  私有控制文件与「身份可能变化后绝不向文件 PID 发信号」的更硬清理逻辑，与 A/联合版约 104 行差异。
  经用户裁定覆盖为联合版，原件逐字节留档于本目录
  `integration-observed-stop_timing-12e012a7.go`，可原样取回；建议后续会话把该强化形状
  作为 N04 的候选改进另行评审（与本批身份集无冲突）。
- 既有四项冻结后漂移**未触碰、未暂存**：`tasks/remaining-work-handoff-20260930/docs/delivery.md`、
  `docs/n06-gate-registration.md`、`docs/task-table.md`、`data/n01-s4-20260930T094654/README-void.txt`。
- 其余 16 个 tracked-modified（host.go `ee6ce551…`＝候选绑定值、probe.go、coordinator、store 等）
  与 91 个外部未跟踪路径**一律未暂存**；host.go 恰与冻结候选值一致故联合测试在原仓树可直接编译。

## 门禁与遗留

- 联合候选门禁（绑定于 joint-candidate 快照）：Go ordinary/race 均 734 = 732P/0F/2S，
  DATA RACE 0；Python 默认面 57P/1S/0F；二进制 `42dc58ac…` 零改动。
- 维持 OPEN（不随集成关闭）：S4 默认并发稳定性、N04 剩余格（15s ticker 上界、
  KindHostProgress IPC 路由、captureWriter 1MiB、UNKNOWN 终末语义）、N05 覆盖面、
  真 Provider/native、Linux 隔离、断电恢复、Spring Boot 生产迁移。

复核链：`/private/tmp/bird-nseries-root-review-20260930-pzpzp52_/docs/root-review-{round2,closeout,round3}-20260930.md`。
