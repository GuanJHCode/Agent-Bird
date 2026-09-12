# sd-qual-05：10 秒内未观察到本服务 SessionStart

本次回传资格未通过。paste 前的 SessionStart 门禁按设计触发：`owned_session_start_missing` / `await_session_start`。未发送初始提示，因此没有 `UserPromptSubmit`、prepare 或 send-once。不重试本 case。

运行采用主树 `894cc38`，Python 3.13.7、native 0.153.4。前检通过后 controller 31073，caffeinate 31074 为 `-i -t 300 -w 31073`。见[启动记录](native-sd-qual-05-launch.json)。

| 事实 | 本次记录 |
|---|---|
| SessionStart | 无；`hook-events` 在该 10 秒窗口仍无本服务记录 |
| 原生进程 | service 31082、TUI 31106，出生 `Sat Sep 12 14:59:54/55 2026` |
| SessionStart 窗口 | 约 06:59:56Z 起 10 秒，06:00:06Z 中止 |
| 有界 PTY | 11047 字节，SHA `5e43a53dc571441b…`；JSON 只存 path/bytes/sha |

PTY 可见启动横幅为 Codex v0.153.4、YOLO、test-repo 目录，并停在 `Starting MCP servers (1/3): cua_repl, node_repl`。这解释了为何 10 秒本地预算内没有 SessionStart，不能据此否定 send-once 接口。

controller 跳过正常 quit。外层核对 service 31082 原身份与唯一 socket holder 后 SIGINT，约 106ms 退出，socket 消失。见[收尾](native-sd-qual-05-post-window.json)。原 case 字节归档见[SHA 清单](native-sd-qual-05-artifacts.json)。
