# sd-qual-07：0.154.0 下初始窗口仍缺少 SessionStart

本次受控原生资格未通过，结果为 `owned_session_start_missing` / `await_session_start`。没有初始 paste 或 Enter；没有 root、UserPromptSubmit、Go job、prepare 或 send-once。原 case 已消费并保留，不重试。完整 G0 未通过，G1–G4 未启动。

运行主树为 `80fe5d6`（冻结代码 `37c3fd0`），普通 Codex 0.154.0，原 argv 为 `codex app-server --listen unix://` 与 `codex`。六个源码摘要、普通入口真实路径/二进制 SHA、Go SHA、空 socket 与未使用的 case/job 路径均在下发前核对一致。见 [前检](native-sd-qual-07-preflight.json)、[启动记录](native-sd-qual-07-launch.json)。

| 事实 | 记录 |
|---|---|
| controller / service / TUI | 55669 / 55676 / 55701 |
| 外层续接句柄 | exec session 64674，已结束 |
| caffeinate | 55670，`-i -t 300 -w 55669`，退出 0 |
| 初始窗口 | 08:17:39.689277Z 起 120 秒，deadline 08:19:39.689277Z |
| 失败观察 | 08:19:39.763332Z，RAW elapsed 120.073809709 秒 |
| 窗口内真实 Hook | 0 条；只有原窗口后的 SessionEnd，单独分类 |
| 初始输入 | `inputs=[]`；没有 initial-submit 或 send-attempt 文件 |
| 受限 PTY | 6602 bytes，SHA `17d2a2a9523b52465f3357da196206962ee679cbc295ad55f109d39315e5c437` |

原始 controller 结果见 [完整元数据](native-sd-qual-07-controller-result.json)，原 case/job 保留清单见 [字节摘要](native-sd-qual-07-artifacts.json)。PTY 原字节只保存在受限 case 文件中，报告不导出正文。允许列表诊断为：`Starting MCP servers`、`cua_repl`、`node_repl` 均未出现；`usage limit` 与 `weekly` 出现。清理前 service 的可见子进程为 0。该结果不足以断言 MCP 卡住或量化实际额度，也不能据此否定尚未调用的 toolOutput 接口。

controller 未执行正常 quit。外层复核 service 的 PID/birth、实际 executable_path、二进制 SHA、原 socket dev/inode/owner/mode、唯一 holder 与 Unix 连接后发送 SIGINT。退出瞬间进程身份读取返回 ValueError，因此不声称有效清理延迟；独立后检确认四个自有进程均不存在、socket 已消失，保护配置未变。见 [所有权核验](native-sd-qual-07-cleanup-ownership.json)、[收尾](native-sd-qual-07-post-window.json)。早期 startup-diagnostic 的 Unix 列解析仅为部分字段，已由 cleanup-ownership 的正确列解析替代，不用于清理结论。

下一可执行动作是独立只读核对这次 usage 提示的来源与 SessionStart 启动路径，形成新的可验证前提后再登记新 case。不能重启 sd07、重发输入、消费 reset 或扩大本次原窗口。

收尾保留原 case/job、诊断日志和摘要交付物；本次没有新建临时脚本。原有 untracked `scripts/__pycache__/` 未动，也未清理非本任务进程或配置。
