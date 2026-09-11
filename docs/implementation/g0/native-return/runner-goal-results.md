# 原生 goal 与独立 Host 的恢复实验

**三个独立受控样本完成了正常退出后自动恢复交付；另有一个强杀样本和一个用户输入中断样本。** 此结论仅覆盖本页列出的合成合同，不宣称 G0 通过。这是新的原生编排组合：用持久 goal 在显式恢复原会话时启动新一轮读取同一任务，不恢复旧 child 的工具 future。

## nr-g0-goal-01

在原生 TUI 使用 `/new` 创建独立 root，通过 `/status` 得到 `01a08e4f-c44d-7cc0-a07c-8a212d739ecf`。随后以 `/goal` 实验请求明确要求：只启动一次 nonce 为 `nr-g0-goal-01` 的 Host 任务，初次由 default child 启动和等待；正常退出后，恢复的主控必须亲自只读相同目录/nonce，用真正工具返回证明 completed/count 1，再 ACK 并完成 goal。未改全局配置、权限或账号。

原生模型实际调用 `get_goal`（初始为空）、`create_goal`；后者于 02:34:55.757Z 返回本 root 的 `status=active`。此证据来自实际原生工具返回，未将本地 SQLite 中无对应行误称为不存在 goal。child 精确 ID 为 `01a08e51-a691-77d3-9803-56ecd8c5f5c3`，原生 parent_thread_id 与 root 匹配；只记录一次 start 和一次 wait。Host 数据目录为 test-repo 中新的 `tasks/native-return/data/runner-host-goal-01`，0700；使用前已核对审查放行二进制 SHA256。

| UTC 时间 | 实际行为与身份 |
|---|---|
| 02:35:27.663 | Host job 落盘，worker PID 54879，唯一 nonce |
| 02:36:16.173 | 监视器记录原 TUI PID 53524 / 创建 10:29:22 +0800 与 worker 同时存活，result 不存在 |
| 02:36:18 附近 | 在 TUI 中提交正常 `/quit`，未使用 Esc 或 Ctrl-C；TUI 最终 exit 0 |
| 02:36:24.234 | 原 TUI 已消失，worker 54879 仍存活，result 仍不存在 |
| 02:36:27.737 | 原 TUI 仍不存在，worker 已退出，completed/count 1 已落盘 |
| 02:37:04 | 仅运行 `TERM=xterm-256color codex resume 01a08e4f-c44d-7cc0-a07c-8a212d739ecf`，没有附加 prompt；新 TUI PID 55425 / 创建 10:37:04 +0800 |
| 02:37:05.133 | 原 root 自动产生新 turn `01a08e53-64e9-7813-a33f-f358f5cee298` |
| 02:37:08.815 | 原生 goal 控制项：`response_item.type=message`、`role=user`，带 `codex_internal_context source="goal"` |
| 02:37:17.226 / 17.368 | 主控真实执行 Host `read`，`custom_tool_call_output` 返回固定 JSON：`version=1,status=completed,nonce=nr-g0-goal-01,count=1` |
| 02:37:21.902 / 22.001 | 调用原生 `update_goal`，工具返回同 root 的 `status=complete` |
| 02:37:25.842 / 25.953 | 输出 `nr-g0-goal-01-ACK` 并 `task_complete` |
| 至 02:39:04 | 完整 120 秒恢复窗口内没有额外 turn，其中 ACK 后观察 98.047 秒；02:39:44 再次只读轮询同一 PTY 仍无新输出 |

窗口内唯一新的 user 角色条目是原生 goal 续跑控制上下文，不是人工 STDIN，也不是 Host 结果。执行结果来自随后真正的 `custom_tool_call_output`，且主控只运行一次 Host read，没有重复 start，也没有反复用模型读取 pending。原生 goal complete 后没有继续自转。

证据：[原生事件与类型化工具返回](runner-goal-01.json)、[直接进程监视](runner-goal-01-process-monitor.jsonl)。监视器只对已确认归属的 TUI PID 和 job 中固定 worker PID 做有限观察，不发送信号；它不是产品轮询机制。

## 正常退出的三个基础样本

以下样本均有不同 root / nonce / 新建目录，Host 延迟 60 秒。每个样本都直接观察到原 TUI 消失时 worker 仍活且 result 不存在；不带 prompt 的精确 `codex resume <root>` 之后，只有一个原生 goal 控制续轮，root 只读一次 Host 真实 completed/count 1，再 complete goal 和 ACK。不是通过重复同一 nonce 凑足次数。

| 样本 | root | 无 prompt 恢复 / ACK（UTC） | 固定恢复窗口终点 |
|---|---|---|---|
| [goal01](runner-goal-01.json) | `01a08e4f-c44d-7cc0-a07c-8a212d739ecf` | 02:37:04 / 02:37:25.953 | 02:39:04 |
| [goal02](runner-goal-02.json) | `01a08e5e-11ab-7252-abac-278f81c0ddcd` | 02:50:59 / 02:51:31.901 | 02:52:59 |
| [goal04](runner-goal-04.json) | `01a08e6c-e6fc-76b1-a19a-3d3b98cb3ca2` | 03:06:52 / 03:07:16.118 | 03:08:52 |

goal02 的直接进程证据见 [监视记录](runner-goal-02-process-monitor.jsonl)。goal04 把 ready 检查、监视器启动、正常 `/quit` 串在同一个有界工具编排中，避免观察者决策消耗 60 秒执行窗口；03:05:52.298 确认 active / worker live / result absent，03:05:54 即提交 `/quit`。[ready](runner-goal-04-ready.json)、[生命周期](runner-goal-04-lifecycle.json)、[进程监视](runner-goal-04-process-monitor.jsonl)。

`runner_ready.py` 只读取明确 root 的原生元数据和工具返回，确认 active goal、匹配的专用目录/nonce、存活 worker 与 result absent；输出限定为本任务身份和枚举字段。其退出成功后，编排直接操作已经确认的本任务 PTY，不重新经过模型决策，也不制造结果消息。

## goal03 操作失配：排除于三个基础样本之外

goal03 的 worker 于 02:55:52.448 启动，02:56:52 已完成；观察者在 02:57:05 才提交 `/quit`，超过 02:54:54–02:56:54 的原定初始模型窗口。监视器正确拒绝已经退出的 worker，退出码 1，没有生成监视文件。因此它不提供“退出时任务仍运行”的证明，不能计入上述三个基础样本，也不能靠扩大窗口补成通过。

之后仅正常恢复同一 root 收束 active goal：02:59:40 无 prompt resume，02:59:59.742 工具确认 complete。该数据保留为结果在退出前已经 ready 的变体。[原生记录](runner-goal-03.json)、[失配与收束记录](runner-goal-03-lifecycle.json)。

## goal05：强杀当前 TUI 的独立样本

在 active goal 与 Host ready 都已确认后，先重新核对 PID 67153 的完整创建身份，再于 03:12:13.075 仅向这个本任务 TUI 发送 SIGKILL。没有使用 Esc、Ctrl-C 或修改 paused 状态。原生 exec 句柄实际退出码为 **1**；不按惯例改写成 137。

监视器直接记录该 TUI 消失后 worker 70688 继续存活，随后同一 nonce completed/count 1 落盘。03:13:11 无 prompt 恢复 `01a08e72-bb0a-7942-bd3b-8cb1793e8d58`；一个新的 goal 控制 turn 真实读取结果，03:13:35.648 complete goal，03:13:40.571 ACK。至 03:15:11 的固定窗口没有新增 turn。这是一个强杀样本，未替代原方案更广的故障屏障。[原生记录](runner-goal-05.json)、[核对身份与信号](runner-goal-05-lifecycle.json)、[ready](runner-goal-05-ready.json)、[进程监视](runner-goal-05-process-monitor.jsonl)。

## goal06：真实用户输入打断活动等待

本组保持 TUI 打开，Host 延迟 40 秒。03:19:36.390 直接核对：root `01a08e79-8b36-7073-9c9e-800f215d5f40` 有且仅有一个尚未返回的原生 `clock.sleep(duration_ms:60000)`，Host 仍运行且 result 不存在。随后通过 TUI 输入明确标为 `USER-STEER` 的实验用户请求；该文本不是执行结果或工具回调。

03:19:37.533，sleep 在 13.8574 秒后返回 `Sleep interrupted by new input.`；03:19:41.024，主控在同一 turn 的 commentary 中输出 `STEERED`。它继续已有目标，之后只调用一次 root Host read，03:20:24.941 得到真实 completed/count 1，03:20:27.311 complete goal，03:20:32.606 ACK。没有重派 start、没有新建模型 turn，也没有反复读取 pending。新用户输入后的固定观察窗口到 03:21:37，没有再续轮。[原生记录](runner-goal-06.json)、[输入及未完成 sleep 证据](runner-goal-06-lifecycle.json)、[ready](runner-goal-06-ready.json)。

## 当前边界与资源

上述结果不修复普通无 goal 的 final→idle 自动唤醒缺口，而是验证原生持久 goal 的替代编排条件。提前恢复到仍在运行的 Host、保持 TUI 在线并 final 后的 goal 行为、身份隔离、ACK 与撤销竞争及原方案剩余交付屏障仍需各自实测，不能从这里外推。

goal01–06 的 Host worker 均已结束，goal03 已正常收束；未删除保存必要身份/恢复证据的专用 job/result 目录。所有隐私回归 fixture 和误建的空运行目录已清理。已确认的本任务 TUI 按后续实验继续使用；不操作其他 Agent 的 TUI 或旧服务进程。
