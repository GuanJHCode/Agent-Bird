# G0.1 只读元数据探针

这是独立的 Go 命令行工具。它只读取调用者明确提供的候选原服务与 thread 元数据；**G0 尚未通过**，模拟协议测试不能证明原生 TUI 接入、在线附着或自动回调。

```sh
go build -o g0-probe .
./g0-probe --socket /absolute/path/to/existing.sock --thread EXPLICIT_THREAD_ID
```

构建工具链为 Go 1.26.8；模块固定依赖 `github.com/coder/websocket v1.8.15`，校验记录见 `go.sum`。构建示例在本模块目录执行。探针不发现或猜测 socket/thread，不启动 Codex 或 daemon，不执行 shell，不读取账号文件或改变设置、信任和权限。`--socket` 必须由独立证据确认是需要调查的原服务端点；此工具不替调用者证明端点归属。

协议是 [WebSocket over Unix socket](https://learn.chatgpt.com/docs/app-server#protocol)：先 HTTP Upgrade，每个文本消息承载一条 RPC，不带 `jsonrpc` 字段。使用 [coder/websocket](https://pkg.go.dev/github.com/coder/websocket@v1.8.15) 实现 WebSocket；`app-server proxy` 的原始字节代理说明不代表 JSONL 传输。

连接后严格串行执行：

1. `initialize`：仅项目 `clientInfo.name/version` 和 `experimentalApi: true`。
2. `initialized` 通知。
3. `server/diagnostics {}`：仅提取 `process.id`。
4. `thread/read {threadId: EXPLICIT_THREAD_ID, includeTurns: false}`。

`experimentalApi` 用于上述实验接口/字段。探针不发送 `turn/start`、`thread/resume`、列表、账号、配置或审批 RPC。任何服务器 request 都终止探测，不代答。未知通知仅在有界内存中解析后丢弃，不输出其内容。

成功仅向 stdout 写一条 JSON，字段固定为：

```json
{"evidence":"metadata_only","attached":"unknown","g0":"not_verified","process":{"id":1234},"thread":{"id":"explicit-thread","sessionId":"session-tree","parentThreadId":null,"forkedFromId":null,"canAcceptDirectInput":null,"status":{"type":"notLoaded"}}}
```

`process.id` 是服务自报的 PID，未与 OS 进程创建身份、实例 nonce、Hook、原生通知或 TUI 附着信号交叉核验。`sessionId` 只关联 session tree，不能代替 `thread.id` 路由。`canAcceptDirectInput` 和 `status` 不能证明原生 TUI 在线。响应 thread ID 必须与显式输入完全一致。

输出按 schema 白名单重新构造，不输出 `preview/name/extra/cwd`、初始化路径、诊断 gauges、历史、完整响应、错误原文或环境值；不提供 raw 日志开关。服务缺失的可选身份字段显示 `null`。状态仅允许 `notLoaded/idle/systemError/active`，active flags 仅允许 `waitingOnApproval/waitingOnUserInput`。未知状态或不符合输出字段类型的元数据导致失败。

资源边界：整体 deadline 为 10 秒，覆盖建连、HTTP Upgrade 和全部 RPC；最多 32 条入站 WebSocket 数据消息，每条最多 64 KiB，JSON 深度最多 32。socket 参数最多 4096 字节且必须是绝对路径；thread/关联身份最多 128 字节，仅接受 ASCII 字母、数字、`-`、`_`，包含 Codex UUID 格式。操作系统还会施加更短的 Unix socket 地址限制。RPC ID 严格匹配，拒绝重复 JSON key、批处理、二进制数据消息和一条消息内多条 JSON。退出关闭 socket 和 HTTP transport；不重试、不改端点、不回退到其他协议。

参数、连接或协议失败时 stdout 为空，stderr 仅固定的 `g0-probe: CATEGORY`，不会拼接服务器或参数原文。输出设备故障可能留下部分已过滤的 JSON，调用者必须检查退出码。退出码 `0` 只代表只读元数据采集成功，`1` 代表探测或输出失败，`2` 代表参数错误。可能类别为 `invalid_arguments`、`connect_failed`、`timeout`、`transport_error`、`protocol_error`、`response_id_mismatch`、`server_request`、`rpc_error`、`message_limit`、`invalid_metadata`、`thread_identity_mismatch`、`output_failed`。

```sh
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
GOOS=windows GOARCH=amd64 go build -o /path/to/task/tmp/g0-probe.exe .
```

测试创建真实的本地 Unix WebSocket 合成服务，检查请求顺序、白名单参数、canary 脱敏、错误和资源边界。macOS 测试使用 `/tmp/g0-probe-*/tmp/s` 短 socket 路径（握手测试使用根目录下 `s`），以避免 worktree 长路径超过 `sockaddr_un` 限制；其他系统使用原生临时目录。测试关闭服务/连接后自动删除自己创建的根目录；意外中断可能留下这些目录，应按任务归属盘点后处理。测试不连接真实 Codex 会话，不调用模型。

Windows 仅执行交叉编译检查；没有执行原生 Windows Unix socket 或 TUI 测试，不声明运行支持。真实原服务证据链、双会话隔离、生命周期、回调与恢复需要另外的原生验收；私有实验规格不随源码发布。

## G0 Hook 身份记录器（macOS 实验）

`cmd/g0-hook` 仅用于G0.1的身份取证。它不安装或信任Hook，不调用Codex、不启动后台服务、不实现回调，不证明TUI附着。

```sh
go build -o /absolute/task/path/g0-hook ./cmd/g0-hook
/absolute/task/path/g0-hook --output-dir /absolute/precreated/event-directory --nonce experiment-id
go test -race -count=1 ./cmd/g0-hook
go vet ./cmd/g0-hook
```

输入是原生Hook经stdin提供的单个JSON对象，最大64KiB，输入等待预算2秒。支持SessionStart、UserPromptSubmit、SessionEnd：session_id/事件必需，SessionStart需要已知source，UserPromptSubmit需要turn_id；可选身份字段缺失不编造。ID和nonce限定128字节ASCII字母、数字、`-`、`_`；未知文本字段仅在有界内存中读取后丢弃，重复顶层key或畸形JSON拒绝。Stop的成功输出协议不同，本工具不注册它。

记录只包含上述身份字段、本实验nonce、记录器PID/PPID及本机时间。stdout始终为空；不保存prompt、transcript、cwd、模型/账号/权限设置或完整输入，stderr仅固定错误类别。PID/PPID均不是TUI归属证明。输出目录必须预先存在，逐层使用目录句柄与身份核对，拒绝静态symlink，随机文件名以O_EXCL创建0600文件，避免覆盖既有文件；文件和目录同步成功才exit 0。失败不报告成功，并在仍能确认自身文件身份时回收不完整记录；无法确认时不删除陌生文件。没有对任意同用户恶意写入者的隔离承诺。

目录/参数错误分别exit 1/2，输入/IO失败exit 1。正常Hook定义额外使用原生3秒timeout；慢磁盘或进程中断不承诺原子恢复，实验收集必须核对Hook退出状态及完整JSON，不能把任意存在文件当作成功事件。没有生产状态库、重放、ACK或完整故障恢复语义。

本轮仅测试macOS，包括真实临时目录、并发文件创建和仅在测试子进程内设置RLIMIT_FSIZE产生的写失败。Windows运行及对应故障注入延期。个人机器的 Hook 配置和原生验收记录不随源码发布；安装 Hook 仍需用户遵循正常信任流程。
