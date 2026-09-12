# True-idle 原生回传：定点能力核查

2026-09-11；仓库基线 `cd3d054`。**官方 standalone `turn/start.toolOutput` 确实能以工具结果身份启动空闲 thread，不要求旧 pending call；尚未解决的是普通原 TUI 的原服务可达与当前附着。** 后台 exec、子 Agent 完成、async Hook 和当前可安装插件，没有在本轮核查中提供另一条满足合同的 true final→idle 自动回传路径。因此本阶段没有重跑模型，也没有升级或更改全局配置。

本机 `codex --version` 为 `0.153.4`。GitHub release API 本次返回最新稳定版 `rust-v0.154.0`，发布时间 `2026-09-09T22:35:38Z`；官网另于 9 月 10 日发布匹配的 Python SDK 条目。源码分别固定到 `3d2ee51ca2d5db578f328aa75e20aa22c0197c9a` 和 `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`。未运行新版二进制，不把源码对比称为新版实测。[版本记录](idle-version-check.json)、[官方更新](https://learn.chatgpt.com/docs/changelog)。

## 合法接口与实际接入分开

官方 App Server 接受客户端执行的工具输出：`turn/start` 带精确 `threadId`、`input=[]` 和 `toolOutput{name,namespace?,output}`；`name` 非空，输出为字符串或受支持 content items。无旧 call_id 也合法。空闲时启动新轮，常规轮次忙碌时排入该轮，通知及持久历史保留 `functionCallOutput`。这不是 `thread/inject_items` 的任意历史追加，也不是把结果写成用户文本。[已直接打开的英文官方合同](https://learn.chatgpt.com/docs/app-server#start-a-turn)。

固定源码与文档一致：`turn_processor` 将该参数创建为 `ResponseItem::FunctionCallOutput { call_id: None, ... }`；`start_or_steer` 明确接受 standalone function-call output，无 active turn 时创建 `RegularTask`。其内部 `TurnStartKind::User` 枚举不把这个输入转换成 user message。[入口校验与类型](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server/src/request_processors/turn_processor.rs#L529)、[新轮或排队路由](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/session/turn_input.rs#L237)。

Python SDK 0.154.0 的 `ExternalMessage` 只是该能力的另一受支持入口：同步/异步 `run()` 与 `turn()` 可用，最低 CLI 为 0.151.0，内容具有工具级权限而不赋予用户授权。此更新不证明 SDK 自动发现或连接普通已开 TUI 的内存服务。[SDK 发布说明](https://learn.chatgpt.com/docs/changelog)。附着与对账分别由本轮其他两路核查；不能把另启服务的实验结果算作原会话成功。

原 [G0 验证计划](../../../docs/g0-validation-plan.md) 允许确有必要且通过有效设置连续性验证的最小 `thread/resume`。本报告不把所有 resume 一概排除；但它必须指向原服务、同一 thread，不能覆盖 model/permissions/cwd/provider 或创建第二主控。恢复本身不替代当前 UI 附着、刷新和失效证据。

## 内建候选的调用链

| 候选 | 本次定点核查 | 对 C05 的结论 |
|---|---|---|
| 普通后台 `exec_command` 完成 | `store_process` 安装 `spawn_exit_watcher`；进程退出并排空输出后进入 `emit_exec_end_for_unified_exec`，由 `ToolEmitter` 发出 `ItemCompleted(CommandExecution)`/兼容执行事件。该链不提交新的 standalone FunctionCallOutput 或启动 root 轮次。 | UI 执行完成事件不等于模型重新采样；后续主动 join/read 的工具结果不能倒算为 idle 自动回传。 |
| child 完成通知 | completion watcher 构造 `InterAgentCommunication`，明确 `trigger_turn=false`；legacy 分支为 `inject_fragment_without_turn`。 | 已有 120 秒 idle 样本仍是有效否证；实际 payload 类型是 `agent_message`，不是父工具结果。 |
| child 主动发消息 | `send_message` 使用 QueueOnly；`followup_task` 使用 TriggerTurn，但共享 handler 明确拒绝 root 目标。 | 不能用 child 的 follow-up 绕过 root 限制。 |
| durable-sleep 门槛 | idle pending-work helper 只在 mailbox 请求 trigger，或 thread extension data 已持有 `SleepItem` 时启动。 | 普通 final 没有自动建立该标记；既有 `clock.sleep` 是活动 turn，不能替换 true idle。 |
| async Hook | 完成输出进入异步结果队列；drain 在新用户输入前记录上下文，或在活动轮次工具边界注入上下文。 | 无 active turn 时等下一用户轮；没有新的 idle 触发能力，输出身份也不是执行结果回执。 |
| 同步 Stop Hook | 官方允许 block/reason 产生下一条 continuation prompt，作用于停止点。 | 这是明确的继续机制，具有新用户 prompt 语义；不满足本任务禁止伪用户回调和真实 final→idle 后事件触发的合同。 |

执行事件链：[退出观察器](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/unified_exec/async_watcher.rs#L165) → [构造执行输出](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/unified_exec/async_watcher.rs#L337) → [CommandExecution 完成事件](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/tools/events.rs#L563) → [会话事件持久化与发送](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/session/mod.rs#L2386)。该事件可供已连接客户端观察，不自动提供对普通 TUI 的外部连接入口。

Mailbox：[completion watcher](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/agent/control.rs#L624)、[root 限制](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/tools/handlers/multi_agents_v2/message_tool.rs#L75)、[idle 门槛](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/tasks/mod.rs#L439)。既有实测：[idle/busy/active-sleep](../../../docs/implementation/g0/native-return/runner-results.md)。未重跑已经排除的组合。

Hooks：英文官方页明确后台完成不会启动新轮，且 session 结束会取消未完成后台 hook 并丢弃未交付输出；同步 Stop continuation 另有用户 prompt 语义。[官方 Hooks](https://learn.chatgpt.com/docs/hooks#how-background-hooks-run)、[async drain 源码](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/hook_runtime.rs#L763)。没有新增或重新信任 Hook 定义。

## 插件与 Rust extension 的边界

普通可安装插件可以包含 skills、MCP、browser extension 与 lifecycle hooks；这里禁用 MCP，剩余 Hook 仍服从上面的调度语义。公开 manifest 的解析字段包含 skills/mcpServers/apps/hooks/interface，没有挂载任意 Rust `ThreadLifecycleContributor` 的字段。[官方插件说明](https://learn.chatgpt.com/docs/plugins)、[实际 manifest 类型](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core-plugins/src/manifest.rs#L45)。

源码的 `ExtensionRegistryBuilder` 确实允许宿主注册线程与轮次生命周期贡献者；但普通 app-server 的 `thread_extensions()` 是编译进宿主的安装列表，最后构造 registry。插件 manifest 的 `extensions.com.openai` 是封装配置，不等于这个 Rust runtime registry。**这证明有宿主扩展架构，不证明当前普通 CLI 可以动态安装一个唤醒贡献者。** 未找到受支持的安装/配置入口；也未据此推断所有未公开二进制扩展都不存在。[registry](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/ext/extension-api/src/registry.rs#L18)、[宿主安装列表](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server/src/extensions.rs#L55)、[Agent Plugin 配置解析](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core-plugins/src/agent_plugin_manifest.rs#L91)。

## 版本证据与下一实验门槛

本轮读取两版本各 25 个固定文件，保留 URL/长度/SHA256。13 组文件字节相同；与 idle 直接相关的 async_watcher、三个 v2 消息 handler、插件 manifest 及宿主 extensions 注册均相同。completion watcher、async-hook drain、durable-sleep 检查、pending-work 启动函数四个定点区域 hash 也全相同。其他文件有变化，所以没有声称整个版本等价。[下载清单](idle-source-manifest.json)、[对比结果](idle-source-comparison.json)。

剩余可核查接口类别是：① 普通原服务的受支持 socket/daemon/IPC 连接；② 原生执行环境或内建 extension 提供的非凭据 RPC bridge；③ 获支持的本地 runtime extension 挂载入口；④ 官方未来具体版本改变后台完成或 root mailbox 的触发规则。前三类必须先给出原服务及当前附着证据；第四类必须先有准确发布/源码差异。单纯换 SDK、升级到已比对的 0.154.0、换 prompt 或把等待保持 busy 没有补足这些前提。

若附着路线找到受支持入口，最小模型实验才有可执行性：普通 `codex` 新建本任务 root，记录 PID/出生身份/thread/原服务和有效设置；主控执行合成任务并真正 final，确认没有活动工具后保持 idle；执行方把其真实、已持久化的固定 nonce JSON 通过原服务的 standalone `toolOutput` 提交。固定 120 秒内核对新 turn、真实 functionCallOutput、唯一 ACK、无新用户消息及未改设置。先做一个资格样本；通过后再按主会话排期扩基础重复和 busy/input 竞争。无可达入口时不启动新模型、不用另起服务替代、不修改历史。

本轮只新增报告/元数据，并纠正两处旧报告把合法结果限于 pending-tool 的过严表述；原失败事实不变。原始公开源码下载仅在本任务 tmp，供两路 Agent 共享只读核查后清理；无自有 native TUI、后台任务或认证变更。

追加的唯一按需启动候选见 [launchd socket activation 机制](idle-socket-activation-mechanism.md)。该页仅核查 50 毫秒探测、监听 FD、多连接及冷启动边界；不表示当前合同已允许安装 LaunchAgent 或必经代理，也不把机制推导当作实际通过。

### 2026-09-12：embedded runtime 的剩余入口复核

`native_identity_review` 对固定 `0.153.4 / 3d2ee51` 追加只读核查：`InProcessStartArgs` 没有 listener/endpoint 参数；启动时创建的是有界 `tokio::mpsc` 通道，输入 handle 要求宿主持有 Rust sender。已审普通 TUI 调用只位于启动路径，没有由 Hook 或配置在已运行 TUI 中开启外部 listener 的分支。[进程内启动参数与通道](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/in_process.rs#L133)

embedded 处理器还明确收到 `remote_control_handle: None`；相应请求会返回 unavailable，remote-control websocket runtime 由 standalone app-server 启动路径创建。这没有补出普通 embedded TUI 的本地连接入口。结论只覆盖这些固定调用链，未修改配置、未启动额外实验，也不声称未审二进制能力绝对不存在。[embedded 的 processor 参数](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/in_process.rs#L473)
