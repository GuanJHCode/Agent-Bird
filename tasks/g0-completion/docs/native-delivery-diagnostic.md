# native-delivery-01 安全诊断增量

原 case 原样 UNKNOWN，未重新运行或改写。原外层 message SHA `f5306bf8b4d23c28d2b35a38443b20da623168f2836706b03e10ed8a41405d96` 精确等于 `SHA256("client stage failed: helper:IncompleteReadError")`。原 Go prepare/claim/owner binding 已存在，服务只有 owner connection、无 helper record；可以定位 helper public WebSocket 握手遭 EOF，不能据此断定具体 admission 拒绝点。root 另已核对合成 profile/source 的 7 项 hash 未变。

controller 保留子进程原安全 error 对象到 `failure.child_error`，只接受固定 stage/type、整数 errno 与 64 位 SHA；不保存 exception 正文。helper 子阶段分为 validate、prepare-claim、open-public、initialize、send、history、decision、ack、eof。未知或含额外字段的 error 拒绝，不能利用错误通道保存原始正文。

ActivationService 的既有拒绝条件、返回值与 RPC 白名单全部不变；仅追加最多 64 条 admission_diagnostics（frontend PID、固定 reason、accepted、exception type），涵盖 helper grant/身份/pins/owner context/socket/lease 与 runtime-policy 阶段。不得用后继 case 的 reason 反推原 case；本次未放行任何新方法。

run finally 在 endpoint close 前执行只读 prepared.verify 与 source_pins 对比，写 `postflight.fixture_valid/source_pins_valid`。任何失败保持 UNKNOWN，也保留实际 ACK/cleanup 事实。

TDD：先观察 child_error 缺失与 admission_diagnostics 缺失。真实 dummy UDS/WS + 独立 Python owner/helper + 实际 Go prepare/claim，测试只在 service helper admission 输入注入 context mismatch；结果固定 reason=helper_context，helper-open-public/IncompleteReadError 原 SHA 保留，1 HTTP/0 ACK/无 helper record/全部 owned cleanup。这是可控诊断复现，不是原 case 根因判定。

验证命令与结果见 native-delivery-diagnostic-green.log。未运行 native/launchctl/模型账号，未读取或写入真实 auth、SBPL 内容或旧 case。测试自有短临时目录已清理，root 授权 Go cache 保留。

最后源码修改后的新鲜验证：**31 passed in 14.88s**。

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m pytest -q -p no:cacheprovider \
  tasks/g0-completion/tests/test_native_delivery_case.py \
  tasks/g0-completion/tests/test_owner_helper_service.py \
  tasks/g0-proxy-continuation/cold-start/tests/test_activation_service.py --tb=short
```
