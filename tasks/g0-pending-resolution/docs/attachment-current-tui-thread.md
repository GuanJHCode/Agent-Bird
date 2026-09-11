# 当前 TUI → thread 附着：已查接口与本地切换反例

日期：2026-09-11；分支 `exp/g0-pending-attachment`；基线 `cd3d0547c807b36e403aca5a85ac34221ab247dc`。

## 结论

**当前没有可进入原生验证的附着候选。** 这不是“所有未来 Codex 版本都不可能提供该能力”的结论，而是固定的 macOS 原生 `codex-cli 0.153.4`（SHA-256 `b973d440acac501fd2594a43e7ca9ce41e0a65b9dfb28d0d7a7837c99e1261e3`）不能用已查的普通入口、Hook、原生日志、app-server RPC、unified-exec 或内建 extension 形成所需链路。

决定性原因不是缺少某一个专用 RPC：已有缓存的 live side child 可以在 TUI 内存中成为当前屏幕 thread，过程中不必发 app-server 请求、不触发 `SessionStart`/`UserPromptSubmit`，也没有此成功分支的结构化 tracing 事件。因而任何只从旧 Hook、请求日志、订阅数或 `loaded` 推导出的正证据都会继续指向旧 thread，不能在此切换后可靠失效。

这仅阻塞“当前显示 TUI→thread 附着”前提；不把 [既有 retained 生命周期样本](../../g0-retained-resume/plans/contract.md) 或完整 G0 以外的目标一并否定。

## 固定输入与复现

| 输入 | 固定值 |
|---|---|
| 本机二进制 | `codex-cli 0.153.4`，SHA-256 见上 |
| 官方源码 tag / commit | `rust-v0.153.4` / `3d2ee51ca2d5db578f328aa75e20aa22c0197c9a` |
| 对照 tag / commit | `rust-v0.154.0` / `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`，仅用于确认未把新版 SDK 功能误当作已安装二进制能力 |
| 原服务事实 | [共享 listener 原生样本](../../../docs/implementation/g0/daemon/result.md) 已证明预先存在的 UDS 可被普通 `codex` 复用，但退出前后 thread 元数据仍无法表示当前 TUI |
| 合同判据 | `tasks/g0-pending-resolution/plans/contract.md`：端点→服务→TUI 实例→当前 thread 必须可刷新、可失效，并排除“同一观察对应不同 displayed thread” |

以下命令只读获得 tag 对象和源码；不启动 TUI、不连接个人会话、不发送模型请求：

```sh
git ls-remote --tags https://github.com/openai/codex.git \
  'refs/tags/rust-v0.153.4*' 'refs/tags/rust-v0.154.0*'
curl -fsSL https://raw.githubusercontent.com/openai/codex/rust-v0.153.4/\
codex-rs/tui/src/app/session_lifecycle.rs
curl -fsSL https://raw.githubusercontent.com/openai/codex/rust-v0.153.4/\
codex-rs/tui/src/app/thread_routing.rs
```

已核对源码摘要的 SHA-256：

| 文件（`rust-v0.153.4`） | SHA-256 | 本报告使用的边界 |
|---|---|---|
| `tui/src/app/session_lifecycle.rs` | `17a2c54df66a29753471a4792fd66d6f42fa094d5546cf42452fdf1fb5bceb08` | TUI 选择、切换和重渲染 |
| `tui/src/app/thread_routing.rs` | `93af9728b626ed0440db7b4b9e6c3a8d64e47b39f5f0c16fdc0377b7adab5f14` | displayed thread 定义与缓存刷新门槛 |
| `app-server/src/in_process.rs` | `5dd5f615be26b7fc961c73ac42ebaeb24871eb895aaa4b3035d4cdafd054ebf8` | in-process client 的内存通道 |
| `app-server/src/message_processor.rs` | `e060124c3cec98dfccc02e73e0622212ffee2989c36e470043393adb966fcdb6` | 收到 RPC 时的日志语义 |
| `app-server/src/extensions.rs` | `5e578d25d67d0692c65fb36d8d5c88bf01a0996ce7e9b82f38c5477789abda04` | 内建 extension 的事件出口 |
| `core/src/unified_exec/process_manager.rs` | `b6b045843b5ff128a6e98ca8be271e6e0575bc6b4adfedbf9ead843e4119d3ed` | 普通 unified-exec 明确注入路径 |

## 决定性本地切换反例

下面是一个普通 TUI 已持有的、已缓存 live side child 被用户选中的成功路径。

1. `select_agent_thread` 对“在 `side_threads` 中且已有 `thread_event_channels`”的 child 直接跳过 liveness 读取；见 [`session_lifecycle.rs:525`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/session_lifecycle.rs#L525)。
2. 同一个 channel 的 attachment 是 `Live` 时，`should_attach_live_thread_for_selection` 为 false，因此不会调用 `thread/resume`；见 [`session_lifecycle.rs:667`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/session_lifecycle.rs#L667)。
3. side thread 还被 `should_refresh_snapshot_session` 排除，不需要补一次 `thread/resume`；见 [`thread_routing.rs:1407`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/thread_routing.rs#L1407)。
4. 之后 TUI 本地保存旧 receiver、从缓存取 snapshot，在 [`session_lifecycle.rs:611`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/session_lifecycle.rs#L611) 设定 `active_thread_id`，并在 [`session_lifecycle.rs:624`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/session_lifecycle.rs#L624) 重建和渲染 ChatWidget。
5. [`thread_routing.rs:164`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/thread_routing.rs#L164) 明确 `current_displayed_thread_id` 以 `active_thread_id` 为稳态真相，描述的是用户当前屏幕上的 transcript。

因此，下面的时间序列可发生而外部观察没有新事件：

```text
T0  Hook/UserPromptSubmit 或 app-server 请求证实 root A
T1  side child B 已 live 且其 event channel 已缓存
T2  用户在 TUI 选择 B
T3  TUI 本地 active_thread_id := B，重绘 B
T4  观察者仍只持有 A 的旧正证据
```

这已构成“同一观察对应不同 displayed thread”的具体反例。它不依赖理论上的跨系统原子性要求，也不需要重跑模型来成立。

同一局部路径在 `rust-v0.154.0` 仍保留：其 [`session_lifecycle.rs:541`](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/tui/src/app/session_lifecycle.rs#L541) 继续跳过 live side child 的 liveness read， [`thread_routing.rs:1531`](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/tui/src/app/thread_routing.rs#L1531) 继续对 side thread 省略 snapshot refresh。这里不把 0.154.0 的其他 UI 改动概括成“没有任何变化”。

## 已查路径

| 路径 | 可确认事实 | 为什么不能成为当前附着证据 |
|---|---|---|
| 普通 app-server UDS / proxy | 官方 App Server 文档把 listener 与 `codex --remote <endpoint>` 作为显式连接流程；0.153.4 的默认 daemon probe 只在 50ms 内 `UnixStream::connect`，见 [`tui/src/lib.rs:447`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/lib.rs#L447)。 | connect 成功不表示 initialize、更不表示 TUI 已把某 thread 渲染到屏幕。显式 `--remote` 或先起 listener 改变普通已开启 TUI 的入口，不能倒推成 attach。 |
| 原生日志 / RPC 请求 | app-server 在收 JSON-RPC/typed request 时记录 `trace!`，见 [`message_processor.rs:608`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/message_processor.rs#L608)。现有共享 listener 日志可保留 `client_name=codex-tui`、method 和 connection id。 | 这是请求到达服务端，早于 TUI 的本地 commit/render；该成功 cached-child 分支不需要 request。所审的切换成功分支没有 post-render 结构化 trace。 |
| `SessionStart` / `UserPromptSubmit` Hook | Hook 可以把一次真实活动关联到 thread。 | 它不覆盖上面的纯本地显示切换；所以只能成为一次活动证据，不能维持或失效“当前显示”资格。 |
| in-process App Server | `InProcessClientSender` 持有 `mpsc::Sender<InProcessClientMessage>`，`InProcessClientHandle` 持有 receiver 与 runtime task，见 [`in_process.rs:214`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/in_process.rs#L214)。 | 这是 TUI 进程内 Rust handle，不是子工具可取得的 UDS/FD/RPC bridge。 |
| unified-exec 显式注入 | 普通 sandbox 启动时显式加入 thread id、session id、apply-patch 与权限 profile，再清理 output 环境，见 [`process_manager.rs:1342`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/unified_exec/process_manager.rs#L1342)。 | 这些是身份/运行配置，不是能回连原服务或确认当前显示的 endpoint。本结论限于该明确注入路径和所查源码；不声称已穷尽任意继承环境或未公开扩展。 |
| 内建 app-server extensions | 已查 event sink 只处理 queue、Goal 更新和 warning，并按现有订阅 connection 转发，见 [`extensions.rs:177`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/extensions.rs#L177)。 | 没有 TUI render/attach/detach epoch；订阅本身也不等于 displayed thread。 |
| 0.154 Python SDK `ExternalMessage` | 官方变更说明其通过 `toolOutput` 向 SDK 的 `run()`/`turn()` 发送 tool-authority 内容，可启动或加入活动 regular turn。 | 它解决的是输入权限和独立 SDK consumer 流，不提供通往普通已开 TUI 的原服务 endpoint，也没有上述本地切换失效语义。 |

官方入口和版本语义见 [App Server 文档](https://learn.chatgpt.com/docs/app-server#connect-the-cli-terminal-ui) 与 [2026-09-10 changelog](https://learn.chatgpt.com/docs/changelog)。

## 允许重新打开附着实验的最小条件

新的候选至少需要同时给出：

1. 仅用普通入口即可抵达原 TUI 所在服务，且不把独立新服务或相同历史当作原服务。
2. 一个在 TUI 本地 `active_thread_id`/屏幕切换提交之后才发出的、外部可读的带实例与 thread 身份事件，或等价的可验证映射。
3. 对 cached live side child、`/new`、`/clear`、resume、fork、断连和重连均能刷新或 fail closed。
4. 在该候选存在前，不为凑样本启动原生模型；本次没有启动 TUI、原生模型、daemon、proxy 或更改任何配置。

当前已查接口没有满足第 2、3 项的信号，因此不建议就这一附着路线安排新的原生样本。
