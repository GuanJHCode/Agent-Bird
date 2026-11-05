# T04 × T07 组合差异复核：PASS

本结论认可 T04 r5 与当前 T07 owner guard 的明确合并局部，未发现必须修复项。所有本轮独立测试会话已结束；root 集成 race 报告也已有退出0与完整终态。该结论不扩大至 T05/T06A 其他实现或全项目交付。

## 冻结与实际差异

- integration HEAD：`28a3698d7df9310aad2eff8b3ab73eceac0ec7ac`，T04 未提交合并树。
- 集成 source-manifest SHA256：`da5c44d060347ebd48ede1e50c15ad158f2c92f7fc6df743142a00bcab198e81`；其26个文件在 hash-before.json/hash-after.json 前后均零差异。
- T04交付的25个产品文件中，23个与此前PASS的r5逐字节相同。2个不同文件的差异均符合组合合同；没有清单外改动的 tools/orchestrator 源码。工作流任务记录/ADR等文档不作为产品新增功能或T05/T06A改动重新签收。
- `internal/store/owner_action_guard.go` 仍精确为 SHA256 `d3704ec2d0d1ace2b2575af7ab53238b9868240ecd593afb27733d12f9649150`。

## 局部正确性

- `internal/coordinator/server.go:106` 追加可选 RefsV3 请求字段。
- `server.go:480` 的 Ready 同时保留 managed_owner_projection_v1、budget_group_policy_v2、owner_request_changes_v2 及其它既有能力，并增加独立 owner_rework_refs_v3。
- `server.go:648` 在识别 refs phase 或进入任何业务校验之前调用既有 authorizeOwnerAction；`server.go:653` 与 `:662` 分别将同一个 actionCtx 传入 v3 ReworkRefs 和 legacy QueueRequestChanges。故 begin/chunk/commit/abort/gc 不能绕过当前 owner、内核进程身份及已有本地 ancestry 规则；Store 仍在锁/事务内核 snapshot，覆盖 rebind 与 replay。
- 相对当前HEAD的 server.go diff 只有字段、Ready与守卫后的 v3 分支3个hunk。原 owner 六动作 guard 与 local-submit collect-only gate 没有因T04合并丢失。
- `internal/store/owner_rework.go:293` 相对 r5 的唯一文本差异为 `if err :=` 代替 `if err =`。仍在同一位置、同一事务中，于读取已有决定/返回重放结果之前调用同一个 validateOwnerActionBinding；变量作用域不同不改变失败返回或后续查询赋值行为。

## 新回归与实际验证

已读 `internal/coordinator/request_changes_owner_integration_test.go`。第一项用真实本机IPC逐个phase测试wrong-control与retired-owner，故意保留不完整业务输入，断言先返回owner_mismatch/origin_owner_lost且task保持ready；第二项确认4个相关能力共存。这两项验证拒绝顺序和合并特性共存；不冒充成功上传/commit的端到端用例，成功业务路径由已PASS的r5及组合race其它已有回归覆盖。

| 证据 | pass events | 顶层 PASS | package PASS | fail / skip | 退出码 |
|---|---:|---:|---:|---:|---:|
| 本轮独立 normal.json（仅2个新增coordinator测试） | 12 | 2 | 1 | 0 / 0 | 0 |
| root T04-integrated-race.log（5个受影响package） | 114 | 37 | 5 | 0 / 0 | 0 |

独立运行使用Go1.26.10、TMPDIR=/private/tmp、GOENV=off、GOTOOLCHAIN=local、GOPROXY=off、固定Codex PATH及review-final/integrated独立GOCACHE；argv见environment.json。没有并发source写入。

root race 已独立读取并解析完整JSON日志，核对报告SHA `687644dc994ff89f5b9e98b50d8c81fe6db67ce64fda6de8daced9d5b9032083` 与元数据一致；全部run有测试终态、5package PASS、无DATA RACE，非仅采信摘要。范围含 ProtectedOwner、OwnerActionBinding、五phase新回归、Refs Store/Host、v1/v2 golden、CLI v3分块与lost-commit、legacy CLI replay；不是全树race。`git diff --check` 退出0。

## 边界与交接

此前 T04 r5 的本地正文回收、异常隔离保留计费、tombstone/空屏障保留边界继续有效；本次没有修改或扩大GC恢复协议。未重跑此前已PASS的全部base suites，复用其未变证据。没有真实模型/provider、安装包v3端到端、native live-parent/typed-resume或非Darwin运行验收。

本轮只写外部review-final/integrated证据，未改source、未派生、未调用模型、未commit/push。独立测试及root相关race会话均已结束，最终source hash零差异；报告/环境/hash/原始JSON与独立缓存保留为必要复核证据。

## 主会话签收

root已逐文件核对26项integration manifest及T07 helper，确认23项与r5精确一致、两项明确组合差异与一项新增coordinator回归均已独审。root5包race114pass/0fail/skip完整结束，独审额外12events通过，相关会话无遗留。T04×T07组合范围签收PASS，允许提交并push任务分支；不扩为最终安装/live/TUI或全项目验收。
