# native-command-input-01：status 资格未通过

本次未在原 10 秒内获得新鲜 `/status` root，资格 FAIL；没有进入 `quit_with_deadline`，因此正常退出方案尚未获得原生阳性。一次性控制脚本最终 exit 0 只表示完成记录和收尾，不是资格通过。

运行固定自树 `3f1f36d` 的四份已审源码，启动前逐一核 SHA、native 0.153.4 的固定 SHA、普通 PATH、无本任务 pyc、默认 socket 与新 case 不存在。唯一普通 service PID 84543、TUI PID 84572，出生均 `Sat Sep 12 09:55:03 2026`；实际 `ps comm=codex` 与 libproc 可执行路径分别保存。没有提交 initial，也未调用 Go、helper 或 resume。保护文件摘要前后相同。[原控制记录](native-command-input-01-result.json)按原字节归档。

status 原 RAW 窗口为 `4100671296077208` → `4100681296077208`。`status-paste`（19 字节）在 01:55:03.633922Z 完成，独立 `status-submit` 在 01:55:03.633954Z 完成；失败记录为 status / AssertionError，RAW `4100681301200083`，没有延长窗口补算通过。只发出这两条 action。

原 PTY 保存总 12,541 字节的 SHA、capture_start_byte=3930、一次 CPR 回复及 action 时间，未保存 raw UI。运行结束后内存 buffer 已不可恢复。因此当前证据不能区分：尚未具备输入处理条件、Paste/命令未处理、或者 `/status` 已执行但增量渲染未符合严格行 grammar。CPR 回复或早期输出字节不能单独证明发送时的 termios raw mode、bracketed-paste 就绪或 root UI 配置完成。

随后仅只读 service 84543 及原 01:55:03.226080Z–01:55:14.175651Z 窗口。Hook 只有关闭时的 SessionEnd，给出 root 候选 `01a09353-4718-73c1-8933-5d4498801d94`；它不是已识别的 `/status` root。该明确候选的 metadata 为 `vscode`、本实验 cwd、CLI 0.153.4，未发现本窗口 task_started/task_complete/aborted。受限协议记录只有 codex-tui connection 0 的 `thread/start` 方法 span（01:55:03.676114Z），晚于 status-submit 约 42ms；方法 span 并非 UI-ready ack，不能据此独断根因。[事件投影](native-command-input-01-events.json)未发现 UserPromptSubmit 或 turn/start，但缺失记录不构成所有模型活动不存在的完整证明。原记录中 `cleanup.model_input_sent=false` 是控制脚本的输入意图标志，不能当成 native 接收测量。

安全收尾另计：TUI 发 SIGTERM 后 exit -15；没有把该动作当正常 `/quit`。复核 owned service 出生身份、实际可执行路径、默认 socket inode 160977014，且仅筛本服务 kernel 端点的 lsof 没有具体其他 peer 进程后，对 service 发 SIGINT；483,910,625 ns 内 exit 0。两个 PID 与默认 socket 均已不存在。原 case 保留且不复用。

如再安排无模型诊断，下一观测应保留有界元数据：发送前 termios 标志、bracketed-paste enable/disable 与字节偏移、同 service 启动 span 时序；发送后固定 status 标签计数、有限 UUID 候选/偏移、ANSI cursor/erase计数及严格匹配结果。候选不自动升级成 root，现有身份判据不放宽；本次没有实施或运行该新观测方案。

[归档与清理清单](native-command-input-01-archive.json)核对原结果 SHA；独立复核已结束的三份公开源码缓存共 558,999 字节已删除，来源 manifest 与报告保留。没有留下本任务活进程或待用临时缓存。
