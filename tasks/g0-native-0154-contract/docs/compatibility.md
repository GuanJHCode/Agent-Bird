# 普通 Codex 0.154.0：限定原生样本的静态版本核验

核验基线为主树 `7263636` 与 `tasks/g0-pending-resolution/plans/contract.md`。结论：原受控 standalone 实验所用接口未发现需要改变调用方式的版本差异；现有执行入口仍锁定 0.153.4 和 sd06，不能直接运行 sd07。以下结果仅允许据此准备版本适配，不代表原生资格或完整 G0 通过。

本机普通入口 `/Users/guanjunhui/.local/bin/codex` 实际解析为 `/Users/guanjunhui/.codex/packages/standalone/releases/0.154.0-aarch64-apple-darwin/bin/codex`，`--version` 为 `codex-cli 0.154.0`，SHA256 为 `4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc`。未切链接、改配置、启动服务/TUI/模型或发送 RPC。

## 结论与必要差异

| 范围 | 0.154.0 固定证据 | 实施要求 |
| --- | --- | --- |
| 普通入口自动复用默认 UDS | 普通无覆盖参数启动仍探测默认 daemon socket，50 ms，仅 connect；有符合条件的服务才复用。 | 保留既有 task-owned listener、socket 所有权和现场独立服务/TUI 核验。不能把 socket 探测等同完成握手、原服务身份或当前显示身份。 |
| 独立 `codex app-server` 的线程 source | CLI 正式分支仍明确传 `SessionSource::VSCode`，协议映射为 `vscode`。 | 保留 `source == "vscode"`；不能因枚举允许 `cli`/`appServer` 就扩大白名单。 |
| 完整历史 | `ThreadReadParams` schema 与旧归档的 SHA256 完全相同；`includeTurns:true` 对 live/persisted、legacy/paginated 仍有完整装载分支，分页实现循环读取全部 turn/item。 | 保留现有一次 `thread/read` 形状、full 判据、10 秒/尺寸上限；deprecated 注释没有取消接口。原服务失败或超窗仍失败/uncertain。 |
| root/session/family | Thread 仍含 `parentThreadId`、`forkedFromId`、`ephemeral`、`sessionId`、`cliVersion`。 | 继续要求父关系显式 null、ephemeral 为 false、sessionId 为 UUID 并后续一致，保持 SQLite 无父 edge 成功证明。仅把固定版本改为 0.154.0。 |
| standalone 真实工具输出 | `TurnStartParams` 整份本机 schema SHA256 与旧 0.153.4 相同；服务拒绝 nonempty input + toolOutput，构造 `ResponseItem::FunctionCallOutput`，没有旧 call ID；core 再为其分配独立 item ID；API 历史映射仍是 `functionCallOutput`。 | 保留 `input:[]`、精确 name/namespace/envelope、唯一发送与历史精确命中。RPC 不含 `role:"tool"` 字段；工具身份来自此专用 output 类型，不能另加 role 或把输出改为 user 文本。 |
| SessionStart/UserPromptSubmit | 两个 hook wire 字段仍是 session_id、hook_event_name，UserPromptSubmit 含 turn_id，SessionStart 含 source；pid/ppid/recorded_at 来自原 Go 采集器。 | 保留真实 hook 采集和服务 PPID 证明；采集脚本中的 CLI version 白名单改为 0.154.0。shell 运行仍是原生 command runner，不能仅从静态源码断言现场 PPID 一定成立。 |
| API 错误 | RPC 仍是 `{id,error:{code,message,data?}}`，code 为 i64，data 任意 JSON，未发送 jsonrpc 字段；模型 turn error 另为 message/codexErrorInfo/additionalDetails 等。 | 现有 helper 已接受 code/message 和可选 data、保存 message 字节数/摘要；不需扩大错误协议，也不能把请求成功或错误响应当业务 ACK。 |
| 连续时钟 | 当前仍是 darwin/arm64、Python 3.13.7，CLOCK_MONOTONIC_RAW=4 可读；helper/controller 实现未变。 | 不修改 RAW 时钟、boot/阶段原点、10/120 秒期限及 caffeinate 边界。这里只确认当前平台和实现，未新做休眠实验。 |

### schema 与 Rust serializer 的区别

本机 schema 将 `parentThreadId`/`forkedFromId` 列为可空但非 required；`Thread` 实际派生 `Serialize`，这两个 `Option<String>` 没有 `skip_serializing_if`，因此 None 输出为显式 null。现有更严格 family 检查与此固定 Rust 输出一致，不应因 schema 非 required 而把字段缺失当作 null。`sessionId`/`ephemeral`/`cliVersion` 在 schema 中必需，Rust 分别是 String/bool/String。

`thread/read` Response 整份 schema SHA 已变化（旧 `de0ec065…`，新 `59bf376d…`），不能声称全协议完全相同；本次使用的关系字段、source、历史输出与 RPC 形状仍兼容。helper 只选择所需字段，新增未使用的 Thread/TurnError 字段不构成适配理由。`TurnStartParams` 的 `input` 是 required array；空数组是否合法还须结合 Rust 的 toolOutput 分支，不从 schema 单独推定。

Hook 的 `session_id` 语义是共享 session 树身份，core 明确区分 `thread_id()` 与 `session_id()`。固定源码在“全新、非 subagent root”构造时令 session ID 等于 root thread ID，因此原受控样本可继续把唯一 SessionStart/UserPromptSubmit 提供的值作为待核验 root 候选。仍必须后验核对 family/source/cwd/version 和实际 thread/read；不能把此限定构造泛化到已有恢复线程或子线程，也不删除合同中独立保存 `sessionId` 的要求。

## 可以实施的最小版本 delta

1. 同步更新 collector/helper 的固定 CLI SHA 和 CLI version；更新 collector 的 threads 行/日志 client_version、controller 的连接 metadata 与 binding `native_cli_version`。普通 CLI 路径保持不变，仍以实际进程 executable path 与固定 SHA 对照。
2. 按已经登记的 sd07 统一唯一 active nonce、固定 initial 文本及标记，保持原样本文件不变；旧测试/历史样本只在测试内部显式选旧 nonce/旧版本，不给生产入口添加兼容旧值或绕过参数。
3. 更新与本次固定版本直接相关的离线期望值/新案例登记和独立复核证据。冻结后重新核验普通 CLI 路径、摘要、socket 空闲/所有权、相关配置未变及必要离线整链；这些本身不是原生准入通过。

在本次限定接口内没有发现必须新增 RPC、切换 transport、扩大历史 schema 白名单、修改 hook binary、放松 family 检查或改变时钟的证据。sd07 尚未冻结/下发、真实 SessionStart/initial identity、模型响应、唯一发送与原服务持久历史/ACK 仍待原生实测；通用当前 UI 附着和冷启动安装的既有缺口不因版本升级自动消失。

## 可审查证据

- [本机 schema 与摘要收据](schema-receipt.json)：实验 schema 离线生成；`ThreadReadParams` SHA `dfe040c6ac71d30795b8be3f3ff232e66f362a37f883b491e5d1ea367f470db4`，`TurnStartParams` SHA `b36fb37326b1cf69f75c8b306f1f886d53a57c4b1b985e08e298e2407ea2ad02`，`ThreadReadResponse` SHA `59bf376d403bc3a696047147a1fdbbbaebd72518b3dda37f0e4709b8d61ad854`。
- [固定官方源码与下载收据](source-receipt.json)：复用原 manifest 后，从 `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`/`rust-v0.154.0` 补取缺失正文；两个不同旧模块 URL 分别 404 后改用成功固定 tree 确认新路径，没有重试不存在路径或取 main。
- [当前平台与 RAW 实现收据](platform-receipt.json)：仅本地读取；无时钟重置或时间窗口换算。
- 普通入口：[默认 UDS 探测](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/tui/src/lib.rs#L459)、[复用资格](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/tui/src/startup_orchestration.rs#L151)、[独立 app-server source](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/cli/src/main.rs#L1353)。
- 历史与字段：[完整读取实现](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server/src/request_processors/thread_processor.rs#L3315)、[Thread serializer](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server-protocol/src/protocol/v2/thread_data.rs#L201)。
- 输出：[toolOutput 检查与转换](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server/src/request_processors/turn_processor.rs#L529)、[独立 item 身份](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/session/turn_input.rs#L621)、[API 历史映射](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server-protocol/src/protocol/v2/item.rs#L890)。
- Hook：[SessionStart 构造](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/hook_runtime.rs#L151)、[UserPromptSubmit 构造](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/hook_runtime.rs#L668)、[全新 root session 身份](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/session/session.rs#L776)。
- 错误：[实际 RPC serializer](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server-protocol/src/rpc.rs#L70)。
