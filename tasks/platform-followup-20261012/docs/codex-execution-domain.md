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

远端 executor 路线需要受控 fork 或上游接口新增经认证的 environment binding，并在 Worker
启动时固定：registry/endpoint、environment ID、Noise key 或 TLS 认证材料、预期
executor build identity 与 capabilities、每次调用的文件系统/网络 sandbox intent。
握手必须核验 `environment/info`；远端需回传实际 `ProcessSandboxType`，并另验
process identity、原始 Wait/tree 和网络策略。完成这些接线和 Darwin 原生验收前，
Codex Worker 执行能力保持 `blocked`。

## 本阶段采用的插件扩展路线

用户明确要求不修改 Codex 源码，因此本阶段不实施上述远端 executor 路线。固定二进制
另有公开 app-server `dynamicTools` 扩展：Host 在 `initialize` 声明
`capabilities.experimentalApi:true`，在 `thread/start.dynamicTools` 注册固定
`agent_bird` namespace；原生以 JSON-RPC request `item/tool/call` 请求 Host 执行。
Host 用同一个 opaque request ID 回传 `contentItems` 和 `success`。
无需 Worker 派生执行器、MCP 子进程或 socket，不改变原 Seatbelt、原生 read-only policy、
账号引用及状态保护。内置原生命令仍可能触发已知 nested Seatbelt EPERM；扩展工具不能
被描述成已修复内置 shell。

固定官方源码 commit 均为上文 `6b9826…`，核心证据为：

| 固定源码 | Git blob | 结论 |
| --- | --- | --- |
| `app-server-protocol/src/protocol/common.rs` | `a718641f7a714dbb51daa1a941fb280c55fee59a` | `item/tool/call` 是 request |
| `app-server-protocol/src/protocol/v2/thread.rs` | `28ca20f7b4750d05bcbff175ae74d96cd830d3fe` | start 的实验字段；resume 无此字段 |
| `protocol/src/dynamic_tools.rs` | `bbe92b8fe0c75802cc1784d900926612b170a6b5` | function/namespace tool union |
| `core/src/session/mod.rs` | `8ffc144d028b4f339d0ff323bbc804659406bada` | 工具定义保存到 session metadata，resume 恢复原集合 |
| `core/src/thread_manager.rs` | `eb85a048b7e6c86d23bc67ab2088da9d9ebb6e38` | resume 传空新集合，不覆写旧集合 |
| `core/src/tools/handlers/dynamic.rs` | `f2b7860d06385301ebefa9bdd70eb0a43a8639ad` | response 逐字段成为 item；success false 映射 failed |

所有路径相对 `codex-rs/`。可从
[固定官方源码](https://github.com/openai/codex/tree/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs)
核验。原始 schema 本地证据保留；产品只嵌入最小裁剪 `host_tool_shapes.json`，记录各源
SHA256，提取脚本先校验这四份固定输入再生成。

`codexsession.HostTools` 当前仅为私有 Go 回调，不构成公共授权。工具集合固定为
read_file/list_files，显式 Writable 才加入 write_file。契约 hash 是
`SHA256("agent-bird/codex-host-tools/v1\n" || encoding/json(tool_inventory))`，小写 hex，
JSON 无尾换行，map key 按 Go 标准编码排序。hash 不是签名或能力；续接调用方必须先验证
原持久化授权，再传匹配的 `ResumeContractSHA256`。

回调只在已绑定 thread/turn 后由唯一 reducer 串行执行；提前请求有界排队，RPC ID 和
callId 分别去重；参数上限64KiB、文本结果上限256KiB、总工具数256。完成 item 必须与
实际 callback 响应的 namespace/tool/arguments/contentItems/success 一致，缺失或伪造
结果不能完成。未启用 HostTools 的旧会话仍关闭 experimental API。

验证：session package 60顶层/505 named PASS；最终增加 failure-result 状态核验及正向
回归后，受影响的 HostTools `-race -count=1` 为4顶层/17 named PASS，均0FAIL/0SKIP。
原始 JSON：`data/host-tools-session.jsonl`（SHA256
`54581fb6c8cbec06064ee59f0a571cf72b62fe124b32922f003a3374c0403b76`），
`data/host-tools-race.jsonl`（SHA256
`da0c9a059510a9c863a48bef754d71f8e5dff57e593deaaa348eb87efb33ee5d`）。
独立只读审查无阻断问题；所建议的 success:false 回归已补。

上述均为 net.Pipe 合成协议验证，尚不是原生 Provider/TUI 验收。文件 Broker、Host
授权及持久化接线仍在实施；公共入口继续 blocked。
