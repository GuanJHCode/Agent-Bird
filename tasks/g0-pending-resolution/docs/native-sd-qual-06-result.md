# sd-qual-06：初始 120 秒窗口内未观察到本服务 SessionStart

本次回传资格未通过。paste 前的 SessionStart 门禁按设计触发：`owned_session_start_missing` / `await_session_start`。等待用的是 origin 捕获后的初始 120 秒窗口（`deadline_mono_ns - mono_ns = 120000000000`），不是 sd05 的额外 10 秒启动预算。未发送初始提示，因此没有 `UserPromptSubmit`、prepare 或 send-once。不重试本 case。

运行采用主树 `9d2fdc9`，Python 3.13.7、native 0.153.4。前检通过后 controller 39163，caffeinate 39164 为 `-i -t 300 -w 39163`。见[启动记录](native-sd-qual-06-launch.json)。

| 事实 | 本次记录 |
|---|---|
| SessionStart | 无；窗口内 hook-events 无本服务 `SessionStart`（仅 cleanup 时的 `SessionEnd`） |
| 原生进程 | service 39172、TUI 39197，出生 `Sat Sep 12 15:45:10/11 2026` |
| SessionStart 窗口 | 07:45:12.319217Z 起 120 秒，07:47:12.330055Z 中止 |
| paste | `tui_pty.inputs == []`，未补第二次 Enter |
| 有界 PTY | 9156 字节，SHA `45b4c4159aa9d331…`；JSON 只存 path/bytes/sha |

PTY 可见启动横幅为 Codex v0.153.4、YOLO、test-repo、`gpt-6-astra max`，并停在 `Starting MCP servers (1/3): cua_repl, node_repl`；同时可见 `weekly 0% left` 与 usage-limit 提示。这解释了为何 120 秒初始窗口内仍无 SessionStart。不能据此否定 send-once 接口，也不能关 MCP、改全局配置或动账号额度来凑通过。

controller 跳过正常 quit。外层核对 service 39172 原身份、socket ino `161532108` 与唯一 holder 后 SIGINT，约 116ms 退出，socket 消失。见[收尾](native-sd-qual-06-post-window.json)。原 case 字节归档见[SHA 清单](native-sd-qual-06-artifacts.json)。

完整 G0 仍未通过。G0.2–G0.4 与 F01–F26 未开始。
