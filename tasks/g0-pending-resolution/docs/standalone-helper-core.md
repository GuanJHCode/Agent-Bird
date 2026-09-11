# Standalone helper 核心冻结（尚非可运行原生探针）

范围：G0 单次固定 root/nonce 的参数核验、受限历史投影、Go 合成结果 envelope、私有目录内独占发送意图。仅新文件，不修改原生 Codex、Go 工具或 delivery_audit。

`native_standalone.py` 当前提供核心函数，CLI/RPC 连接与 prepare/send-once/observe 尚待后继提交，不可用于原生资格结论。拟用入口为 `python3 native_standalone.py prepare|send-once|observe --case ABS0700DIR --root UUID --nonce sd-qual-01`。

- binding 显式登记服务与 TUI 的 PID/UID/出生时间/实际二进制路径及固定 SHA，服务 socket dev/inode，当前 boot、明确 root/prior turn、Go 二进制、job 目录与 controller-evidence.json 字节 SHA。
- 固定 RPC 仅 initialize、server/diagnostics、remoteControl/status/read、thread/read，以及一次 `turn/start` 的 `input:[]` 和 `g0_delivery/orchestration` envelope；额外模型/权限/用户输入参数均拒绝。
- `issue_once` 先 O_EXCL+fsync 写 `send-attempt.json` 的 `status:send_intent`，再重新核服务/TUI身份并发送。任何失败均不能用同 case 重发。回执明确 `history_delivery_proven:false`。
- 历史阳性仅精确 functionCallOutput 类型、name、namespace、完整 envelope；原生 READY final、completed、idle 与工具终态共同组成发送屏障。投影不保存模型/命令正文。原生 completedAt 仅 nullable Unix 秒，不能用同秒推断任务先后。
- 本机身份核查不替代 controller 的独立 PTY/当前显示证据；这部分核心不宣称通用 attached/source-service 真实性，也不实现业务 ACK。

TDD：初始 core-red 为 8 项中的 7 个 not_implemented 接口缺失；binding-red 记录新增 TUI 字段拒绝与 bool/int 混淆。runtime-red 记录 11 项中的 3 个明确 assertion 失败（时间戳投影、发送前持久意图、身份丢失停止发送）。最后 runtime-green：11 项通过，0.005s。均为离线测试，不连接原生服务或模型；发送边界只替换外部身份/时钟与 wire，真实私有目录、O_EXCL、锁、fsync 路径保留。

复现：`PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tasks/g0-pending-resolution/scripts -p native_standalone_test.py`。
