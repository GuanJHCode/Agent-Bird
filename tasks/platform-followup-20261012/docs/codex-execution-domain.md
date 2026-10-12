# Codex 独立执行域接口核查

核查对象固定为 upstream `openai/codex` commit
`6b9826e3aa83b1a5947db50f4332cb9c65f1b340`（Codex 0.154.0）。本记录只读
固定 Git tree `tasks/platform-followup-20261012/data/upstream/tree.json`；不启动
exec-server，不操作账号、fork 或安装。

## 已存在的接口

- `codex-rs/exec-server-protocol/src/protocol.rs`，Git blob
  `64b9e2a346a31318a946269330c0d26df0820ac1`：`process/start` 的
  `ExecParams` 包含 `argv`、executor-native `cwd`、`env` 和可选
  `FileSystemSandboxContext`；`ExecResponse` 回报 `ProcessSandboxType`，包括
  `None` 与 `MacosSeatbelt`。
- `codex-rs/exec-server/src/local_process.rs`，Git blob
  `c728c7416e648b1b9e30d01231e5b9eb9efbbb1d`：executor 按请求准备执行，随后
  调用 `codex_sandboxing::spawn_process`。因此 sandbox 的实际生效位置是
  executor，不是请求方 Worker。
- `codex-rs/exec-server/src/client_api.rs`，Git blob
  `e870f124717a7a52624bfe0840d4d55f2822e5b7`：私有
  `ExecServerTransportParams` 支持 `WebSocketUrl`、`NoiseRendezvous` 与
  `StdioCommand`。没有 Unix-domain-socket transport。
- `codex-rs/exec-server/src/remote/direct.rs`，Git blob
  `02365f58bb111a79605eeebe5f3c75c6f6e19707`：direct WebSocket 对非 loopback
  强制 `wss`；loopback 可用 `ws`。Noise 是另一条经 registry 授权的远端路径。
- `codex-rs/cli/src/exec_server_args_tests.rs`，Git blob
  `c58b3b9945778f1c542667dff362dcdb4fbe996a`：server CLI 形状为
  `codex exec-server --remote <registry> --environment-id <id>`，可选
  `--remote-transport noise|direct`；direct 不支持 forwarding。

## 结论与实施边界

远端 exec-server 可以成为不继承 Worker Seatbelt 的独立执行域，前提是 server
先由未受 Worker Seatbelt 的原 Host 启动并持有。`StdioCommand` 由 Worker 派生，
因此仍继承其 Seatbelt，不能解决此问题。

固定版本没有公开的 Worker/TUI client-side endpoint 或 environment binding 配置。
上述 transport API 是 `pub(crate)`，且 `StdioCommand` 标为未使用；不能仅传一个
Unix socket 或调整 sandbox 字段把现有 Worker 工具调用改送远端。

可实施路线是受控 fork 或上游接口新增经认证的 environment binding，并在 Worker
启动时固定：registry/endpoint、environment ID、Noise key 或 TLS 认证材料、预期
executor build identity 与 capabilities、每次调用的文件系统/网络 sandbox intent。
握手必须核验 `environment/info`；远端需回传实际 `ProcessSandboxType`，并另验
process identity、原始 Wait/tree 和网络策略。完成这些接线和 Darwin 原生验收前，
Codex Worker 执行能力保持 `blocked`。
