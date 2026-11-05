# T04 不可变返工引用：已实施并签收

显式version3走有界正文引用；未指定仍走旧v2，旧v1/v2已返工任务不自动升级。rev1完整prompt一次封存，后续所有feedback/acceptance按历史顺序完整组装，worker仅获得plain input，无控制目录权限。v1/v2字节及digest golden保留。

正文encoded<=256KiB、manifest<=8KiB、最多3refs、chunk decoded<=16KiB；最终实际stdin<=512KiB并保留原16KiB adapter/64KiB IPC较低限制。全量hash/parse/组装使用同次有界bytes，不截断、不模型摘要。

schema11 registry/uploads/pins先预留object expected_size+4096、upload4096元数据；包括published未commit、abandoned/deleting均计费。pin/小决定/revision/ready/revoke同事务。重放仍核当前owner，已提交同command同body可在Host/provider后续不可用时重放。五个refs phase均继承T07当前owner/actionCtx，原legacy流程不变。

GC正常流程先证完整已知link集合，再Darwin原子no-overwrite捕获到0700私有held目录，核捕获条目身份后仅删已证对象，fsync及heldFD nlink0后释放object charge。未知条目/捕获后崩溃保留原内容和计费并返回awaiting_cleanup_decision。每upload最多一个barrier，两object共用；成功removed只指正文删除，cleanup_reason明确upload tombstone及empty barrier继续作为有界元数据保留，须显式owner保留/清理决定，不宣称runtime全cleanup或自动TTL。4096是逻辑计费，不是APFS block测量。非Darwin GC创建隔离目录前明确unsupported。

有效证据：独立r5 normal/race各51test/subtest（23顶层），额外合同回归133（44顶层）；组合树5pkg race114（37顶层），独立组合新增2测试/12events，全为0fail/skip、无race。r5 vet/build0。T07 helper SHA保持不变；root只将server的RefsV3分支接到原actionCtx、合并cap，owner_rework仅保留原T07 if err :=声明形式。两次已证GC缺陷、一次协调快照失效和平台中断审查均保留记录；后续复审与组合签收已PASS。

当前实现/独审/root验收完成，允许本功能commit并push；安装包、真实provider、native-parent/TUI及整体最终验收仍待后续。完整历史见T04-implementation-history.md；正式独审及组合签收见reviews/T04-review-r5.md、T04-integration-review.md；精确命令/计数/SHA见data/public-evidence/T04-*。
