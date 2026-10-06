# F02 explicit budget revision

ROOT冻结2026-10-07。目标：真实公开owner控制命令以显式budget-group revision提高未来预算，保持历史已用/预留守恒；不是只增数字而无法retry/resume。独立WT budget-revision，base owner6b0ad5c（schema17已稳定）；integration3d61c08/F05/F01增量稍后由ROOT组合，禁止回退它们。schema18由本任务唯一占用；F10本轮只改owner运行恢复，不修改DB。root原研发dirty仓不得修改。全goal仍F01-F13。

## 责任和权限

指定budget_revision_worker为本WT唯一writer，ROOT不互写；你不是唯一协作者，其他WT有ROOT修改，不撤销他人变更。不得派生。负责budget DTO/IPC/CLI/Coordinator/Store schema18、effective caps、生命周期与summary投影、必要回归及本任务记录。不要改owner runtime/签名/process/候选输入/retry profile权限面或详细日志。纯Store/contract测试和build/vet可执行，mac原生进程/CLI/sandbox与Docker token始终ROOT，必须先交可运行用例再由ROOT调度。无账号、用户安装、push/release。可本地commit。

## 固定契约

- 追加immutable budget revision ledger，不覆盖task_execution_policies基线或retry codes/backoff/hash。有效预算为绝对effective cap vector：revision 0使用baseline，revision N直接使用latest完整snapshot，绝不与baseline或旧revision算术相加；未增加字段须重复当前值。原policy hash/既有grant/launch intent不重写。
- public `budget-revise --task-id --control-file --request <json>`沿项目CLI风格；controller/control-token只由既有control capability加入，不能由模型artifact自授权。IPC owner peer鉴权/ValidateDispatchPeer/ValidateTaskOwner完整复用。request中明确budget_group_id、expected_revision、command_id、group_max_attempts、group_max_active_ms、tasks[]完整成员snapshot（task_id/work_revision/max_attempts/attempt_max_active_ms/task_max_active_ms/segment_max_active_ms）。初始budget revision=0，成功CAS递增1；严格JSON、大小/数量边界和canonical排序。
- 校验anchor在请求group，同group全成员来自同run、均已有execution policy，任务集合exact匹配且work_revision当前；拒绝legacy/mixed group、漏/多/重复任务、跨run/组/owner、无效caps、减少/零值、没有任何增加、stale CAS、overflow。保持既有上限：group attempts<=256/task attempts<=16/group active<=86400000ms，task attempts<=group，attempt<=task<=group，segment<=min(attempt,现有defaultSegmentActiveMS)。不通过修订提高平台硬上限。
- command_id已存在且canonical请求完全相同（包括原expected_revision）返回原receipt；异payload/异group冲突。command replay先于当前revision检查，不因后来修订使旧幂等失效。一个transaction内quota admission、CAS、append immutable snapshot/receipt、storage_usage/必要trigger和版本发布。schema17->18迁移真实既有rows，失败完整回滚；不占用17或做noop17。
- 只影响未来capacity/grant。不能重置attempt_no/used_ms/granted_ms/UNKNOWN、不能放大已存在segment deadline、不能改尚未消费的decision reserved_active_ms或旧grant。已有pending retry policy hash仍原基线，不因budget amendment失效/获得更大承诺；新显式决定可用剩余额度。
- 增额本身不创建attempt、不重试/恢复UNKNOWN、不复活completed/cancelled/blocked_dependency。failed root耗尽后可显式retry同失败event/action-slot，interrupted可同attempt显式resume；既有ready/resume_queued可重新dispatch，守恒计费。必要下游保持blocked，另由显式DAG rework决定。
- Host生命周期：policy failed任务仍有`runtime_events` pending或`report_events` pending/pending_session时，`RetirableHosts`及`RetireHost`的TX重检必须保留Host；不可因当前effective caps耗尽推断owner决策已完成。owner显式retry后任务变ready；本次policy failed的新retry仅在其failure action slot尚未ACK时可首次提交，`runtime_decisions`对该slot的既有ACK `handled`、`waiting_user`、`stale`或`rejected`都关闭首次retry资格；已有`retry_decisions`的精确幂等replay必须先返回，不受后续ACK影响。增额后要retry，须在ACK处置前完成revision+retry；等待用户选择时保持event pending，不先以`waiting_user` ACK消耗slot。所有failure action slot经上述既有ACK处置后，才沿既有terminal路径退休，绝不自动completed。CLI/docs/summary须明确此顺序。legacy新retry行为不变，不笼统扩展为全任务ACK语义改变；旧released/dead owner不由预算复活，仍走F10权限路径。
- 注意旁路：lifecycle.go releasableTaskPredicate直接读baseline JSON/t.max_attempts，summary.go用t.max_attempts决定retry投影；均须用effective caps。selectReady/QueueRetry/rework现经executionCapacity/executionGrant/readExecutionBudget，可复用，但不得只改reader便假定全部consumer已覆盖。
- 状态/status/summary须可查当前budget revision与effective caps，使owner知道CAS基线；最小附加字段/专用只读入口按项目惯例选择，先在任务记录冻结，不加入无关交互。

## 必须证明

1. failed root真实Start/Failed/Exited后Host仍以原tuple ready、原retry预算拒绝 -> owner显式增额 -> 原pending精确failure action-slot retry -> 实际新worker/newattempt、新剩余grant；旧已用保持。interrupted同attempt resume剩余正确；UNKNOWN和blocked依赖仍拒绝重派。
2. shared group并发CAS仅一份成功；重复command精确幂等；reopen revision/receipt/累计不变；跨任务/segment/attempt/workrev不重置；旧pending promise不放大，已有active deadline不延长。分别证明四种ACK（handled/waiting_user/stale/rejected）拒绝本次policy failed的首次新retry、先retry后ACK仍可精确replay、revision本身不消耗failure slot。
3. owner peer/token拒绝、public request实际可达、status可见；legacy默认行为及新caps生命周期/summary投影无回归。
4. quota/存储事务回滚，非法或stale命令无部分写入，17->18迁移/新库/新于binary拒绝。

先TDD真实Store RED，再最小实现。每次测试必须 `-count=1 -json`落本WT tasks/budget-revision/data，functions.exec必须text返回exec_command metadata；保存原session并poll至终态，不能把30s观察返回/旧报告/自述当PASS。记录top/named/fail/skip和未验native范围。控制命令和JSON不得泄露真实能力材料。原生CLI测试先准备但无token不执行。

交付：本地commit（含测试/记录），原session和JSON路径/计数、源码位置、未解问题。ROOT独审和原生最终验收后再集成；scope冲突或连续两次无新进展及时交回，不重做已调查模块。
