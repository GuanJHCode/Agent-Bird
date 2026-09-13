# Owner-helper second frontend

`owner-helper` 是 TUI 已持有 backend lease 时的受控第二 frontend。service 在同一个 private Unix listener 上建立第二条 raw UDS transport，由 `ProxyServer` 统一转发唯一的 HTTP/WebSocket upgrade；service 不创建第二个 backend，也不把 helper 当作 TUI。

## Admission and lifecycle

owner ready 后 service 登记 `OwnerLease`，冻结 `profile_id`、owner-context digest、`lease_id`、TUI `owner_connection_id`、epoch、owner `thread_id`、private socket、backend PID/birth，以及 backend socket 的 dev/ino/uid/mode。helper grant 必须是 version 1、role `owner-helper`，并逐字段匹配当前 lease。

manifest 的 `owner_helper` policy 必须包含 canonical `source_path`、`source_sha256`、helper executable 和 executable SHA。service 启动时、每次 helper admission 时和 immutable `file_pins` 检查时都复核 source 文件；helper grant 的 source SHA 不能自行声明通过。实际 peer PID、UID、birth、executable 和 backend peer 会再次核验。

helper transport 不取得 profile lock、不启动或停止 backend。helper 关闭只关闭自身连接；owner frontend 结束时 lease 先关闭所有 helper 并等待 EOF，再进入 backend cleanup。receipt 中的 native backend record 保持一条，helper 只作为关联 owner lease 的安全摘要记录。

## Native wire gate

native wire 使用 `{id, method, params}`，不带 `jsonrpc` 字段。允许的 request envelope 是：

- `initialize`：固定 `clientInfo` 与 `capabilities`。
- `initialized`：无 `id`、无额外字段的 notification。
- `server/diagnostics`、`remoteControl/status/read`：空 params。
- `thread/read`：仅 `{threadId: owner_thread_id, includeTurns: true}`。
- `turn/start`：仅 `{threadId, input: [], toolOutput}`，其中 `toolOutput` 必须是 `name: "g0_delivery"`、`namespace: "orchestration"`，且 output 是完整、校验过的 owner intent envelope。

普通 prompt、配置/model override、跨 thread、`turn/steer`、fork、inject、command/exec、未知方法均拒绝。`turn/start` 只有上述 toolOutput 形状可用，不会转换成 user prompt。

service 的双向 `NativeWebSocketGate` 缓冲 HTTP upgrade、分片和粘连帧，完整消息通过 envelope、owner thread、pending request-id 和 toolOutput 校验后才转发。服务端 request、错误方向的 mask、RSV/压缩位、二进制/未知 opcode、超限、重复 JSON key、畸形 JSON 和未知通知 fail closed。允许的 delivery 生命周期通知是 `turn/started`、`item/started`、`item/completed`、`turn/completed`，另保留已审的状态通知；turn/item 的精确 envelope 和 owner thread 必须匹配。顶层可带唯一可选的 `emittedAtMs`，必须是非 bool 的 i64 范围整数；其它顶层 metadata 仍拒绝。固定 native 来源为 `tasks/g0-native-0154-contract/data/source/codex-rs/app-server-protocol/src/protocol/v2/{turn.rs,item.rs}` 的通知结构。兼容层还允许固定的 owner-scoped `item/agentMessage/delta`（精确 `threadId`、`turnId`、`itemId`、`delta`）以及当前 profile 的只读 `account/updated`、`account/rateLimits/updated`（精确 v2 payload shape）；这些正文只做入站校验，不写入 receipt。`thread/started`、带 `id` 的 server request、跨 owner thread 和未知通知仍拒绝。Account 结构来自固定 0.154 官方提交 `6b9826e3aa83b1a5947db50f4332cb9c65f1b340` 的 `app-server-protocol/src/protocol/v2/account.rs`（`https://raw.githubusercontent.com/openai/codex/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server-protocol/src/protocol/v2/account.rs`, SHA-256 `22d4ec67a5979d33cbe2e40b53c7afbdd1bbed935a54b2284e9b2de11f4b590c`）。响应允许固定的状态通知集合，并要求所有带 thread 的通知/结果匹配 owner thread。

`OwnerHelperConnection.send_rpc()` 是 native standalone `make_request()` 形状的可调用适配器：现有 controller 可把 `{id, method, params}` packet 和 response reader 接到此 channel。它不替代 `native_standalone` 的 owner-context、Go/ACK、历史审计或真实 native 验收，也不读取模型正文。

## Verification and limits

本交付包含真实合成 UDS、HTTP 101、双向 WebSocket、第二 frontend、owner EOF/helper EOF 和 production `ActivationService.from_manifest → backend process → ready → OwnerLease → helper` pipeline 测试。production fixture 实际接收并回传一次 owner-scoped `turn/start`，验证完整 turn/item lifecycle；普通 prompt 与跨 thread 请求在 service gate 被拒绝且 backend receipt 没有相应请求。manifest 中的 `owner_helper.source_path` 是 gate library 的 immutable code pin，不是对任意外部 helper 脚本身份的声明；若未来部署独立 helper entrypoint，必须另行冻结其 executable/source pins。测试使用 dummy backend/子进程，不连接真实 auth、Keychain、模型或 launchctl；通过这些测试不能宣称 G0 原生验收通过。
