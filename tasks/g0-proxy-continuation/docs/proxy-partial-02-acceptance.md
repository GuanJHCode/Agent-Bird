# 部分任务样本 02 验收

2026-09-13：主会话与独立 reviewer 验收通过，仅覆盖本次固定 Go 合成任务。源码基线 7d84f55，实际文件摘要由同目录 preflight 冻结。

- 普通 `codex` 创建新 thread `01a096ad-fd9c-73b3-b4b5-1739b21b1887`。两阶段自有 backend 的新鲜 config/read、requirements 与项目配置来源均确认 Luna/medium。
- segment 1 首次观察到 step 1，停止期间追加合法 step 2；已提交旧步骤的内容与 inode 保持不变。完整停止前缀为 step 1–2。
- 使用同一 thread 的新 runtime 恢复，10 秒静默窗口没有新模型轮，前后 Go inspect 与前缀完全一致；候选与独立 `/status` 精确匹配。
- 明确继续后，segment 2 只完成 step 3–12；最终 effect_count=completed_steps=total_steps=12，旧 step 1–2 原样保留。
- 两次 TUI `/quit` 均退出码 0，终止事件基线 0 后出现新 EOF/close；初始和恢复 trace 均完整。初始模型阶段 client/server turn-start 各 2 次；恢复阶段最终各 1 次，发生在明确继续后，不能将整个实验描述为零模型。
- 主会话重新计算实现、CLI、Go binary、配置、Hook、19 个种子文件和最终步骤摘要，并确认已知自有 PID 68454、68479、69207、69406、69431、70079 全部退出，两个 socket 均不存在。原 partial01 及旧种子未改变。

时序限制：初始 TUI 约 0.54 秒退出并观察到 EOF，之后 backend cleanup 约 10.155 秒。该样本证明自有任务最终有界停止和记录保留，**不证明立即停止、任意进程树清理或控制器崩溃恢复**。

原始证据：`../data/proxy-partial-02/`、`../data/proxy-partial-02-job/`，同目录 preflight、controller argv、post-run receipt。主树旧代理、恢复、重连、partial 与 cached 组合测试为 220 passed；完整 G0 仍未全部通过。
