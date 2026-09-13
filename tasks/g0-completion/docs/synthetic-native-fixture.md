# 私有合成 Responses 验收夹具

基线 `6bfb87f906302179b4286fb3a2a183371afbdc4d`。本交付仅准备隔离上下文并服务本任务自有 loopback HTTP；没有执行真实 Codex、模型、launchctl、账号/Keychain 访问或全局配置变更。它可以验证原生客户端后续是否正确消费合成响应，不能作为真实 AI 行为或完整 G0 验收证据。真实 native/controller 的编排与准入仍由主会话负责。

`synthetic_responses.SyntheticEndpoint` 在构造时实际 bind/listen `127.0.0.1:0`，不接收任意 hostname/端口。调用者持续持有该 FD。冻结与复查包含 owner PID/birth、FD dev/ino/mode 和实际地址；没有 close/rebind 间隙。`synthetic_native_fixture.prepare(endpoint, spec, executable=...)` 只接受这种已持有的 listener，新建 task-owned HOME、CODEX_HOME、空 workspace、SBPL、隔离 manifest 和配置/source/executable SHA。`executable` 仅被读取摘要和校验，没有执行。默认只用于 Python dummy；后续 root 可显式传冻结 native 路径。

准备完成后，`prepared.start_endpoint()` 重新验证 listener、所有 profile 目录身份以及磁盘 source/config/manifest 摘要，再开始服务。该接口只启动本地 HTTP 线程，没有调用 native/controller 的路径；不会自行决定真实实验准入。context 可供主控制器复用：`prepared.context`，隔离 manifest 路径在 `prepared.manifest['isolation_manifest']`。控制器必须持续保留 endpoint 对象并使用这个 proof，在启动受控客户端前再次 `prepared.verify()`；不能仅凭 IsolationSpec 的端口标量声称 listener 归属成立。显式退出顺序为 `prepared.write_receipt()`（新 owner-only result，不覆盖）、`endpoint.close()`，保留 fixture/task 记录供 root 归档和核对。测试自行删除其全新临时根，不自动清理别人的文件。

配置使用 `gpt-5.6-luna`/medium 作为客户端能力标识，`model_provider=synthetic`、Responses HTTP、`requires_openai_auth=false`、`supports_websockets=false`，不创建 auth.json、不携带 API key。自有端口场景将 request/stream retries 设为 0，stream idle 10 秒。只有新建的空 workspace 在冻结前写入 exact trust。默认 IsolationSpec 的 `owned_loopback_port=None` 保留原配置地址 `127.0.0.1:1` 和全 IP egress deny；布尔、非整数和越界端口都拒绝。可选端口通过原稳定 manifest loader 传给 backend plan，未修改旧 native_activation_fixture.py。

## 合成协议与预算

请求只接受 `POST /responses` 或 `/v1/responses`、JSON、`stream=true`、模型标识 `gpt-5.6-luna`、array input。第一阶段 input 中必须有 `G0_SYNTHETIC_READY`，返回固定 `READY`；第二阶段必须有 `G0_SYNTHETIC_TOOL_RESULT`，返回固定 `SYNTHETIC_COMPLETE`。input marker 可被原生包装在工具结果/消息结构内，端点只查找标记，绝不把内容写入收据。它不生成工具调用、不执行工具，也不制造业务 ACK。

每个响应依次发 `response.created`、`response.output_item.added`、`response.output_text.delta`、`response.output_item.done`、`response.completed`。最后一个事件含 response id、`end_turn=true`、零 synthetic usage；message 为 assistant/output_text。固定源码依据：

- `codex-api/src/sse/responses.rs:118` 定义 completed 必需 id 与可选 usage；`:169` 定义事件字段；`:357`、`:365`、`:408`、`:483`、`:509` 处理所用五类事件。
- 复用原已归档 `protocol/src/models.rs:979` 的 ResponseItem::Message 与 `:871` 的 OutputText。
- `model-provider-info/src/lib.rs:131` 起定义 retry/idle/no-auth/websocket 字段；`codex-client/src/retry.rs:89` 的 `0..=max_attempts` 保证 retry 0 仍执行首发，不增加重试。

三份新增正文与复用文件摘要在 `synthetic-source-receipt.json`，全部固定官方 commit `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`。未用当前文档替换固定版本协议。

总服务窗口从 start 起最多原 120 秒，不因请求重置；单个请求 header/body 收读最多原 10 秒。header 16 KiB、body 1 MiB、嵌套深度 32、最多 8 个连接尝试、最多 2 个 accepted 请求；失败或阶段不符不推进状态，不自动重发。到期停止服务线程，listener 仍由调用者持有直到显式 close，以保留端口归属。部分 HTTP 帧不会让 close 等满原收读窗口。Authorization/API-key/Cookie 请求拒绝；只保留字节数、SHA、阶段、status、计数，无 body/header/env/auth 原文。`external_model_calls=0`、`synthetic_only=true` 始终明确。

## macOS SBPL 实证

原试验 `(remote tcp "127.0.0.1:port")` 编译失败，exit 65：host must be * or localhost。最终规则为 `(remote tcp4 "localhost:<owned-port>")`。真实 `/usr/bin/sandbox-exec` Python dummy 依次验证：

- 自有 `127.0.0.1:port` listener connect 成功。
- 另一自有 IPv4 port、同 port 的自有 `::1` listener、`127.0.0.2:port` 均为 EPERM。
- 任务外自建 synthetic credential 文件 read/write 仍为 EPERM。

没有放行 IPv6、任意主机、IP bind/inbound，也没有修改防火墙或全局网络。原已测 Keychain/Mach deny 规则保持；本轮没有调用真实 Keychain。SO_ACCEPTCONN 在本机返回 ENOPROTOOPT，故不以该 getter 判定 listener；只有构造器自己成功执行 bind/listen 的对象可进入 prepare，并持续校验相同 FD 身份。

## 新鲜证据

初始 RED：16 failed/0.13s（新模块/字段未实现，日志 `synthetic-red.log`；重复 oversize 参数名已缩写）。后续真实边界 RED：2 failed/10.08s，partial header close 等满原 10 秒，且缺 owner birth；见 `synthetic-boundary-red.log`。

最终旧新同进程命令：

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m pytest -q -p no:cacheprovider \
  tasks/g0-completion/tests/test_synthetic_native_fixture.py \
  tasks/g0-auth-preserving-activation/tests/test_auth_isolation.py \
  tasks/g0-auth-preserving-activation/tests/test_native_activation_fixture.py \
  tasks/g0-proxy-continuation/cold-start/tests --tb=short
```

结果 **86 passed in 15.00s**，`synthetic-green.log`。新测试 18 项，真实 HTTP/SSE、沙箱正负端口、无 auth 配置、digest 漂移、closed listener、原 deadline 与 partial frame cleanup 均在内。公开源码 tree 查询缓存已删除；源码正文、必要 receipt、RED/GREEN 与交付文件保留。
