# Controller 的受限 RPC 错误投影

`helper_rpc_error_fields(raw)` 独立校验 helper stdout，仅在版本 1、rejected/uncertain、reason=`native_rpc_error` 及来源/重发标志为 false 时接受可选 `rpc_error`。可选 absence_proof 必须为 false；不接受其他顶层字段、重复 JSON 键或非标准常量。

内层精确为五字段：固定五个方法名之一、request_id 为 1..10000 的非布尔整数、code 为有符号 64 位整数、message_bytes 为 0..65536 的非布尔整数、message_sha256 为 64 位小写十六进制。仅保留这个投影，不保留 error.message、data 或 params。它是对 helper 报告的类型校验，不把摘要格式正确升级成独立原生来源证明。

`helper_calls` 继续保留原 stdout/stderr 字节数、SHA 与退出码；非零退出但没有合格投影时只补 `rpc_error_unavailable=true`。诊断不改变准入、原 deadline、写入次数或重发规则，没有新增 RPC。旧 sd02 数据不修改。

两项新增用例先 RED，再覆盖类型/范围、canary、扩展字段、重复键和错误 envelope 的拒绝。最新 `python3 -B -m unittest native_sd_collect_test native_sd_pty_test native_sd_control_test -v`：`Ran 27 tests in 0.775s / OK`。未启动原生服务或模型样本。
