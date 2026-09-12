# Controller 的受限 RPC 错误投影

`helper_rpc_error_fields(raw)` 独立校验 helper stdout，仅在版本 1、rejected/uncertain、reason=`native_rpc_error` 及来源/重发标志为 false 时接受可选 `rpc_error`。支持 main 的最小错误包和 c2 的固定完整 observe 包；后者还必须 scope=`one_bound_native_root`、status=uncertain、current_display_verified=false。可选 absence_proof 必须为 false；拒绝这两种已知形状之外的顶层字段、重复 JSON 键或非标准常量。

内层精确为五字段：固定五个方法名之一、request_id 为 1..10000 的非布尔整数、code 为有符号 64 位整数、message_bytes 为 0..65536 的非布尔整数、message_sha256 为 64 位小写十六进制。仅保留这个投影，不保留 error.message、data 或 params。它是对 helper 报告的类型校验，不把摘要格式正确升级成独立原生来源证明。

`helper_calls` 继续保留原 stdout/stderr 字节数、SHA 与退出码；非零退出但没有合格投影时只补 `rpc_error_unavailable=true`。诊断不改变准入、原 deadline、写入次数或重发规则，没有新增 RPC。旧 sd02 数据不修改。

两项新增用例先 RED，再覆盖类型/范围、canary、扩展字段、重复键和错误 envelope 的拒绝。最新 `python3 -B -m unittest native_sd_collect_test native_sd_pty_test native_sd_control_test -v`：`Ran 27 tests in 0.775s / OK`。未启动原生服务或模型样本。

后继交叉回归使用固定 c2 helper 的实际离线 main/Rpc/Case 返回，保留在 [四阶段 fixture](../scripts/fixtures/native-rpc-error-c2-outputs.json)。原 e6 消费者对前三阶段保留摘要，但丢失 observe 的 thread/read id 6；修后四阶段全部保留精确五字段。完整包中的 facts、rpc_checks、历史/业务结果等只是被丢弃的上下文，不在此进行二次审计，也不据其改变准入。未知顶层扩展仍拒绝，放入这些已知上下文的 canary 不外泄。

本轮最新本地全套 `Ran 35 tests in 0.759s / OK`；另外重新执行固定 c2 producer 的四阶段路径，exit 0、四个摘要均精确保持。来源与临时副本清理见[交叉记录](native-rpc-error-c2-cross-check.json)。这是离线协议回归，不是原生运行证据。
