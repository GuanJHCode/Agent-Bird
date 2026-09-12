# 有界原生 RPC 错误诊断

原 helper 对匹配的原生 error 只保留 native_rpc_error，丢失 code/method/message 摘要；prepare 在 send-attempt 前还不会写观察文件。controller 旧版只保存 stdout/stderr 字节数与 SHA。这使不同原生拒绝无法从已有记录区分。本修正不新增请求或重试，只在 rejected/uncertain 且 reason=native_rpc_error 时加入可选 rpc_error：

```text
{method, request_id, code, message_bytes, message_sha256}
```

method 只能来自本地已通过白名单的 initialize、server/diagnostics、remoteControl/status/read、thread/read、turn/start；request_id 为本地请求且响应已精确匹配的整数 1..10000。code 必须为有符号 64 位整数，bool 不接受。message_bytes 是原 error.message 的 UTF-8 字节数 0..65536，message_sha256 是对应字节的 64 位小写 SHA-256；空字符串有效。原 error 仅接受 code/message 与可选 data；data 不导出，正文、请求参数、配置和其他字段也不导出。畸形结构、未知 error 字段、非法 UTF-8 或非整数 code 返回固定 invalid_rpc_error，不附加投影。

原帧大小/总量、响应 ID 和连续时钟检查仍先执行；超窗响应不能借诊断获得成功。初始化及 prepare 错误由 main 输出这份受限摘要；已有发送意图时，main 或 observe 把相同摘要写入既有观察记录。send-attempt/O_EXCL 与原 120 秒窗口不变，同 case 重发仍拒绝，absence_proof 和 resend_authorized 保持 false。

controller 配套由另一写入者实施 helper_rpc_error_fields(raw:bytes)：对不超过 65536 字节的 stdout 严格 JSON 解码，独立核版本/状态/reason/false 标志和上述五字段，不相信任意 stdout 内容。helper_calls 保留原 bytes/hash/exit，仅额外保存合格 rpc_error；非零且无合格摘要只记固定 unavailable。此函数不参与放宽成功准入。

3 项新增回归先产生 6 个 assertion RED，再全部 GREEN；覆盖 UTF-8 计数、code 类型/范围、畸形消息、额外字段、错响应 ID，以及真实 Case/main/Rpc 路径中的 initialize、prepare、send、observe 错误与不重发。网络、身份和 Go 只在这些单元场景使用固定替身；另含真实 Go 副本的完整 prepare 整链仍通过。最终 30 项通过（0.158s），见 rpc-error-red、rpc-error-targeted-green、rpc-error-green 日志。没有调用原生服务或模型；本结果不授予原生资格。
