# F01/F02 第一里程碑交付

最终产品修复快照：`b2d5be98f28a3ff508aeaab577d5011d2af6ee89`；集成提交 `2587052`。R1 公开预检、R2 终态依赖/pin、R3 pending 时间预留及精确 quota 准入均已修复并独立审查 PASS。此范围是 prelaunch probe 有限重试与四层预算，不是全部 F01/F02 完成。

最新证据和原始失败说明见 [review-closure.md](review-closure.md)。下面保留初始 `7403249` 里程碑的范围和验证记录；其中“等待独审/集成”已被本段与新报告取代。

产品与测试冻结提交：`740324974aab278cdd9d2f0b6819e18b692dfcd7`，base `3ecc6dd3f129f25194d1255d3be0862f3d7be872`。此提交是生产 prelaunch probe 瞬态重试和四层预算的第一里程碑，**不是全部 F01/F02 闭环**。初始证据保留于下文，最终独审和补验以 review-closure.md 为准。

## 已完成行为

- 公开 task submit/dispatch JSON 可传 `execution_policy`；v1 policy 闭合 JSON、规范化默认值、不可变 canonical JSON/hash 同权威 SQLite 事务保存。没有 policy 保留原预算与 owner retry；没有 retry 不自动重试。
- task/group 次数、attempt/task/group 活动时间及 segment grant 按冻结 brief 接线；continuation 不创建新 attempt，UNKNOWN 完整预占，not_started 只退款活动时间，次数与编号不退回。pending retry/rework binding 预占 policy 次数容量，claim 自己的 binding 不双计。
- 仅 `ProbeOutput` 的固定 `--version`/`--help` 在 `cmd.Process==nil`、`ProcessState==nil`、非取消/超时/ExitError 且 errno EAGAIN/EINTR 时产生私有 typed facts。version wrapper 保 cause 和稳定公开错误。Host 的可信 pre-Start 拒绝事件保留 facts，Store 同事务零结算、排唯一 retry、保留 Host；默认退避 5s/20s，持久到期时间不因 reopen 清零。
- Coordinator 100ms 有界 wake 驱动既有 dispatcher，无需新请求；Serve 返回前取消并 join。较早 sibling 被 pending 容量挡住时继续扫描 policy FIFO，让到期的承诺 retry 执行；legacy 返回语义保留。
- policy+config_generation 已支持。ExecutionPolicy 纳入准备与 receiver 同一 PlanSHA commitment，`omitempty` 保留 legacy 无 policy 的摘要字节；变 policy 复用原 CPP 被拒绝。
- summary 暴露实际 policy 和 `next_retry_at_unix_ms`；retirement、idle、config pin release 均考虑 pending binding。

数值、使用示例和后续限制见 [usage.md](usage.md)。高层 dispatch 的 profile timeout 使用规范化 segment cap，policy attempt 可以超过 60min；显式 segment、0 默认 segment、短 attempt、长 attempt 和 legacy ≤60min 均已核验。task submit 保留用户提供的 adapter profile，Host 取 profile 与预算 grant 的较小 deadline。

## 实际阶段边界

自动 retry 支持直接只读 reviewer/普通非候选 Provider 任务在 Host invocation 阶段遇到的上述 probe 启动瞬态，继续受原 Provider、binary pin、profile 和权限 guard 约束。它不依赖 fixture 的 `invocation_unavailable`，不授权更换账号、模型、Provider 或权限。

`invocationForGrant` 先 decode/只读校验和 binary 校验，再 version/help probe，最后 `adapter.BuildInvocation`（`cmd/orchestrator/main.go:1065,1124,1141`）。source runtime 在 resolver 成功后才调用 `ExecuteLaunch`（`internal/host/source_runtime.go:421,457`），candidate/provider preparation 在 Host 执行阶段（`internal/host/host.go:208,433,451`）。本阶段仍明确拒绝 candidate workspace/action 与 GitOps 的自动 retry；候选 validate/review/implement/integrate 可以使用无 retry 的预算 policy。没有隐式复用未知 candidate 目录。

UNKNOWN、permission/question、已启动 probe、ExitError、timeout/cancel、EACCES、任意 launch_failed、主 worker Start 错误、未对账 integration 均不自动 retry。主 worker 的 Start 错误仍保留 UNKNOWN 原路径，未修改 process Start/Stop/Wait。

后续保留：安全已退出 readonly 执行、候选阶段安全 retry、显式 policy revision 增额、详细诊断日志。真实 Provider/模型、原生 Host/worker/TUI 生命周期、用户安装和最终集成未执行；应由 ROOT 按剩余阶段验收。

## 关键源码

以下路径相对 `tools/orchestrator/`：

| 责任 | 位置 |
| --- | --- |
| policy/retry/facts DTO 和闭合解码 | `internal/contract/execution_policy.go:8`；`internal/coordinator/execution_policy_json.go:14` |
| schema15 及权威持久化/quota | `internal/store/migrations.go:3,117`；`internal/store/execution_policy_schema.go:11`；`internal/store/admission.go:332` |
| 四层预算、pending 容量 | `internal/store/execution_policy.go:12,116,148` |
| 同事务有限 retry 与持久到期 | `internal/store/host_events.go:134`；`internal/store/policy_retry.go:14,100` |
| FIFO、claim、source proof | `internal/store/limits.go:103`；`internal/store/scheduling.go:13`；`internal/store/decision_attempt_proof.go:43` |
| continuation/owner retry/rework/review | `internal/store/decisions.go:14,263`；`internal/store/continuation.go`；`internal/store/rework_validation.go:173`；`internal/store/review_retry.go:109` |
| summary/Host 保留/idle/config release | `internal/store/summary.go:154`；`internal/store/lifecycle.go:32,99,111` |
| probe cause/facts 与 Host 发布 | `internal/adapter/probe.go:24,60`；`cmd/orchestrator/main.go:1127`；`internal/host/launch_failure.go:73`；`internal/host/host.go:1025` |
| timer shutdown/公开请求映射 | `internal/coordinator/server.go:338,376,704`；`cmd/orchestrator/task_dispatch.go:55,151`；`cmd/orchestrator/task_entry.go:222` |
| CPP/PlanSHA 一致 commitment | `internal/install/config_generation_submission.go`；`internal/install/config_preparation_authority.go:334`；`internal/coordinator/config_generation.go:238`；`internal/store/config_preparation_authority.go:113` |

## 验证与原证据

Go 1.26.8，`GOMAXPROCS=2`，独立 `GOCACHE=tasks/retry-policy/tmp/gocache`。所有 Go 命令 cwd 为 `tools/orchestrator/`。完整具名结果、package Action/elapsed、原文件 SHA256、退出码与具名选择复验命令见 [verification.json](../data/verification.json)。计数包含 Go 的顶层与 subtest Action；同一模式按 package+name 去重。

最终普通与 race 各 **154 具名 PASS / 55 顶层 PASS / 0 FAIL / 0 SKIP**，实际 package 均 PASS。不是全仓测试，也不将 ROOT 的旧 100 基线重复相加。

| 最终范围 | 普通证据：具名/顶层 | race 证据：具名/顶层 |
| --- | --- | --- |
| 新 policy Store | `final-store-fifo.json`：33/12 | 与受影响 legacy 合并，`final-store-legacy-race.json`：86/28 |
| 受影响 legacy ledger/guard | `final-legacy.json`：53/16 | 同上 |
| public JSON、分类、CPP、dispatch 边界 | `final-public-bounds.json`：17/5 | `final-public-bounds-race.json`：17/5 |
| 受影响历史迁移 fixture | `final-legacy-migration.json`：26/18 | `final-legacy-migration-race.json`：26/18 |
| Host spool/Store/diagnostic | `host-policy.json`：24/3 | `host-policy-race.json`：24/3 |
| Coordinator timer/socket | `coordinator-actor.json`：1/1 | `coordinator-actor-race.json`：1/1 |

最后 `go build ./...` 和 `go vet ./...` exit0，输出分别为 `final-build-bounds.log`、`final-vet-bounds.log`（正常无 stdout/stderr）。旧 fixture 修改后也重跑 vet（`final-vet-fixtures.log`，exit0）。提交前 `git diff --cached --check` 无错误。

保留有效 RED：`red-store.json`（忽略 segment policy）；`red-retry.json`（未入队、pending 容量被抢、不安全 facts）；`red-classification.json`（facts/cause 缺失）；`red-mixed-classification.json`（mixed ExitError/timeout 错误链）；`red-dispatch.json`（公开 policy 丢失）；`red-policy-fifo.json`（到期 retry 被 sibling 挡住）；`red-legacy-migration.json`（旧 fixture 残留新 schema）；`red-dispatch-attempt.json`（合法长 attempt 被 legacy 入口上限拒绝）。早期 `green-*` 文件包含开发中失败，也全部保留，不计入最终通过。

真实 `TestPolicyMigrationKeepsLegacyReceiptAndPromise` 在旧 receipt/pending 已存在时移除新增 v15 对象，写回 v14、close/open，再比较原 receipt 并消费原 promise 到 attempt2；不以空新库代替迁移。corrupt v14 DDL 失败验证版本与新增列整笔回滚。

历史降级 fixture 窄修清单（均在 `internal/store/`）：`quota_bench_test.go`、`rework_migration_test.go`、`provider_controls_test.go`、`core_recovery_scope_test.go`、`bridge_schema_test.go`、`original_owner_migration_test.go`、`continuation_migration_test.go`、`decision_attempt_review_fix_test.go`、`config_abandon_test.go`、`decision_attempt_transaction_test.go`。它们先调用测试专用 `downgradePolicySchema` 移除 v15 对象，再执行原降级；`config_abandon_test.go` 的两处当前版本断言改用 `currentSchemaVersion`，未删除旧数据、回滚或 receipt 断言。

## actor 证据限制与执行记录

Coordinator test **确有 exec 子进程**：`internal/coordinator/host_hello_auth_test.go:234` 构造当前 test binary 的 `TestHostSocketPeerProcess`，`:244` `cmd.Start()`；`:250` cleanup 调用 peer/relay Close、Process.Kill 和 `cmd.Wait()`。Wait 返回值被忽略，没有退出状态断言或自然退出记录。这个 relay peer 提供独立 PID/socket，**不是原生 Host/worker/Provider**。约 0.44s 的普通测试证明无新请求时 timer 发送新 grant、未提前 retire 和 Serve join，不能扩大为物理 Host/worker 自然退出或完整时序验收。

Host 新测试调用真实 `recordLaunchFailed`、owned spool 和实际 SQLite Store 消费，但允许事实来自可信恢复诊断 fixture；adapter 分类测试在实际 `ProbeOutput` 的窄 OS command 构造依赖注入 errno，运行到 `cmd.Run`，不启动真实 Provider。组合证据证明生产接线及守卫，未声称实际 OS 资源耗尽或真实模型运行。

`green-expanded.json` 曾被 `TestPolicy[^A].*` 误纳入 relay actor，已向 ROOT 报告并保留原输出；之后取得令牌仅执行预定精确 Host/Coordinator 名单，普通/race 均完成后立即归还。所有后续纯检查采用具名列表，未再扩 actor。

actor 日志产生在 policy exhausted-sibling FIFO 修复与 CLI timeout 修复之前；单 task actor 不进入修改的错误分支，CLI 更改不影响该 socket fixture。此差异明确交 ROOT 审查，未机械作废原日志，也未扩大为最终原生验收。多数早期开发命令的完整 shell 参数未另存；JSON 的实际具名选择与 package 结果保留，manifest 提供选择等价的复验命令，不伪称它们是逐字 shell history。

## 状态

源码和测试已提交，最终修复已独审并由 ROOT 集成；范围仍限第一里程碑。本任务没有派生 Agent、真实 Provider/model、用户安装或 push。任务数据均为交付或必要验证证据；隔离 GOCACHE 继续保留为可复验缓存并通过 `tmp/` 忽略，不纳入交付提交。
