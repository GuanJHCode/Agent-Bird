# 退出停止、保留现场与明确续接：实施结果

日期：2026-09-11。状态：Go 合成实现与整合验证完成，正式原生样本进行中；完整 G0 尚未放行。

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

正式原生验收待补：3 次正常退出后同原会话明确续接、1 次强杀后续接，逐个记录有效观察窗口、前缀产物、进程和锁的停止证据及恢复工具结果。Go 本地通过不能替代这些样本。

## 交付与剩余范围

源码已整合：`57edba1`、`79c140e`，验证文档 `748e542`；前置探针 `3fb1c99`。独立复核的 macOS arm64 二进制保留在本任务 `data/g0-return-lab-retained-darwin-arm64`，SHA-256 为 `feb9b9ad969abca5ff35db28c0f0d8d920dc0778d7aac68a90952900e989a285`。

完整 G0 仍需满足未变更的在线 idle 自动回传、当前会话附着与完整交付对账要求。G1–G4 未启动，Windows 继续延期。本页会在本轮原生样本与收尾复核完成后更新实际结论。
