# N03 结论：S1/S3 机制验证与历史归因边界

状态：待独立复核。复用批次三 E1c/E3b 同型受控负载形状；未放宽隔离、未关闭安全服务、未改 2s/5s 预算。

## 1. 事实（本轮实测，身份分开计数）

主运行：`data/n03-s1s3-20260930T101600/report.json`；
> 返修说明(R2/BLOCKER-5)：原 findings 误写 `...T101139`，冻结快照实际为 `...T101600`，此处按实际路径纠正；
> 旧原始证据不改写，另由纠正版 harness 生成新的 `started/finished` 与逐窗 `window_bounds`。
补充运行（测量器返修后风暴窗）：`data/n03-stormcause-20260930T102057/report.json`。

| 身份 | 静默对照 (-count=10) | fresh-exec 风暴 (-count=10) | 风暴失败签名 |
|---|---|---|---|
| S1 `TestProfileRunsThroughPinnedCapabilityProbe` | 10/10 PASS（0.46–0.63s） | **10/10 FAIL 恰 2.00s** | `binary_version_probe_failed`（文案与归档逐字一致） |
| S3 `TestGrokProbeReportsSeparateWriteAuthorization` | 10/10 PASS（0.47–0.74s） | **10/10 FAIL 恰 2.01s** | 同上（独立计数，不并桶） |

风暴形状：8 churn 线程、cap 3200、≤150s（E1c/E3b 同参数）；主窗 churn p50 4076ms/p99 9554ms、
补充窗 p50 7952ms。storm_active 判据满足。

## 2. cause 分解（F6 修复后的新观测面）

`internal/adapter/probe_cause_observation_test.go`（opt-in，默认 Skip）以
`ProbeOutput` 直调 + `errors.Is/As` 分类，对**全新文件首 exec**逐次归档：

- 静默 10/10 全部 `ok`，elapsed ≈ 497–526ms（复现历史 E1b"首 exec≈500ms 常态"独立观测）；
- 风暴 10/10 全部 `classified(deadline=True, exited=True)`：
  **到期中断 + 子进程被杀**（`provider_probe_failed` cause 链含 `context deadline exceeded` 与
  `exec ... signal: killed`），**无一例 start-failure、无一例 cancelled**；
- 负控制：预取消 ctx 必判 `cancelled`（断言，通过）；warm 稳定路径判据 `start_failure→fatal`
  （风暴下 warm 吸收重试记录保留，不误杀）；已交付 4 项 cause-chain 单测同窗 4/4 全过。

结论：S1/S3 风暴失败与 S4 同族——**产品截止（2s probe）在 fresh-exec 评估膨胀下的正确到期中断**；
F6 cause 链使阶段可分解（exec/退出/取消/deadline 四类可区分），H3 归档时代"cause 丢弃不可分解"
的现状描述已改变（源码 `probeFailure.Unwrap`，`docs/capability-index-20260930.md` §3-C2 已登记）。

## 3. 边界（不得夸大）

- 同签名受控复现成立 ≠ 历史 r3/r5/r6 现场已证同因：无当时分段记录，**历史归因维持未证**（沿用 R4 口径）。
- 静默 NC 全绿不关闭默认并发稳定性；S1/S3 与 S4 一样在默认并发下仍是刀刃（N01 findings §2）。
- 测量器一次缺陷如实登记：主运行风暴窗的 cause 观测因"warm 控制在风暴下本身是冷首 exec"
  而中断（0 样本，`measurer_gaps` 记录 in-driver），返修为"重试吸收 + 非致命记录"后单独补窗重跑；
  两次运行目录均保留。

## 4. 复验命令

```sh
python3 -B tasks/remaining-work-handoff-20260930/scripts/n03_s1_s3_storm.py <新绝对目录>
python3 -B tasks/remaining-work-handoff-20260930/scripts/n03_storm_cause_addendum.py <新绝对目录>
ORCHESTRATOR_PROBE_CAUSE_OBSERVATION=1 go test -count=1 ./internal/adapter -run '^TestProbeCauseDecompositionObservation$' -v
```
