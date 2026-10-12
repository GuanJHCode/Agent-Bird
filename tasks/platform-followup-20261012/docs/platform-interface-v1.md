# Platform interface v1

`orchestrator platform capabilities` 是无参数、无状态的只读查询。它不打开数据库、拉起 Coordinator、读取账号配置或探测 Provider。schema_version 是此二进制写入的版本，不是磁盘上数据库的观测值；runtime_verified 恒为 false。

返回外层 version=1、schema_version、code_sha、code_modified、contract、contract_sha256。Go build provenance 不可用时 code_sha=unknown/code_modified=true；有修改的构建不能将 revision 当作精确产品代码证明。

contract 固定顺序字段：version/os/arch/capabilities/events；capabilities 按源码固定顺序，每项 id/version/gate/reason/requires。gate=public 表示有公共接线但仍须逐任务准入；internal 表示私有底层，不授予公共动作；blocked 必须拒绝。客户端必须拒绝未知版本和未知 gate，不得由 missing/unknown 推断支持。

哈希：Go encoding/json 默认 escaping、固定 struct 字段和数组顺序、无末尾换行；SHA-256 输入为 UTF-8 `agent-bird/platform-contract/v1\n`（末尾一个真实 LF）拼接 contract 的精确 JSON bytes，输出小写 hex。外层 build metadata 不进入 contract hash。该 hash 是完整性标识，不是签名、进程证明或执行权限。

既有运行事件仍为 contract.Event v1，producer/sequence/hash/epoch/durable ACK、业务内容摘要及 signed Owner 协议均保持原有规则。本次不替换既有 hash 域。events 列出相关生命周期 vocabulary，`exited` 仍要求原 Handle Wait 与所属执行树终态；PID absence、人工 resolved、lease expiry 不能代替。`failed`/`stopped` 不自动证明树已退出。

## 恢复边界

- 新任务显式 `submit --owner-launch-protocol 2`：Coordinator 保留原 Owner Cmd/唯一 Wait，原始空FD交接和签名ACK保留；只允许同 scope 且最多一次 pre-Bind replacement，不能升级旧记录。
- supervised-v1 的 transport Host 丢失，但 Original Owner 仍活且保存原 Handle：按签名 scope、PID/birth、generation/epoch 重连，原 Owner 结算真实终态。
- 原 Owner/最后 Supervisor 都丢失：历史原始 Wait 无法恢复，继续 UNKNOWN；不得因为 capability query 输出支持监督就采用存活执行树或释放槽。
- 已有受限 never-Applied/initial-bind 双死亡恢复是独立协议，原有 schema29/30/32 围栏和预算不变。它不产生历史 worker Wait。

## 验证

本批新增CLI用例最初因 invalid_args 失败；实现后定向Go JSON：4个顶层、8个命名用例PASS，0 FAIL/0 SKIP（data/platform-final.jsonl）。测试覆盖严格参数、无诊断输出、外层与canonical hash、未知平台默认关闭、不可误开的公共动作和稳定版本摘要。不构成Provider/native执行验收。

独立只读审查覆盖 2e0f79b；发现遗漏 launch_rejected vocabulary，已通过新增失败回归补齐。恢复条目追加 current_origin_control_revision_budget_admission 条件；本次不修改运行时准入或所有权流程。
