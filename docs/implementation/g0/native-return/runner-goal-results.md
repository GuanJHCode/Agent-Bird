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

上述样本使用的旧 `runner_ready.py` 将 Goal 与 Host 资格放在一起。后续独立审查发现来源验证缺口，当前脚本已收窄为 **Host/进程就绪检查**：不读取任何 Codex DB 或 rollout，不解释 Goal，对输出显式标记 `goal_checked=false`。其调用者提供的 root ID 只是声明；owned job 需与该声明一致，但真正的原生 thread/Goal 资格仍须独立验证。编排只把这个检查用于本任务操作顺序，不把 Host-ready 当成 active Goal 的证明。

## goal03 操作失配：排除于三个基础样本之外

goal03 的 worker 于 02:55:52.448 启动，02:56:52 已完成；观察者在 02:57:05 才提交 `/quit`，超过 02:54:54–02:56:54 的原定初始模型窗口。监视器正确拒绝已经退出的 worker，退出码 1，没有生成监视文件。因此它不提供“退出时任务仍运行”的证明，不能计入上述三个基础样本，也不能靠扩大窗口补成通过。

之后仅正常恢复同一 root 收束 active goal：02:59:40 无 prompt resume，02:59:59.742 工具确认 complete。该数据保留为结果在退出前已经 ready 的变体。[原生记录](runner-goal-03.json)、[失配与收束记录](runner-goal-03-lifecycle.json)。

## goal05：强杀当前 TUI 的独立样本

在 active goal 与 Host ready 都已确认后，先重新核对 PID 67153 的完整创建身份，再于 03:12:13.075 仅向这个本任务 TUI 发送 SIGKILL。没有使用 Esc、Ctrl-C 或修改 paused 状态。原生 exec 句柄实际退出码为 **1**；不按惯例改写成 137。

监视器直接记录该 TUI 消失后 worker 70688 继续存活，随后同一 nonce completed/count 1 落盘。03:13:11 无 prompt 恢复 `01a08e72-bb0a-7942-bd3b-8cb1793e8d58`；一个新的 goal 控制 turn 真实读取结果，03:13:35.648 complete goal，03:13:40.571 ACK。至 03:15:11 的固定窗口没有新增 turn。这是一个强杀样本，未替代原方案更广的故障屏障。[原生记录](runner-goal-05.json)、[核对身份与信号](runner-goal-05-lifecycle.json)、[ready](runner-goal-05-ready.json)、[进程监视](runner-goal-05-process-monitor.jsonl)。

## goal06：真实用户输入打断活动等待

本组保持 TUI 打开，Host 延迟 40 秒。03:19:36.390 直接核对：root `01a08e79-8b36-7073-9c9e-800f215d5f40` 有且仅有一个尚未返回的原生 `clock.sleep(duration_ms:60000)`，Host 仍运行且 result 不存在。随后通过 TUI 输入明确标为 `USER-STEER` 的实验用户请求；该文本不是执行结果或工具回调。

03:19:37.533，sleep 在 13.8574 秒后返回 `Sleep interrupted by new input.`；03:19:41.024，主控在同一 turn 的 commentary 中输出 `STEERED`。它继续已有目标，之后只调用一次 root Host read，03:20:24.941 得到真实 completed/count 1，03:20:27.311 complete goal，03:20:32.606 ACK。没有重派 start、没有新建模型 turn，也没有反复读取 pending。新用户输入后的固定观察窗口到 03:21:37，没有再续轮。[原生记录](runner-goal-06.json)、[输入及未完成 sleep 证据](runner-goal-06-lifecycle.json)、[ready](runner-goal-06-ready.json)。

## goal07：Host 仍运行时提前恢复

本组沿用 60 秒 Host，但确认旧 TUI 正常退出后即恢复，没有先等 Host 完成。03:27:13.722，worker 79582 的创建身份仍匹配且 result 不存在；随后直接 `codex resume 01a08e80-5b64-7282-b2c4-acb0776fa362`，不附 prompt。

自动 goal turn 于 03:27:14.108 启动。03:27:33.766，一次真实 root read 返回 pending/count 0；03:27:44.602 启动一次 Host wait，返回运行 session 15849；之后仅一次 `write_stdin` 等待该命令，03:28:04.796 得到 completed/count 1。03:28:07.783 complete goal，03:28:11.306 ACK，固定恢复窗口到 03:29:13 没有再续轮。没有再次 start，也没有反复用模型读取 pending。[原生记录](runner-goal-07.json)、[提前恢复时的存活证据](runner-goal-07-lifecycle.json)、[进程监视](runner-goal-07-process-monitor.jsonl)。

## goal08：在线 active goal 在 final 后继续编排

这是一个无 child 的对照样本：root 直接调用合成 Host start 作为任务派发，确保 Host 已启动后才 final。整个窗口不调用 clock.sleep、不退出或 resume TUI、不再输入人工 prompt，也不存在 child completion 消息。

03:33:28.193，root `01a08e86-6a18-7be0-b5b9-ea03b1eca9bc` 输出 WAITING 并 `task_complete`。仅 **23 毫秒**后，原生 active goal 自动启动第二个 turn；控制项仍是带 goal 标记的 `message/role=user`。03:33:34.556 一次 read 返回 pending/count 0，之后一次 Host wait 和一次既有 session 等待，03:34:26.258 得到真实 `custom_tool_call_output` completed/count 1。原生 goal 被 complete，03:34:29.913 ACK。固定窗口为 03:32:53–03:34:53，其中 ACK 后观察 23.087 秒，没有额外 turn。[原生记录](runner-goal-08.json)、[唯一初始输入与 TUI 操作](runner-goal-08-lifecycle.json)、[已启动 Host 身份](runner-goal-08-ready.json)。

这说明 active goal 会在结果产生前就继续编排，并在一个活动 turn 内阻塞等结果。本样本没有重复 `pending→final→goal续轮` 的模型忙轮询；它也不证明“仅在结果事件到达时才唤醒主控”的原 C05 语义。

## goal09：Host-ready 正例与超窗的 ACK 重试观察

**Host-ready 在原定窗口内通过；完整 ACK 流程未在 120 秒内完成，不能计为窗口内通过。** 唯一 root 为 `01a08ea6-b1fb-79b1-a146-1158e247b8e4`，nonce 为 `nr-g0-goal-09`，初始窗口固定为 04:08:08–04:10:08。之后只给予至 04:12:08 的有界收束观察，没有新的模型输入、重派任务或重设起点。

04:08:18.673，原生 `create_goal` 返回本 root 的 active 状态，同次 `printenv CODEX_THREAD_ID CODEX_SESSION_ID` 只返回一个 UUID，与本 root 一致；这里不据此声称已经验证 CODEX_SESSION_ID。主控第一次把 `mkdir && start` 放在同一命令中，因观察者已预建空目录，04:08:37.219 的 mkdir 报 File exists、exit 1，后面的 start 未执行。主控随后只用 rmdir 删除该空目录、以 0700 重建，再于 04:09:06.497 成功执行一次 owned start。这个准备失配消耗了初始窗口，不能通过重置计时排除。

修复后的 `runner_ready.py` 于 04:09:06.581 真实观察到 TUI PID 79894 与 Host worker PID 92122 的创建身份、owned job 的 controller/thread 匹配、result 尚不存在。输出明确 `scope=host_process_and_pending_result`、`goal_checked=false`；active Goal 资格来自前述独立原生工具返回，未由 Host-ready 推断。主控使用原生环境 `CODEX_THREAD_ID` 启动任务，没有设置或伪造该变量。唯一 child 只执行一次 Host wait 和两次既有进程的 write_stdin 等待，没有启动任务。[Host-ready](runner-goal-09-ready.json)、[生命周期](runner-goal-09-lifecycle.json)。

| UTC 时间 | 已发生的原生证据 |
|---|---|
| 04:10:06.575 | child 的真实工具返回 completed/count 1；仍在初始窗口内 |
| 04:10:13.598 | child 的 `agent_message` 进入 root，无 role 属性；已超过初始窗口 |
| 04:10:21.312 | root 实际 inspect 的 `custom_tool_call_output` 返回 completed、实际 event_hash、ack=null |
| 04:10:46.235 / 46.357 | root 在一个 `&&` 命令链中执行 ACK UUID1 并将 stdout 重定向至 `/dev/null`，随后以 UUID2 重试，再 inspect 两次；工具成功返回首条 UUID1 的 ACK 及两个相同 envelope |
| 04:10:49.761 / 51.836 | 原生 goal complete，随后最终 `nr-g0-goal-09-ACK` 和 task_complete |
| 至 04:12:08 | 有界收束窗口内，ACK 后 76.164 秒无新增 task_started |

首条 command ID 为 `10000000-0000-4000-8000-000000000001`，重试为末位 `2` 的新 UUID。重试返回仍保留首条 UUID，`decision_count=1,effect_count=1`；两次 inspect 与后来只读取得的 `ack-r1.json` 完全一致，event_hash 按合同重新计算一致。这是**主动丢弃 stdout** 的单次原生观察，不能称为 ACK 进程被杀，也不证明第三方动作恰好执行一次、Goal 与 ACK 原子提交或完整 G0 通过。[实际消息类型与精确工具 JSON](runner-goal-09.json)、[持久结果](runner-goal-09-persisted.json)、[冻结检查](runner-goal-09-checks.json)。

最终导出仅为这个固定 root/nonce 增加已知 ACK envelope 的精确键、类型和 hash 白名单，不支持任意工具正文。新回归先出现 3 项预期红灯，实现后 4 项通过，包括未知字段、错误类型、owner/hash 和 canary 拒绝；原有 8 项 readiness CLI 回归与隐私回归仍通过。自然完成发生于有界收束窗口内；观察者在恢复上下文后于 04:16:29 才执行正常 `/quit`，exit 0。该较晚的 UI 退出不改变超窗判定，期间没有新增模型轮次。

## 当前边界与资源

上述结果不修复普通无 goal 的 final→idle 自动唤醒缺口，而是验证原生持久 goal 的替代编排条件。身份隔离、ACK 与撤销竞争及原方案剩余交付屏障仍需各自实测，不能从这里外推。

独立审查指出旧 `runner_ready.py` 对任意工具输出中的 Goal 相似对象接受过宽，且未严格校验 job.created_at。现在已直接删除 Goal 解析，所有 job/进程导出字段严格验证，created_at 校验为 UTC RFC3339 后也不导出。最终 8 项 CLI 回归在旧实现产生 7 项失败，修后全部通过；进程查询在这些单元 fixture 中被隔离替代，因此单元回归不算真实原生就绪正例。goal09 已提供真实 Host-ready 正例，Goal 资格另取原生工具证据。[修复时的冻结验证](runner-ready-fix-checks.json)、[后续原生正例](runner-goal-09-checks.json)。此前已列样本的实际 Goal 调用与结果仍由独立采集器比对，不为迁就新脚本而改写旧实验结果。

本 Agent 使用的 8 个 TUI PID、offline01 和 goal01–09 的 10 个 Host worker 已逐一确认不存在，最后 PTY11106 正常退出。所有隐私回归 fixture、脚本字节码及此前误建空目录已清理；最终检查没有新增待删临时文件。保留 runner 脚本、报告和必要的专用 job/result/ACK 目录，不删除原生会话历史、Hook、全局配置、可信二进制或其他 Agent 产物。完整路径与已确认 PID 见 [最终资源记录](runner-cleanup-final.json)。
