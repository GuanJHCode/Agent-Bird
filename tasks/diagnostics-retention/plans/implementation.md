# F02 显式诊断保留期清理

基线：integration `73016e87cb59eb6b606819c25c6d194126675399`。ROOT负责契约、集成与验收；worker在独立diagnostics-retention worktree唯一写实现、相关测试和任务证据。只读独审针对固定快照。用户已授权继续实施；本轮不操作其真实诊断数据。

## 目标与时间语义

新增run control公共CLI/IPC的bounded dry plan与saved exact plan apply。`terminal_received_before`严格比较Store已持久化的所有required segment的Exited接收时间最大值；这不是实际退出、Owner Accept或任务完成时间，不是后台timer/自动GC。现有terminal/delivery/grant/auth准入全部保留。

## 契约

- plan request：version1、run_id、request_id、terminal_received_before、limit1..8、可选after_task_id。cutoff必须canonical UTC RFC3339Nano且不在未来。先认证run_control后枚举。每页最多扫描64个按task.id稳定排序的task/current latest attempt，返回已扫描cursor以便继续；未选中项明确计数/原因码，不能把错误当全部可删。
- plan result：version/run/request/cutoff、固定ordered items、next cursor、plan SHA256（不含自身digest字段的canonical JSON SHA256）；单item沿用DiagnosticsDeleteRequest并附optional retention guard。0..8items（无eligible时空计划为正常只读结果），完整canonical plan<=8192bytes；若字节限额先到，cursor不得跨过未放入item。plan无DB写入，不持有FS锁。
- retention guard：reservation、namespace、terminal_received_at、cutoff、terminal vector SHA256。vector包括完整diagnosticDeletionFacts以及该attempt全部严格绑定event的实际row身份/sequence/revision/payload_hash/body hash/created_at，稳定排序，至少每个segment一条；重复晚到退出按最大值保守延后，不要求恰好一条。
- 不以json_extract过滤掩盖坏event：单attempt最多512event rows，单body最大64KiB，SQL先length/substr bounded读取，超限/坏JSON/缺失/坏时间/不一致均排除；不截断vector装作完整。严格核真实producer协议，DB event_revision是sequence，raw host body EventRevision可能0，不机械等同。Scope通过task/run+attempt/segment/producer/command/work revision及DB冗余字段交叉验证。
- optional guard `omitempty` 保持旧manual delete请求canonical bytes不变。fresh diagnosticDeletionSnapshot及prepareDiagnosticDeletion同一事务内重新计算并精确比较selection；drift在任何Remove之前失败。guard完整存入既有request_json并纳入control quota，无新schema，不改facts_json历史格式。
- apply只执行保存的exact items，不重新枚举/补位；在任何删除前校验整个plan结构、hash、run归属、唯一item/requestID、时间和规模上限。各item继续现有DeleteDiagnostics的prepare→Remove→VerifyRemoved→finalize，只有物理删除证明后释放rawreservation。已durable prepared/released replay按相同requestJSON恢复，不按新cutoff重选；partial batch不全局原子、重复apply可恢复。
- requestID从batchID/run/task/attempt/cutoff等固定字段稳定派生；planhash是自一致性检查，不是新的授权capability。正常响应逐条返回稳定成功/失败码，不暴露路径/DB细节；cancel/deadline停止剩余items。5秒超时/断连可能没有aggregate response，不能承诺durable batch report；调用方用同一exact plan重试，沿用每item durable prepared/removed receipt，不伪造回滚。
- 仅run-control命令`diagnostics-retention-plan`、`diagnostics-retention-apply`；本轮不新增task handle的runwide入口。复用现有canonical0600/owner/nlink1/8KiB request file和5s diagnostics连接取消生命周期。Store还要检验run归属，不能仅依赖CLI。

## 审查决策

只读契约审查的run先认证、64 task扫描边界、全event有界读取/真实revision语义、UTC严格时间、双事务guard/完整request持久、5秒中断重放及hash requestID要求均采纳。两项收窄：活动/UNKNOWN/pending/bad记录逐项excluded并统计稳定reason，不让一个活跃task阻塞整run清理；仍绝不将其加入删除计划，cursor明确已审查范围。空计划允许0items；strict decoder+canonical body self hash即可，文件可含命令输出末尾换行，不强制用户手写JSON空白必须完全相同。requestID hash使用无歧义canonical tuple而非任务ID裸NUL拼接，taskID合法规则复用现有契约。

## 实施与少量必要验收

1. 一个实现者完整负责Store types/planner/guard/apply、IPC/Coordinator/CLI接线、README/命令能力宣告及最小测试。先复用manual delete现有fixtures，补真实Producer形状的有效病例，不能用手写错误event掩盖兼容问题。
2. 定向Store测试覆盖真实多segment/晚到退出的max、old replay不改接收时间、cutoff严格边界、坏/缺证据、plan后新增event/revision/state drift（含snapshot→prepare时），auth/跨run、quota/old manual replay、partial delete/replay。批量pagination/size/cancel按风险合并到少数table tests，保留原FAIL。
3. 公共CLI/IPC至少一条实际plan→saved private JSON→apply→replay链，确认expired读取、原quota释放一次/marker保留；不使用真实Provider/model/account，不删用户日志。
4. 固定生产快照独审与ROOT隔离wholebuild/受影响vet并行；最终定向race一次，复用未受影响旧delete原生crash证据，不重跑全仓。报告实际top/named/FAIL/SKIP和rawSHA。
5. ROOT核原raw、diff和源树，合入integration并更新progress/continuation。该实现改变binary，旧F13包仅覆盖旧source，最终候选按后续稳定批次重新封包。

## 未覆盖

无后台完成TTL、ACP/其他artifact GC、整盘/断电、2GiB洪泛、Formal60资源资格、跨机器Python、Intel/Windows原生或真实模型资格。原16tracked修改、旧证据和旧包保留，不push/release/替换安装。
