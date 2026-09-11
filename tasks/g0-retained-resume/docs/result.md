# 退出停止、保留现场与明确续接：实施结果

日期：2026-09-11。状态：本轮生命周期调整已在 Go 合成工具中实现，三次正常退出及一次强杀后的原生续接全部完成并通过独立复核；完整 G0 尚未放行。

用户已授权将退出策略改为停止执行并保留记录，恢复原主会话后明确发起继续。普通 `codex` 入口、账号权限、原始七份设计文档及其他验收要求保持；[本轮合同](../plans/contract.md) 覆盖旧 C06/C07/C08 的相关生命周期要求。原生 Goal 不参与本轮恢复。

## 已实现

[Go 工具](../../../tools/g0-return-lab/RETAINED.md) 增加 `retained-start`、`retained-inspect`、`retained-resume`，旧的 detached Host 和 ACK 命令兼容。

- 固定合成步骤以前台单进程执行，持有同一稳定文件锁；在已核验直系父的运行路径中，父进程变化会触发停止。
- 保留明确的 task/owner/revision/segment、持久化步骤产物与进度缓存。inspect 只读，不因记录写着 running 就宣称进程存活。
- 明确 resume 在同一锁内验证 owner、任务版本和上次 segment，再对账已提交产物。新 segment 继续未完成步骤，完成态重试不重复产生效果，旧 CAS 拒绝。
- 产物是权威检查点；进度缓存可以因强杀落后，但已提交的步骤不会被覆盖重做。父退出与一次原子提交之间的竞争按保留下来的产物处理。
- 信号、父退出和 stdout 背压都进入停止路径；retained 最终错误输出也有界，避免错误流本身堵住进程退出。

该工具是 G0 的固定步骤夹具，没有真实 Claude/AGY/Grok adapter、业务代码执行、数据库、调度器或安装器。检查点续接不等于旧进程内存或旧工具 future 恢复，直接父守卫也不证明共享服务或任意执行树的当前 TUI 附着。

## 证据与验证

无守卫的原生前台探针已证实：强杀父 Codex 后，子进程可 reparent 并在约 85.41 秒后仍写完成结果。因此只把进程改成前台不足，父退出守卫是由失败证据推动的修复。[探针结果与被作废的观测误判](native-probe-results.md)

实现由 `retained_host_impl` 在独立 worktree 完成；独立 reviewer 复现了正常硬链接发布被误拒、最终 stderr 堵塞退出两项问题，均已补测试修复并独立转绿。14 个 retained 顶层测试加上旧 baseline/ACK 测试覆盖真实文件、信号、锁、并发和输出背压。[实现验证](host-validation.md)、[复核闭环](review.md)

主 worktree 最后源码完整 race（含真实 CLI 子进程）为 `32.527s`、exit 0；vet exit 0。源码、保留二进制与独立复核候选一致，原七份设计文件摘要不变。[新鲜整合验证](main-verification.json)

原生实测使用普通 Codex CLI 0.153.4 和同一已审查二进制。每个合格样本都在已持久化 step 1、确认进程父链与任务锁之后退出；普通无 prompt 恢复同一 thread，静默观察后才输入一次真实继续请求。

| 样本 | 退出至原执行停止、锁释放 | 恢复后静默 | 明确继续至 ACK | 最终结果 |
|---|---:|---:|---:|---|
| 正常退出 quit02 | 5.324547 秒 | 10.128495 秒 | 105.535 秒 | segment 2，20/20 |
| 正常退出 quit03 | 3.544911 秒 | 10.050900 秒 | 70.083 秒 | segment 2，12/12 |
| 正常退出 quit04 | 5.731129 秒 | 10.096726 秒 | 70.780 秒 | segment 2，12/12 |
| 强杀父进程 kill01 | 0.060753 秒 | 10.037441 秒 | 67.672 秒 | segment 2，12/12 |

四次均保留旧步骤的字节摘要，最终 effect_count 等于总步数；打开会话没有触发新模型轮或进展变化，明确继续才启动新 segment。真实完成快照来自原主会话执行 `retained-inspect` 的 `custom_tool_call_output`。未识别的 pragma join 仍只保存摘要，没有据此认定任务完成。[原生时间线](native-results.md)、[检查汇总](native-final-checks.json)、[主 Agent 核验](main-native-verification.json)

quit01 因观察流程错过在途阶段、任务在退出前已完成而排除，只用新的 quit04 替代；无守卫强杀探针仍是 FAIL。最终证据由 `native_identity_review` 独立复核 PASS，无未解决问题。最终 Python 采集与观察器回归 15/15 通过；[收尾检查](final-verification.json) 记录最后文档整合后的测试、摘要和链接核验。

## 交付与剩余范围

源码已整合：`57edba1`、`79c140e`，验证文档 `748e542`；前置探针 `3fb1c99`；正式原生证据 `ba03e55`、`908e2e8`，原生报告与清理 `8490166`。独立复核的 macOS arm64 二进制保留在本任务 `data/g0-return-lab-retained-darwin-arm64`，SHA-256 为 `feb9b9ad969abca5ff35db28c0f0d8d920dc0778d7aac68a90952900e989a285`。

本轮 22 个已登记 TUI/worker 均已结束，五个任务锁已释放。两个实现 worktree 与约 291 MB 构建、测试和复核临时文件已清理；源码、分支、七个必要运行目录及检查点、失败证据和审核二进制保留。[原生清理核对](native-cleanup.json)、[主工作区清理](cleanup.json)

本轮约定的有限生命周期实现与原生基础样本已完成。完整 G0 仍需满足未变更的在线 idle 自动回传、当前会话附着与完整交付对账要求；本轮没有实现通用预算账本、真实 CLI adapter 或任意执行树停止保证。G1–G4 未启动，Windows 继续延期。
