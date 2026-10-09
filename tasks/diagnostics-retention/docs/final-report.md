# F02 显式 diagnostics retention

已实现受限 plan → 保存 JSON → apply → exact replay 垂直链。生产源码固定 `61daae64482a6a8aca4e0d4a1374bba50c9e57bf`；本任务定向 race 12top/64named PASS，0FAIL/0SKIP。ROOT 负责固定源码独立审查和 wholebuild/vet；此报告不替代其集成验收。

## 行为与边界

- 仅 run-control 的 `diagnostics-retention-plan` / `diagnostics-retention-apply`，无 task-handle、后台 timer/GC 或 schema 变化（仍 schema34）。
- 选择最新 completed attempt 的 detailed diagnostics；所有 required segment 均需可信 Exited。严格比较全部 Exited 的最大 Store `runtime_events.created_at` 与 `terminal_received_before`，并非 completed TTL。
- vector 包含该 attempt 全部 runtime_events，以及原 immutable deletion facts；最多512行、每body64KiB，SQL length/substr 在解码前限界。独立协议的 host_progress/report_events 不属于本保留期。DB event_revision=sequence；真实 producer body revision0可接受。重连接收 epoch>0 被完整body绑定，不要求等于原launch epoch。
- run授权先于枚举，每页最多64task，items0..8，canonical JSON加CLI newline≤8192。容量不能容纳的 candidate 连同 exclusions/scanned/cursor 全部回退，不越过它；满64可再查询空页。
- hash 是一致性校验。apply 在第一项删除前校验完整 plan（scope/order/unique IDs/hash/尺寸），然后每项再次认证并复用原 DeleteDiagnostics 的 durable intent、physical lease、VerifyRemoved 和 refund。无 batch transaction/全局回滚。
- optional guard 在入口深拷贝，完整对象保存在原 request_json并计费；fresh snapshot和prepare事务双重重算。prepared/removed仅exact原request恢复，不重新施加选择cutoff。nil guard的旧manual JSON字节与facts格式不变。
- 固定reason wire DTO兼容原map JSON与canonical digest；Coordinator外层也严格扫描wire。缺失/null Excluded、null/0 count、unknown/case/duplicate拒绝，合法{}保留。全局bridge parser未放宽。
- 五秒连接生命周期/断连可在部分成功后不给aggregate；保留原plan并原样重试，已完成项重放原receipt且不再次退款。

## 源码位置

`tools/orchestrator/internal/store/diagnostics_retention{,_types}.go` 实现planner/vector/whole-plan apply/wire；`diagnostics_delete.go` 只增加optional guard与两处fresh重核。`internal/coordinator/diagnostics_read.go`、`internal/ipc/types.go`、`cmd/orchestrator/diagnostics_read.go`、`main.go`、`coordinator/server.go` 接线和capability；`tools/orchestrator/README.md`说明公开命令。测试为Store `diagnostics_retention_test.go`与CLI `diagnostics_read_test.go`；原physical deletion/FK COMMIT/crash测试复用。

## 有效证据

[全部15批原始命令、计数、退出码和SHA](../data/evidence-index.json)；原stdout/stderr/exit均保留，不覆盖FAIL，计数不含package pass。

- `retention-final-race`：clean61daae6，Go1.26.8 `-race -json -count=1`，只选相关12top。Store10top62named、原physical deletion crash1top1named、公共CLI1top1named，合计12top64named PASS0F/S，exit0、stderr空。stdout SHA256 `62a72acd4398396cda20094ae911098a8b24c11fb333a65af19c391ce48a2ac0`。
- 业务RED包括原optional guard拒绝、合法重连epoch被排除、canonical8192发布为8193、长excluded cursor超限，以及真实公共plan能输出却无法读回/apply。对应修复后的green与最终race覆盖原断言。部分中间FAIL是明确记录的测试夹具错误/容量未到边界，未伪称生产RED；见[milestones](milestones.md)。
- 最终公共CLI通过真实socket的plan输出810bytes，原样0600保存→apply→exact replay；6种坏Excluded经实际Coordinator拒绝且delete intent仍0。原Host身份fixture PID54255的原Wait0在读取/删除前完成；全部CLIchild真实Wait0。事件由受控Store提交，不是真实provider worker。
- final矩阵含512个真实CommitHostEvent认证late事件/513拒绝、65536/65537体积边界、多segment所需max/缺证据、late/strict cutoff/坏JSON/row-body binding、snapshot→prepare drift、guard/plan深冻结、whole-plan认证/结构、部分失败/cancel/exact replay与旧manual兼容。第二segment生命周期为合成durable fixture，不声称真实进程；时间边界矩阵显式控制Store timestamps，公共CLI使用自然Store接收时间。
- 原删除actual deferred-FK COMMIT prepare/finalize失败后pool/external DB均回滚，退款待可信完成；原crash helper经Run原Wait确认exit91，复用original saved physical plan并VerifyRemoved。

未运行全Store/全仓，未验证Linux本轮链、真实provider/model/account、物理断电或资源资格。未替换用户安装/Hook注册、push/release。任务`tmp/`为空；Go fixture根由既有测试清理，原CLI/crash子进程均有原Wait证据；未删除归属不明或其他WT文件，无本任务保留actor/binary。原FAIL和所有交付raw保留。
