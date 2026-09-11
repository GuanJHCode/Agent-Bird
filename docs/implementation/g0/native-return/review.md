# 独立复核与最终验收

2026-09-11。实现／运行者与复核者分离，写入者使用独立 worktree。最终判定：**实验工具及记录可交付；原合同 G0 不放行，G1–G4 未实施。**

| 冻结来源 | 独立复核结论 | 处理与范围 |
|---|---|---|
| Host `0fc9b0b`／`0d9de85` | PASS | 原覆盖检查存在竞态，改成真实 exclusive-create／publish 冲突；替换为覆盖式写入的 mutation 被测试捕获。只证明合成 Host |
| Collector `153a4ff`→`9300d2a` | 修复后 PASS | 取消任意工具／Hook 原文导出，限定来源与类型；旧版 canary 泄露／新版拒绝回归成立 |
| ACK Host `65d8311` | PASS | 同一 OS 锁、生产 revision、event hash、单 action slot、实际响应丢失及 20 并发测试符合单事件合同 |
| 身份 `e3f2825` | PASS | idle writer 锁、正常接管、fork/new/clear Goal 归属与两项原生身份字段；未扩为业务权限证明 |
| 竞争 `a33e933`／`ac16fff` | 修正后 PASS | 旧 cell 的新 Goal active→complete 反例在固定窗口内；一处“无第二工具单元”表述已收窄到反例出现前，未改原始事件 |
| 正向 owned ACK `d9d9117` | PASS | 实际 root 环境、原生工具输出、持久记录及 hash 一致；跨 thread 负向 ACK 未执行 |
| Goal 基线 `944df4c` | 事实链成立；新 ready 工具有两项 P1 | 01/02/04 三基线、05 强杀、06 steer 由独立类型化事件支持；不能用宽松 ready 单独证明 Goal |
| ready 修复 `d416e08` | PASS，P1 全部关闭 | 删除 Goal／rollout／SQLite 读取，明确 caller-supplied root／goal_checked=false；旧版 7 项失败、新版 8/8，通过独立复跑 |
| pending／在线 `8309fac` | PASS（窄事实） | 在线 Goal 主动续轮早于结果，不能当作 C05 事件唤醒 |
| 最终 ACK／清理 `7d7884c` | PASS（记录与工具） | 新 ACK 回归 4/4，父版 3 项失败；Host-ready 窗口内成立，完整 ACK 超窗未通过。各 TUI／worker 均对应本任务 |
| 来源 `46d551c`／`b510a66`／`6a4fb9f` | PASS | 0.154 的相关 9 个生产文件独立比较一致；源码与原生实测边界、当前 UI 附着缺口均未夸大 |

主 Agent 已在整合后的源码运行完整 Go race（含 CLI 子进程）和 vet，均 exit 0；Go race 为 16.353s。最终 Python 回归 12/12，通过。原有七份设计文档的 SHA-256 未变；审核过的两个实验二进制按原字节保留。见 [Go 验证](main-host-verification.json)、[最终检查](final-checks.json) 和 [清理清单](cleanup.json)。

goal09 的 child wait 次数以及直到正常 quit 无新 turn，属于 lifecycle／现场观察；类型化事件直接覆盖至 04:12:08，其中 ACK 后 76.164 秒无新 task_started。没有把后续现场观察升级为独立类型化调用证明。首次 ACK 的 stdout 被主动丢弃；真正杀死阻塞 ACK 进程的响应丢失测试属于 Go 夹具验证，两者分开计。

汇总曾有计划状态未更新的一处 P2，已以当前已整合提交替换并复核关闭。最终没有未处理的有效实现／证据复核问题；未验证的原生条件和已失败的 G0 条件仍按 [结果](result.md) 保留，不能因审查 PASS 转成产品通过。
