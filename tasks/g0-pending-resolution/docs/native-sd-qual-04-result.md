# sd-qual-04：摘要合同已对齐，初始 Hook 窗口内未绑定

本次回传资格未通过。controller 与 helper 已冻结为唯一 nonce `sd-qual-04`，canonical binding 摘要离线整链已独立复核。原生运行在 `initial_hook_scan` 结束为 `initial_binding_inconclusive`：120 秒原窗口内 `discover_prompt_hook` 未得到属于本服务的唯一 `UserPromptSubmit`。未进入 READY/pending 屏障、prepare 或 send-once，没有发送意图，不重试本 case。

运行采用主树 `6c15326`，Python 3.13.7、websockets 16.0、native 0.153.4。新鲜前检通过后，同 PID exec controller 26203；限时 caffeinate 26204 使用 `-i -t 300 -w 26203`。源 SHA、普通入口 argv 见[启动记录](native-sd-qual-04-launch.json)。没有 `/status`、Goal、第二次初始提交或重发。

| 事实 | 本次记录 |
|---|---|
| 精确 root / turn | 无；`candidate_hook` 未写入 |
| 原生进程 | service 26212、TUI 26236，出生分别为 `Sat Sep 12 14:31:48/49 2026`；comm=`codex`，libproc executable_path 匹配 0.153.4 |
| 原初始窗口 | RAW `4117278136282958` → `4117398136282958`，墙钟 06:31:50.240536Z 起 120 秒 |
| 初始输入 | 一次 bracketed-paste（2101 字节）+ 一次独立 Enter；两者间隔约 71µs |
| Hook 目录 | `hook-events` 在该窗口 mtime 命中 0；最新仍为 sd03 的 SessionEnd |
| 中止 | 06:33:50.422767Z，`initial_binding_inconclusive` / `initial_hook_scan`，controller 第 515 行 |

[controller 记录](native-sd-qual-04-controller.json)保存 origin、startup_scheduling（`input_ready_proven=false`）和 PTY 输入摘要（11133 字节，SHA `e1b3c556…`）。同一秒内 Codex home 出现 thread-writer lock 名 `01a09450-ae84-7442-bafe-52c5bb914cff`，并有插件缓存写入；这不是 `UserPromptSubmit` 证明，也不导出该 thread 的历史正文。

controller 跳过正常 quit；TUI 在 PTY master 关闭后不在。外层核对 service 26212 仍为原出生身份、原 executable、原 socket inode，且 lsof 仅该 PID 持有 socket 后发 SIGINT。进程退出，socket 消失。不把这次收尾算作 normal quit，也没有对其它客户端发信号。见[收尾](native-sd-qual-04-post-window.json)。

原 case 三份 JSON 按原字节归档，见[SHA 清单](native-sd-qual-04-artifacts.json)；保护文件摘要未变。本次没有记录模型推理或凭据。sd04 不复用。
