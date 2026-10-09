# F02 Store COMMIT 处置静态审计

固定源码 `73016e87cb59eb6b606819c25c6d194126675399`。本次仅只读审计与记录，没有生产修改或新增测试。独立审查待完成；不能将这份清单当作故障验收。

当前生产Store中直接`sql.Tx.Commit`只有五处：

| 位置 | 实际用途与失败处置 |
|---|---|
| store.go:migrate | Open阶段基础schema初始化DDL，失败由Open立即关闭整个sql.DB并返回，不会将该DB交给运行中的调用方。 |
| owner_transaction.go:commit | 所有显式BeginTx的运行期写事务共同出口；持有物理sql.Conn，COMMIT失败后独立background bounded ROLLBACK，再无条件ErrBadConn丢弃，不能将失败COMMIT后仍active的SQLite连接还池。 |
| config_abandon.go:ConfigAbandonFence | 只读取/认证已存fence；不创建absence proof、不写DB、不unlink。真实CommitConfigAbandon写路径用ownerTransaction。 |
| snapshot.go:TaskSnapshot | 只读任务/事件/预算快照，失败返回error；无DB写入或额度释放。 |
| summary.go:SummarizeRun | 只读run/task/预算/重试资格；executionCapacity和executionGrant仅查询，不提交reservation。后续continuationSummary另开只读快照。 |

运行期schema迁移migrations.go已将其原始conn/tx装入ownerTransaction后调用owned.commit；失败记录connectionDiscarded，不能重用不确定连接。schema23需要临时关闭FK时还会恢复并核PRAGMA，无法核实即丢弃。initial owner与diagnostic deletion的rooted helper返回同一ownerTransaction，保留根身份核对；COMMIT成功后根身份异常映射uncertain，不声称回滚已提交事务。

其余直接BeginTx路径为scope/owner/Host recovery/new-base/validation source/bridge/continuation读取快照，返回前Rollback；本审计未发现由这些路径提交的新运行期写事务。具体读操作并不赋予新的dispatch/deletion authority，相应写事务会重核事实。

已有真实延迟FK COMMIT失败、外部连接/池连接回滚、FK重新启用与额度守恒证据，按适用范围复用`tasks/validation-tool-quota/docs/milestone.md`及该任务原raw，以及owner/config迁移原证据；此次没有重跑或声称每个读API都进行了COMMIT故障注入。

剩余边界：本清单只覆盖显式事务，不涵盖`d.sql.ExecContext`等自动提交写及其故障处置；例如admission.go的Host离线标记、limits.go配置、owner_grants.go授权记录和store.go若干旧写入口仍存在自动提交，不能凭此清单声称全部运行期写操作COMMIT失败均已关闭。三个仍直接Commit的只读入口和其他直接Rollback读快照没有统一的物理连接不确定处置；当前不涉及未提交业务写入泄漏，但取消/IO错误下的连接可用性未在本审计动态验证。未提供直接SQLite篡改、任意driver故障或物理断电保证。后续需要实际失败证据时再最小修复，不以静态搜索宣称所有连接错误路径已验收。
