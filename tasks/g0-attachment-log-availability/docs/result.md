# 默认 app-server 日志的附着输入可得性

日期：2026-09-12。只读核验；未启动 TUI、RPC 或模型，未读取个人 SQLite 日志或历史，未改变配置、权限或日志开关。

结论：**固定 0.154.0 的现有 app-server 日志没有提供已证明完整、同 connection 的双向 request/response 输入，当前不能据此准入附着 FSM。** 这不是对所有无 proxy 路线的否定，也不复用旧报告已经撤回的“切换没有事件”结论。

固定官方提交为 `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`（`rust-v0.154.0`）。新增必要源码与 SHA-256 见 [source-receipt.json](source-receipt.json)。复用 `tasks/g0-native-0154-contract/data/source/codex-rs/app-server/src/lib.rs`、`request_processors/thread_processor.rs` 以及生成的 `SkillsListParams` schema；没有重复下载这些文件。所有结论为固定源码分析和已归档受限证据的核对，并非新版本原生运行验收。

## 实际可用信号

- SQLite `log_db::default_filter()` 默认是 TRACE，`codex_app_server` 未被目标过滤器降级。不能因为 stderr 的 `RUST_LOG` 过滤就认定默认 SQLite 没有 TRACE；默认日志层只在 state DB 初始化成功时安装。见 [log_db.rs:57](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/state/src/log_db.rs#L57)、[app-server/lib.rs:687](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server/src/lib.rs#L687)。
- JSON-RPC 请求到达处理器时，有 method、`connection_id` 与 `request_id`；请求 span 还有 transport、client name/version 和 API version。固定服务进程日志 UUID 可区分服务重启。见 [message_processor.rs:610](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server/src/message_processor.rs#L610)、[app_server_tracing.rs:94](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server/src/app_server_tracing.rs#L94)。
- 因而正常保留下来的请求日志可以表明某 connection 曾发出 `skills/list`。请求到达顺序可作局部观察，但不能直接当作 UI render/commit 或请求成功完成的顺序。`SkillsListParams` 只有 `cwds`、`forceReload`，没有 thread ID。

## 尚缺的输入与完整性

1. **请求参数的精确 thread ID。** `process_request` 的 TRACE 只打印 method 和两个 ID，不打印 params；公共 request span 也没有 `thread.id`。其他线程事件可能带 thread_id，不代表每个 `thread/read`、resume、fork、Apps 请求均有精确的 request→目标 thread 映射。新收到的 `skills/list` 连 `cwds/forceReload` 都不在这条请求日志内。
2. **每个请求对应的成功/失败响应及边界。** `send_response_as_inner` 构造带 id/result 的真实 response 并加入发送队列，但成功路径没有对应日志；`send_error_inner` 同样没有稳定的错误 response 投影。函数内 `<- response: ...` 是客户端回答服务端请求的分支，不能冒充服务端响应 TUI 的 `thread/read` 等请求。见 [outgoing_message.rs:641](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server/src/outgoing_message.rs#L641)、[outgoing_message.rs:446](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server/src/outgoing_message.rs#L446)。
3. **socket 层没有补齐同 connection 的双向记录。** 普通 UDS 升级后使用 `run_websocket_connection`；正常接收只反序列化并 enqueue，正常发送只序列化并 `send`。这两侧没有成功请求体/响应体记录；connection opened/closed 被传入内部队列，也没有在该路径形成逐条 SQLite 事件。见 [unix_socket.rs:133](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server-transport/src/transport/unix_socket.rs#L133)、[transport/mod.rs:205](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server-transport/src/transport/mod.rs#L205)、[websocket.rs:172](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server-transport/src/transport/websocket.rs#L172)、[websocket.rs:289](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server-transport/src/transport/websocket.rs#L289)。依赖库可能另有低层诊断，未建立其与 connection/thread 的白名单映射，不能将其当作已有合格输入。
4. **无法从日志空白证明未发生。** SQLite sink 容量 2048、batch 512、默认刷新间隔 10 秒；`try_send` 满或关闭时仅调用 drop 遥测。所查路径没有附着消费者可用的逐 connection 连续序号或无丢失水位。因此即使新增一份投影，仍要证明切换/响应/断连没有静默漏项。见 [log_db.rs:53](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/state/src/log_db.rs#L53)、[log_db.rs:170](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/state/src/log_db.rs#L170)。

## 默认与额外开关

`RUST_LOG` 及 `LOG_FORMAT=json` 控制 stderr 级别和格式；默认 stderr 不是完整 RPC transcript。显式启用 TRACE/JSON 可以增加 span 生命周期等输出，但不能生成上述代码没有记录的 params、成功 response 或 UI generation，也不能修复 SQLite 队列缺项。

已有附着报告讨论的 `CODEX_TUI_RECORD_SESSION` 属于额外 TUI 记录路线，不是本题原 app-server 默认日志；本次没有启用或把旧版本记录行为视为 0.154 已验证事实。Apps 条件、异步迟到、skills 缺 thread 与显示提交时序仍保留原报告的验证边界。

## 受控历史证据及下一项可验证证据

只读取三个已归档受限文件：`docs/implementation/g0/daemon/native-protocol-fields.json` 5 行，`native-sd-qual-02-controller-evidence.json` 和 `native-sd-qual-03-controller-evidence.json` 各 11 行，总计 27 行。三者只保留 transport、client name/version、connection ID、API version 及部分 method，没有 request ID、response 方向或按请求绑定的参数 thread ID。它们证明曾可读取原服务协议元数据；由于投影本来丢弃其他字段，不能反推原日志绝无那些字段，也不能据此恢复完整双向序列。

重新打开该具体候选，需要先指出一个在普通入口、现有授权开关下实际存在的日志源，并用新的独立受控样本证明：同一服务/connection/request ID 的请求目标 thread、响应成功或失败及完成顺序、skills 刷新前后序列、连接开闭均可白名单投影，且缺项可检测。**当前仅增加投影器无法补齐未记录的字段，尚不具备开始 FSM 实现或声称无需 proxy 已解决附着的证据。** 本次未安排这个原生样本。

验证：固定源码收据逐文件 SHA-256 检查；静态检查请求标记、request span、response 发送函数、UDS→WebSocket 输入/输出链和 SQLite 过滤/入队链。没有 Rust 构建或运行时测试；此任务无源码修改。

清理：本任务没有可丢弃的临时文件。`data/source/` 为上述结论的固定源码证据，`docs/` 为交付物；其他任务文件未修改。
