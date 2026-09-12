# 当前 TUI → thread 附着：已查信号、条件候选与验证边界

日期：2026-09-11；分支 `exp/g0-pending-attachment`；基线 `cd3d0547c807b36e403aca5a85ac34221ab247dc`。

本次文档修正基于 `8a390b5`，由 `exp/g0-pending-attachment-fix` 后继提交记录；不改原生代码、配置或既有实验事实。

## 结论

**当前附着链尚未通过原生验证；存在需要继续核验的条件信号，不能以“缓存切换没有外部事件”排除它们。** 本报告针对固定的 macOS 原生 `codex-cli 0.153.4`（SHA-256 `b973d440acac501fd2594a43e7ca9ce41e0a65b9dfb28d0d7a7837c99e1261e3`），以下切换与异步时序均为固定源码分析，未执行新的原生 TUI 切换实验。

撤回原报告的“决定性无事件反例”：已有缓存的 live side child 虽可跳过 liveness、resume 和 snapshot refresh，但 render/replay 后仍会刷新 skills，产生 `skills/list` RPC；Apps 开启且为 ChatGPT 账号时还可能发出带 thread 的应用查询。需要核验的是这些信号的身份、条件覆盖、发送与显示切换的先后关系，以及旧异步请求如何失效；缺少专用 attach RPC 本身不是排除条件。

这些前提仍未补齐，不能把条件信号当作已合格的附着证明；也不因此否定 [既有 retained 生命周期样本](../../g0-retained-resume/plans/contract.md)。

## 固定输入与源码核对

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

## 缓存切换的完整后续链

下面是固定源码中普通 TUI 选择已有缓存 live side child 的成功路径，不是新的运行复现。

1. `select_agent_thread` 对“在 `side_threads` 中且已有 `thread_event_channels`”的 child 直接跳过 liveness 读取；见 [`session_lifecycle.rs:525`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/session_lifecycle.rs#L525)。
2. 同一个 channel 的 attachment 是 `Live` 时，`should_attach_live_thread_for_selection` 为 false，因此不会调用 `thread/resume`；见 [`session_lifecycle.rs:667`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/session_lifecycle.rs#L667)。
3. side thread 还被 `should_refresh_snapshot_session` 排除，不需要补一次 `thread/resume`；见 [`thread_routing.rs:1407`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/thread_routing.rs#L1407)。
4. 之后 TUI 本地保存旧 receiver、从缓存取 snapshot，在 [`session_lifecycle.rs:611`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/session_lifecycle.rs#L611) 设定 `active_thread_id`，并在 [`session_lifecycle.rs:624`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/session_lifecycle.rs#L624) 重建和渲染 ChatWidget。
5. [`thread_routing.rs:164`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/thread_routing.rs#L164) 明确 `current_displayed_thread_id` 以 `active_thread_id` 为稳态真相，描述的是用户当前屏幕上的 transcript。
6. render/replay 随后调用 `handle_side_thread_session`，进入 session 配置处理；其中 `refresh_skills_for_current_cwd(true)` 产生 `ListSkills`，最终发送 `skills/list`。其 wire 参数仅为 `cwds/forceReload`，没有 thread id；因此“有 RPC”成立，但单靠它还不能确定新显示的 thread。[render/replay:1552](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/thread_routing.rs#L1552)、[session_flow.rs:150](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/chatwidget/session_flow.rs#L150)、[ListSkills:778](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/thread_routing.rs#L778)

由此只能列出下面的源码时序，不能再声称外部观察没有新事件：

```text
T0  Hook/UserPromptSubmit 或 app-server 请求证实 root A
T1  side child B 已 live 且其 event channel 已缓存
T2  用户在 TUI 选择 B
T3  TUI 本地 active_thread_id := B，重绘 B
T4  session 配置处理排入 skills 刷新，随后产生 skills/list(cwds, forceReload)
T5  若 Apps/账号条件满足，还可能异步查询 B 的应用信息
```

T4 没有 thread 字段；T5 带 thread 但有异步调度边界。它们能否与其他原生观察组合成可刷新、可失效的映射仍为 **unknown**，本次没有完成组合验证。

拟议的 B→C 与 C→B 产生完全相同 wire 观察的反例也**未建立**：正常创建流程会先处理已有 side thread，不能假定两个 side child 任意并存；非 side 的缓存目标仍需 liveness `thread/read`。Remote/LocalDaemon 路径在有 pending AppEvent 时还会阻止新 terminal input，不能任意交错 UI 选择并忽略完整请求序列。[side.rs:686](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/side.rs#L686)、[liveness:281](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/session_lifecycle.rs#L281)、[startup.rs:733](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/startup.rs#L733)

同一局部路径在 `rust-v0.154.0` 仍保留：其 [`session_lifecycle.rs:541`](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/tui/src/app/session_lifecycle.rs#L541) 继续跳过 live side child 的 liveness read， [`thread_routing.rs:1531`](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/tui/src/app/thread_routing.rs#L1531) 继续对 side thread 省略 snapshot refresh。这里不把 0.154.0 的其他 UI 改动概括成“没有任何变化”。

## Apps 与原生日志的条件候选

Apps 路径要求 `Feature::Apps && has_chatgpt_account`。本轮已有只读观察为 `apps=false`、ChatGPT 登录布尔值为 true；没有为本报告改变开关或启动新 TUI，因此不能把 Apps 路径写成本机已发生的观测。[条件:144](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/chatwidget/connectors.rs#L144)

条件满足时，处理器先捕获 `current_displayed_thread_id` 再 `tokio::spawn`；请求为 `app/installed{threadId,forceRefresh}`，后续 `app/read` 也带捕获的 thread id。RPC 中没有 UI generation/epoch；随机 request id 只关联请求。fetch 发起前、结果送回 UI 时都有 generation 检查，但该 spawn handle 没有保存；已派发的网络任务并未因切屏结果过滤而被取消。因此旧 B 的请求可能晚于后续显示切换到达，不能直接用“最近 Apps 请求的 thread”认定当前屏幕。这个迟到可能性仍是源码时序分析，不是完整不可区分反例。[捕获与 spawn:35](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/connector_mentions.rs#L35)、[wire 请求:64](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/connector_mentions.rs#L64)、[发起前过滤:914](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/event_dispatch.rs#L914)、[结果过滤:1525](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/event_dispatch.rs#L1525)

`session_log` 默认关闭，需 `CODEX_TUI_RECORD_SESSION` 启用，可通过 `CODEX_TUI_SESSION_LOG_PATH` 选路径；本次未启用。`AppEventSender::send` 在 enqueue 前记录非 CodexOp 事件，但 `SelectAgentThread`、`FetchInstalledConnectorMentions`、`InstalledConnectorMentionsLoaded` 走 variant-only 分支，不含目标 thread/generation。另一方面 outbound 日志会序列化完整 `AppCommand`，包括用户内容，不能直接当作现成的白名单附着日志。热键 toggle 还会直接调用选择流程，绕过 `SelectAgentThread` AppEvent，但仍会走上述 skills 刷新 RPC。[初始化:85](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/session_log.rs#L85)、[日志字段:218](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/session_log.rs#L218)、[AppEventSender:34](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app_event_sender.rs#L34)、[热键:312](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/input.rs#L312)

## 已查路径

| 路径 | 可确认事实 | 尚未满足的附着条件 |
|---|---|---|
| 普通 app-server UDS / proxy | 官方 App Server 文档把 listener 与 `codex --remote <endpoint>` 作为显式连接流程；0.153.4 的默认 daemon probe 只在 50ms 内 `UnixStream::connect`，见 [`tui/src/lib.rs:447`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/lib.rs#L447)。 | connect 成功不表示 initialize、更不表示 TUI 已把某 thread 渲染到屏幕。显式 `--remote` 不符合本轮入口约束；另起 listener 也不能倒推已经接管原先打开的 TUI。预先存在 listener 被普通入口复用的证据，按上面的既有样本单独保留。 |
| 原生日志 / RPC 请求 | app-server 在收 JSON-RPC/typed request 时记录 `trace!`，见 [`message_processor.rs:608`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/message_processor.rs#L608)。现有共享 listener 日志可保留 `client_name=codex-tui`、method 和 connection id。cached-child render 后会引发 skills RPC，Apps 满足条件时还有带 thread 的请求。 | 不能把所有请求统一放在 commit/render 之前。skills 请求缺 thread；Apps 请求可能迟到；session_log 非默认启用且 selector 记录无目标。它们与显示提交的映射、失效及覆盖仍待验证。 |
| `SessionStart` / `UserPromptSubmit` Hook | Hook 可以把一次真实活动关联到 thread。 | 这两个 Hook 本身不覆盖上述显示切换；一次活动证据不能单独维持“当前显示”资格，但不排除与其他实际信号组合。 |
| in-process App Server | `InProcessClientSender` 持有 `mpsc::Sender<InProcessClientMessage>`，`InProcessClientHandle` 持有 receiver 与 runtime task，见 [`in_process.rs:214`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/in_process.rs#L214)。 | 这是 TUI 进程内 Rust handle，不是子工具可取得的 UDS/FD/RPC bridge。 |
| unified-exec 显式注入 | 普通 sandbox 启动时显式加入 thread id、session id、apply-patch 与权限 profile，再清理 output 环境，见 [`process_manager.rs:1342`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/unified_exec/process_manager.rs#L1342)。 | 这些是身份/运行配置，不是能回连原服务或确认当前显示的 endpoint。本结论限于该明确注入路径和所查源码；不声称已穷尽任意继承环境或未公开扩展。 |
| 内建 app-server extensions | 已查 event sink 只处理 queue、Goal 更新和 warning，并按现有订阅 connection 转发，见 [`extensions.rs:177`](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/extensions.rs#L177)。 | 没有 TUI render/attach/detach epoch；订阅本身也不等于 displayed thread。 |
| 0.154 Python SDK `ExternalMessage` | 官方变更说明其通过 `toolOutput` 向 SDK 的 `run()`/`turn()` 发送 tool-authority 内容，可启动或加入活动 regular turn。 | 它解决的是输入权限和独立 SDK consumer 流，不提供通往普通已开 TUI 的原服务 endpoint，也没有上述本地切换失效语义。 |

官方入口和版本语义见 [App Server 文档](https://learn.chatgpt.com/docs/app-server#connect-the-cli-terminal-ui) 与 [2026-09-10 changelog](https://learn.chatgpt.com/docs/changelog)。

## 允许重新打开附着实验的最小条件

新的候选至少需要同时给出：

1. 仅用普通入口即可抵达原 TUI 所在服务，且不把独立新服务或相同历史当作原服务。
2. 对现有 skills/Apps/日志等信号或其他外部可读事件，给出与 TUI 本地显示提交相符的实例/thread 映射、时序和失效证据；不要求一定新增专用 RPC。
3. 对 cached live side child、`/new`、`/clear`、resume、fork、断连和重连均能刷新或 fail closed。
4. 先确定普通入口下可取得的最小观测及条件覆盖，再安排有边界的原生样本；本次文档修正没有启动 TUI、原生模型、daemon、proxy 或更改任何配置。

当前这些条件尚未组合验证，状态保留为 unknown / 未通过。是否能用已有信号组合达标需要继续核验；不能从旧报告已撤回的“无事件”前提，或单凭没有专用接口，推导出整条路线没有实验价值。

### 2026-09-12：原生状态栏的可见候选

固定 `0.153.4` 提供 `thread-id`（兼容 `session-id`）状态栏项，由当前 widget 的 `thread_id.to_string()` 显示完整 UUID；尚无 session 时省略。cached side/primary 重放会更新 widget 的 thread ID 并刷新状态面板。这是可继续研究的原生可见信号。[状态栏项](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/bottom_pane/status_line_setup.rs#L137)、[显示值](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/chatwidget/status_surfaces.rs#L757)、[重放更新](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/chatwidget/session_flow.rs#L21)

独立只读检查仅输出相关布尔值：现有 global `tui.status_line` 没有这两个 ID，本任务 test-repo 配置不存在，因此当前没有已开启的该信号。没有更改配置、申请 UI/Automation 权限或实现终端采集器。widget 在切换期间还可滞后于本地选择状态，故单个可见文本快照不足以认定通用附着已解决。[切换期间的 widget 状态](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/thread_routing.rs#L164)
