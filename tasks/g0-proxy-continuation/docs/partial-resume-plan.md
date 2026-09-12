# 部分任务停止与续跑实验

实际执行入口是 `../scripts/partial_case.py`。`--prepare` 仅冻结新 case/job、绝对参数、实现与二进制、配置/Hook/种子文件摘要；`--run` 才执行一个经主会话核验的新样本。`partial_resume_plan.py` 中早期纯数据校验函数不能独立证明系统事实，也不作为原生运行的成功判据。

已完成样本 01（停止阶段 unknown）与 02（受控全流程 observed）。它们的 case、job、nonce、thread 和结果不可复用或改写，详见 `proxy-partial-01-result.md`、`proxy-partial-02-acceptance.md`。

实际操作与门禁：

1. 普通 `codex` 创建新 thread；从完整 bootstrap 的 observer candidate 获取真实 ID。通过同一自有 backend 的新鲜 config/read 与 requirements 核实项目 Luna/medium 后，才提交固定合成任务提示。
2. Go fixture 使用固定二进制、全新 0700 job 与继承的 `CODEX_THREAD_ID`。通过只读 inspect 与实际 PID/birth/executable 核实 worker 的直接父进程是自有 backend。
3. 捕获至少一步未完成进度后发送 `/quit`。要求此前 TUI 仍活着、目标 epoch 未失效、终止事件计数为 0；在原本地窗口内取得新 EOF/close 后才清理 backend。阶段时间、退出码、双 peer、进度和清理信息在失败时也保留。
4. 停止期间允许 Go 合同规定的合法已提交尾部；已有步骤必须原样保留。以实际停止后的完整连续前缀作为恢复基线，不把新提交的合法步骤当成旧文件改写。
5. 用真实 thread ID 创建新 resume runtime，观察 10 秒静默；客户端/服务端均不得开始新轮次，inspect 与前缀不得变化。`/status` 仅作独立核对，不能制造候选。
6. 再次核实 Luna/medium 后，发送固定明确继续输入。segment 2 必须完成剩余步骤，保留 segment 1 文件并达到精确总 effect count，最后核对 worker、TUI/backend、端点清理和全部保护摘要。

本地窗口为 10 秒，初始和明确继续模型阶段各为 120 秒。样本 02 的初次 backend cleanup 约 10.155 秒；不能表述为立即停止。实验只覆盖已登记的固定 Go 任务，不证明任意第三方进程树、控制器 SIGKILL 或实际账号登出后的行为。没有安装用户级服务或修改 Codex 本体。
