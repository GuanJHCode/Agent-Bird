# Owner-helper second frontend

本交付定义 `owner-helper` 的最小受控接线合同。它服务于一个已经由 TUI 持有的 backend lease：helper 在同一个 private Unix listener 上建立第二条独立 WebSocket，服务端复用现有 backend，不创建第二个 backend，也不把 helper 当作 TUI。

## Admission

`OwnerLease` 由服务端在 TUI 连接建立且 backend 已通过身份检查后构造。它冻结以下边界：`profile_id`、owner context 摘要、`lease_id`、TUI `owner_connection_id`、epoch、明确的 owner `thread_id`、private socket、backend PID/birth，以及经过服务端检查的 helper `pid/uid/birth/executable/executable_sha256/source_sha256`。

`OwnerHelperGrant` 必须逐字段匹配当前 lease，并且 grant 中的 helper 身份必须等于服务端预先检查的 `HelperIdentity`。`role` 只能是 `owner-helper`，版本只能是 1。lease 关闭、socket 不存在、socket 是 symlink、或 socket 不是 Unix socket 时均 fail closed。helper 关闭不会触碰 owner TUI 或 backend 进程；`owner_closed()` 会立即关闭当前 lease 的全部 helper，服务层应在结束序列中等待这些 EOF 后再销毁 backend。

此模块只打开第二条 `asyncio.open_unix_connection` 并完成无压缩 RFC 6455 握手。它不共享 owner 的 reader/writer，因此不会把两个 frontend 的帧交叉读取；也没有 backend spawn、kill、socket unlink 或 profile lock 的副作用。

## RPC gate

helper 发出的 JSON-RPC 只允许有限只读集合：`thread/read`、`thread/turns/list`、`thread/subscribe`、`thread/unsubscribe`（参数必须只含精确的 owner `threadId`），以及无参数的 `initialize`、`initialized`、`skills/list`。`turn/start`、`turn/steer`、`thread/start`、`thread/resume`、`thread/fork`、`thread/inject_items`、`command/exec`、`prompt` 和未知方法明确拒绝。

收到 backend response 或通知时，解析器只接受有限大小、无压缩、服务端未掩码的文本 JSON-RPC 帧，支持文本分片和同一读取中的粘连帧；响应必须保持 owner thread，通知只允许固定的 `thread/status/changed`、`thread/updated`、`thread/tokenUsage/updated`、`thread/goal/cleared`、`remoteControl/status/changed` 和 `mcpServer/startupStatus/updated` 集合。压缩位、服务端掩码、二进制/不支持 opcode、超限帧、畸形 JSON、未知通知和不同 thread 的结果均在交付前拒绝。这个边界是执行 gate，不是 observer 记录。后续如需写操作，必须增加独立、具体的合同和测试，不能通过扩大当前 allowlist 实现。

## 接线要求与范围

`activation_service.py` 的后续接线应在 public role branch 中完成 admission，然后调用 `OwnerLease.open_helper()`；它必须保留原 TUI 的 `binding.tui` 身份和 service/backend 的独立身份，helper 自身使用独立的 Python executable/source pin。helper 的 grant、lease、thread、epoch、profile 和 backend receipt 认证不能由 UI 当前显示或任意 UID/thread id 推断。

本交付目前只包含合成 UDS/WebSocket server、身份/lease gate 和解析器测试。没有修改 `activation_service.py`，没有连接真实 auth/Keychain/native/model/launchctl，也没有声称 G0 原生验收通过。Guardian 冻结的生命周期接线完成后，服务层还必须复用本合同的第二 WS、helper EOF 和 owner-thread gate。

## Service wiring

service 的 helper role admission 只接受 manifest 中冻结的 helper executable/source pins，并把 grant 顶层字段、实际 peer PID/birth/executable、active backend PID/birth、private socket 的 dev/ino/uid/mode 和 owner context digest 逐项重验。owner ready 之后才登记 lease；helper 不会取得 profile lock，也不会触发 backend spawn。`ProxyServer` 为 helper 连接安装双向 wire gate：完整 native WebSocket 消息在转发前校验，request id 与 response 一一关联，server request、普通 prompt、跨 thread 和非法 `turn/start` 均拒绝。owner lease 结束先关闭 helper，再执行 backend cleanup。

允许的 `turn/start` 只有精确的 `threadId`、`input: []` 和 `toolOutput: {name: "g0_delivery", namespace: "orchestration", output: <validated intent>}`；intent 的 version、事件 identity、payload hash 和 owner thread 也会重验。helper 输出不会转换成普通 user prompt。


`OwnerHelperConnection.send_rpc()` 是 native standalone `make_request()` 形状的可调用适配器：调用方传入其生成的 `{id, method, params}` native packet，模块执行同一 envelope gate 后写入 WebSocket。它不接受带 `jsonrpc` 的 JSON-RPC 变体，也不替代 `native_standalone` 的 owner-context、Go/ACK 或历史审计；后续 controller 只需把现有 `Rpc.call` 的 packet/response reader 接到该 channel，不能绕过 gate。
