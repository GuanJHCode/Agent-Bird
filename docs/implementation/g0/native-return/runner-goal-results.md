# 原生 goal 与独立 Host 的恢复实验

**单个受控样本观察到正常退出后自动恢复交付；尚未达到 3 次基础样本门槛，也不宣称 G0 通过。** 这是新的原生编排组合：用持久 goal 在恢复时启动新一轮读取同一任务，不恢复旧 child 的工具 future。

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

## 当前边界

该样本不修复普通 final→idle 的自动唤醒缺口，而是验证一个原生持久 goal 的替代编排条件。仍需另外两个独立基础样本、强杀场景、用户输入中断、身份隔离及原方案剩余交付屏障。当前恢复 TUI 保持本任务独占，供后续实验使用。
