# sd05 唯一运行 nonce 冻结

按主合同对第五个独立 case 的登记，helper 运行入口只允许代码常量 `ACTIVE_NONCE = "sd-qual-05"`。没有环境变量或 CLI choices 覆盖；`sd-qual-01`/`02`/`03`/`04` 不再作为生产入口。本提交只冻结 helper 侧唯一 active nonce，不启动原生服务、TUI 或模型，不计作原生阳性。

READY/ACK 标记由 nonce 推导为 `SD_QUAL_05_READY` / `SD_QUAL_05_ACK`。family/session、O_EXCL send-once、canonical binding digest 与既有 RPC 边界保持不变。未新增 RPC、配置项或业务事务。

历史回归只在隔离测试内显式 patch 对应 nonce：sd02 整链仍 `ACTIVE_NONCE=sd-qual-02`；sd03 摘要交叉链 `ACTIVE_NONCE=sd-qual-03`，fixture 与 `SD_QUAL_03_READY` 原字节不改，绿控制器继续用已导出的 `59a2d67`。原 standalone-helper-sd03-* / sd04-* 登记/日志/validation 保留为当时收据。

准入回归先 RED（旧常量拒绝新 05），最小实现后 GREEN：`native_standalone_test` + 历史 binding 共 33 项（0.257s），hash 1 项（0.023s）。见 sd05-nonce-red、sd05-green、sd05-hash-green 与 [validation](standalone-helper-sd05-validation.json)。
