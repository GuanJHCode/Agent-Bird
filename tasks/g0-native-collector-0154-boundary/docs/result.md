# 0.154 collector 内部字段边界

结论：**所核查 JSONL、SQL 列及日志 span 格式兼容，未发现需要新增适配的字段阻塞。** 固定官方提交 `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`。核查对象是现有 `native_sd_control.native_ready` 与 `native_sd_collect.local_evidence/log_fields/require_root_family`；不是通用附着或原生运行验收。

| 采集器依赖 | 固定源码事实 |
| --- | --- |
| JSONL 顶层 `timestamp/type/payload` | `RolloutItemWire` 用 `type` + snake_case，`EventMsg` 字段为 `payload`；writer flatten item，UTC timestamp 为毫秒精度 `...SS.sssZ`，逐行 JSON 加换行并 flush。可选 `ordinal` 不影响现有读取。 |
| `payload.type=task_complete` | Rust 名称虽为 `TurnComplete`，但明确 `serde(rename="task_complete", alias="turn_complete")`，写出仍是 `task_complete`；无需把采集器改为只读 `turn_complete`。 |
| `turn_id/last_agent_message` | `TurnCompleteEvent` 保留 `turn_id: String`、`last_agent_message: Option<String>`。核心用当前 `turn_context.sub_id` 和实际 task 返回的消息构造事件；精确 READY 匹配仍有对应字段。新增可选 error/时间字段不会改变这两个字段。 |
| legacy/paginated 是否保留完成事件 | policy 对 `EventMsg::TurnComplete` 无条件返回 true；区别只影响其他部分 legacy 事件。不能因 paginated 模式认定完成 JSONL 不再持久化。 |
| `state_5.sqlite` 与 `logs_2.sqlite` | `SqliteConfig` 中两个文件名仍完全一致；使用其配置 home。采集器现有固定 home 的现场前提没有因本次核验扩大。 |
| `threads` 查询列 | 原 migration 建立 `id,rollout_path,source,cwd`，后续加入 `cli_version`；当前 `ThreadRow` 仍读取这些同名列。SessionSource 的 lowercase serde 与 metadata 提取支持普通服务的 `vscode` 字符串。 |
| root parent edge | `thread_spawn_edges` 仍有 `parent_thread_id,child_thread_id,status`，child 为主键；现有精确 child 查询及零行判据可运行。 |
| `logs` 查询列 | logs migrations 保留 `id,ts,ts_nanos,target,thread_id,process_uuid`，第二迁移建立 `feedback_log_body`；collector 使用的是这个新列，不依赖已移除的 message 列。 |
| log span parser | `app_server.request` 仍含 `rpc.method/rpc.transport/rpc.request_id/app_server.connection_id/api_version/client_name/client_version`。`log_db` 把 span 的格式化字段和 event 写入 `feedback_log_body`，process UUID 为 `pid:<pid>:<uuid>`。现有正则的词边界能读取带这些点前缀的字段。 |
| 所选日志 target | `codex_config::loader::layer_io` 仍会发出配置读取诊断；`codex_app_server::outgoing_message` 仍会在 `send_server_notification_to_connections` 发出 TRACE。请求上下文中的 thread/start 通知保留 request span。这些事件存在不保证每次都在界限内落盘；缺失时现有资格仍应失败。 |

源码位置：

- [完成事件 wire 名](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/protocol/src/protocol.rs#L1413)、[完成字段](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/protocol/src/protocol.rs#L2141)、[实际事件构造](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/tasks/mod.rs#L826)。
- [rollout wire](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/history/src/rollout_payload.rs#L23)、[JSONL writer](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/rollout/src/recorder.rs#L1968)、[完成事件持久化策略](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/rollout/src/policy.rs#L94)。
- [DB 文件名](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/state/src/sqlite.rs#L29)、[threads DDL](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/state/migrations/0001_threads.sql)、[cli_version DDL](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/state/migrations/0005_threads_cli_version.sql)、[spawn edges DDL](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/state/migrations/0021_thread_spawn_edges.sql)、[logs DDL](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/state/logs_migrations/0002_logs_feedback_log_body.sql)。
- [请求 span](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server/src/app_server_tracing.rs#L94)、[log_db 入库字段](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/state/src/log_db.rs#L276)、[thread/start 发送通知](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server/src/request_processors/thread_processor.rs#L1595)。

验证：`python3 -B` 在任务专属临时目录应用固定官方 SQL migration，构造明确标识的合成 JSONL 和 qualified span，调用真实现有 Python 读取函数。6 项检查通过：JSONL 接受、错误 turn 拒绝、真实 DDL 支持查询、qualified span 投影、投影后连接门禁通过、parent edge 拒绝。详见 [verification.json](verification.json)。这不是 Rust 编译或真实模型输出验证；没有启动 native/RPC，也没有读取个人 DB、会话正文或修改实现。

18 个必要源码文件按固定 tree 的 Git blob 与 SHA-256 双重校验；已有源码从其他任务目录复用。见 [source-receipt.json](source-receipt.json)。默认日志不提供完整双向附着序列的既有结论不变；本报告只确认固定受控 collector 的字段格式。不能从版本号或单次缺记录推导 API 不支持。

清理：隔离 SQLite/JSONL 夹具已由 TemporaryDirectory 清除；只读 tree 缓存已清理，保留所引用源码和本报告/收据。
