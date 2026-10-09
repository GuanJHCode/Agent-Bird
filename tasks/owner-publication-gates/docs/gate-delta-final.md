# Publication gate 增量交接

生产固定 `fe3bbd55fdb6cfae3bcf668334d055a3286b4261`（基于 `de19375`），未改 schema、API signature、Host stage、Coordinator 或 cmd。保留 ROOT 既有 9 处 gate，全部在权威 scope 的同一写事务中、任何 durable/exact receipt 返回或写入前。ROOT 负责独审、整合及 build/vet。

## 修改

- `owner_succession_prepare.go`、`initial_owner_recovery.go`、`owner_recovery_handoff.go`、`owner_bound_abandonment.go`：覆盖 prepare/finalize/apply/resume 的 pending publication 防御门禁。
- `resume_continuation.go:43`、`rework.go:86`、`dag_rework.go:25`：在 owned.tx 得到后按输入 TaskID resolve RunID 并检查 pending；fresh 与 exact replay 都经过门禁，移除续跑与 v0 的后部重复 gate。
- `bridge_bindings.go:158`：认证 helper 保持纯认证；新 `bridgePublishedOwnerTx` 在认证后检查 publication。Bind、Activate、current-owner redelivery 与 ACP 写入口复用此 helper。
- `bridge_historical.go:41`：Receive 写入仍 gate，历史 Query/Status 保留当前 owner 认证且允许观察 pending；Unbind 仍认证后降低 authority。BridgeStatus 的 age-offered 仅降低/到期状态，不创建可发送权限。
- sender 审计：`owner.go` rebind 在同事务将 CLAIMED attempts 与 bindings 标记 STALE 并清 token；Claim/Hook ingress 校验非 STALE token，`bridgeSendBindingTx` 要求 ACTIVE/epoch/native sender，send permit 和 MarkOffered（含 exact replay）先执行该验证。本次没有改这些路径或放宽认证。

## 实际验证

所有命令、HEAD/dirty metadata、stdout JSONL、stderr、exit 均在 `data/gate-delta-*`；runner 为 `scripts/run-gate-delta.py`。工具链 Go1.26.8，TMPDIR=/private/tmp。`data/gate-delta-summary.json` 给出逐具名结果、SHA256 和当前源码摘要，不把 package pass 当测试计数。

| batch | exit | top / named | 结果 |
| --- | --- | --- | --- |
| gate-delta-red | 1 | 0 / 0 | 新测试 Unbind 返回值夹具编译错误，保留原 FAIL |
| gate-delta-red-02 | 1 | 2 / 13 | 7 FAIL / 6 PASS；真实复现续跑、v0 replay 放行及 Query/Status/Unbind 误阻 |
| gate-delta-green | 0 | 2 / 13 | 13 PASS，0 FAIL/SKIP |
| gate-delta-final-race | 1 | 2 / 15 | 13 PASS / 2 FAIL，未称整批通过；新增 ACPBind wire 夹具缺必填路径，子例及其顶层失败 |
| gate-delta-bridge-race-02 | 0 | 1 / 10 | 10 PASS，0 FAIL/SKIP；补合法路径后仅重测受影响 Bridge 顶层 |

最终适用 race 证据组合：final-race 中 QueuedDecisionReplay 的 1 top / 5 named PASS（续跑、v0、v1 fresh、v1 pending exact replay，源码及用例未被后续路径夹具变更影响） + bridge-race-02 的 1 top / 10 named PASS，共 2 top / 15 named、0 FAIL/SKIP。没有重跑全 Store。生产源码在两个 race 批之间不变。

## 范围与清理

回归 `owner_publication_gate_delta_test.go` 复用实际 admission/scheduling/event/decision fixtures；Provider/Git receipt 为 Store fixture，没有调用 Provider/model 或启动 actor。pending latch 是合成 SQL gate-predicate 测试，按真实 schema FK/不可变 transition 插入并标记 published，不送 Finalize、不伪造 process proof，不声称合法 rebind 同时保留 ACTIVE binding。真实 publication/rebind pending 链由 ROOT 原有独立证据覆盖，本增量不重复冒称 native publication 验收。

查询/Status/Unbind 的无认证反例仍拒绝；写入反例包括 Receive、Redeliver、Activate、Bind 和公开 BindACPBridge；拒绝前后业务摘要与 delivery attempt CLAIMED 状态不变。发布后 queued exact receipt 完全相同，Unbind 清空 token 并 DETACHED。

runner `subprocess.run` 已返回每批真实终态；测试仅对当前 test PID 做 native 身份核验，没有新进程 actor、signal 或 UNKNOWN 清理。DB/receipt 属于 Go test 自建 TempDir，正常测试清理；本任务新增文件均为交付测试、runner、报告和 raw，无需删除额外 binary/package/runtime。原全 Store partial/raw 与旧交接完整保留，仍不计完成、通过或 original Wait。

不宣称全 F01–F13、资源资格、真实 Provider、Linux/native 综合验收。独立审查及最后集成验收由 ROOT 接管。
