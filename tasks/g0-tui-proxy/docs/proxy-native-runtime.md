# 受控代理 runtime/controller

`scripts/proxy_native_runtime.py` 只提供受控实验的准备代码。导入模块不会启动
Codex、app-server 或模型；原生边界是显式调用 `NativeRuntime.start()`。

runtime 固定普通 TUI 入口为单一 `codex` argv，拒绝 `--remote`、模型参数、配置
覆盖和 prompt。backend argv 固定为同一冻结二进制的
`app-server --listen <private-UDS>`。默认前端 socket 或私有 backend socket 已存在
时，在创建任何子进程前拒绝启动。

`AttachmentInferer` 只接收 observer 的白名单事件。候选要求同一连接 epoch 的双向
WebSocket upgrade、`initialize`、`thread/start` 及 `skills/list` 的请求/响应，并从
`thread/start` 响应读取线程身份。`/status` 结果只能作为独立 UI 对照保存；driver
自报的 selected/root 不会产生候选。clear 会废止旧目标，只有随后观测到的新
`thread/start` 才能在同一连接上重新建立候选；新连接不会继承资格。gap、EOF、未知
事件和序列缺项立即失效。

runtime 保存二进制/配置摘要以及自有进程的 birth/executable。关闭时只清理仍与
登记 inode 相同的 endpoint，并在默认清理路径再次核对 PID birth/executable；被替换
的 socket 或无法确认身份的进程保留。窗口基于 `CLOCK_MONOTONIC_RAW`，本地操作 10 秒，必要模型轮次 120 秒，
但本实现没有启动模型轮次。

当前离线验证覆盖 runtime、transport、observer 共 121 项。尚未执行 native、TUI
或模型，也不把离线通过当作原生附着证据。

显式运行入口为：

```text
python3 tasks/g0-tui-proxy/scripts/proxy_native_runtime.py --run \
  --case-dir <new-proxy-attach-01-dir> \
  --frontend-socket <empty-default-socket> \
  --backend-socket <private-backend-socket> \
  --cwd <test-repo>
```

不带 `--run` 只打印准备提示。`--run` 使用专用 PTY 驱动 TUI，按每阶段 10 秒
窗口执行 initial readiness、`/status`、`/clear`、再次 `/status`、前端 leg
disconnect 和 quit；模型轮次没有入口。case 目录及 `result.json` 均以 O_EXCL
创建，PTY 屏幕正文只保留内存尾部，结果只写白名单 Session ID、RPC/连接身份、
摘要和步骤事实。

runner 的 PTY 读取使用非阻塞 fd、`read_available()` 和短 `await`，不会在 relay
所在 asyncio loop 上同步等待；readiness 先由独立 replay 状态驱动，candidate、两次
status 对照、clear 后新 thread、disconnect/EOF 和正常 quit 在最终清理及 hash 保护
完成后共同决定结果。trace 是有界的白名单事件摘要，超限或不完整会使结果保持
`unknown`。

受控 native 实例显式把 observer 的 WebSocket frame/message 上限设为 16 MiB/64 MiB，
与固定 0.154 实现一致；observer 的通用默认上限保持不变。只读 bootstrap 事件仅在
严格的 client 请求/server 同 ID 响应配对后继续，`remoteControl/status/changed` 与
`mcpServer/startupStatus/updated` 只作为无 ID 的 server 通知保留，均不会单独产生 candidate。
gap trace 仅保留 observer 提供的非负整数 `payload_bytes`、`message_bytes`、`limit_bytes`；
无效值被丢弃，gap 本身仍使 trace fail-closed。

仅当 observer 对当前工作目录下精确的 `git -c safe.bareRepository=explicit branch --show-current`
请求给出纯布尔 `workspace_probe_qualified=true` 时，`command/exec` 才进入严格 pending 配对；
它不构成全局 command/exec 白名单。PTY status parser 保持严格 UUID 行匹配并接受 CRLF。
受控 runtime 要求 backend socket 与 child cwd 都是绝对路径，避免 `unix://` URI 在 child cwd
下被重新解释到未登记的 endpoint。

若 initial replay 或 candidate 在其窗口内未成立，runner 只记录内存态界面枚举并
执行 bounded quit/cleanup，跳过无意义的 `/clear` 和 disconnect；不会把诊断状态当作
附着候选。PTY 窗口同时回答终端 cursor-position 查询，屏幕正文不落盘。
