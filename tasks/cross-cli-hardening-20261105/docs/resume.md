# 当前恢复入口

集成worktree /Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/integration，分支 feat/cross-cli-hardening-20261105。原仓库 /Users/guanjunhui/code/ai/codex-cli-orchestration-design 保持原分支与未跟踪文件。root唯一写集成树；子agent不递归，最多两个并发子agent；每功能另一agent独审PASS、root核验/签收后才commit并push任务分支。未授权merge正式分支/release/全局安装替换。

已独审/签收并push：T00=08aa0184，T02=428a5877，T10=bde4bacf，T01=c29ac2bb，T03=2347d34c；T07=3fe267b9（独审73pass/集成55pass）；T05=a14affc4（独审39pass/集成19pass）；T06A=28a3698d（独审45pass/集成28pass）；T04 r5独审及组合签收PASS，当前提交。相关reviews和new reports可查，未将skip/旧树报告混为当前全验收。

活动负责人/树（均位于上述integration的同级tmp目录）：
- /root/implement_t04，t04 HEAD2347d34c：v3正文refs/schema11，ADR004已root采用（256KiB正文，512KiB最终stdin硬限，chunk16KiB；旧v1/v2golden保留，显式CLI v3）。r3实现冻结后正常/race有效完成，但独审实证GC额外hardlink仍存时提前释放charge。r3 fullrace1307pass8skip、store normal130pass不能覆盖此漏洞；review_t04全部会话已结束；root因消息交叉曾让worker在独审未结束时追加单一red测试，这批独审66顶层normal/21顶层race只作诊断；GC反例仍有效。现明确移交worker唯一写权修r4：先证明已知stage/final link集合/nlink再unlink，未知links保留charged deleting。未修/复审前不可提交。证据tmp/t04-evidence。它只读依赖T07 owner_action_guard.go快照SHA d3704ec2d0d1ace2b2575af7ab53238b9868240ecd593afb27733d12f9649150，必须排除其feature patch，由T07先集成。
- /root/implement_t07已final，/root/review_t07已PASS并结束，冻结t07 HEAD2347d34c：owner投影采用显式owner_projection_version:1与Ready managed_owner_projection_v1，legacy默认response不含新字段；全部KindOwnerBind仅managed_process，非native会话证明。local新提交空delivery=>collect、显式非collect拒绝。实证旧dead owner可用旧control排resume_queued，root授权run-specific live/birth+local peer ancestry与同tx snapshot/CAS guard，仅六类resume/retry/rework/request_changes/accept/answer；query/stop/ACK保留原bearer收尾合同，recover-host专用新owner能力/对账路径不套旧owner-live通用guard。shared helper在store/owner_action_guard.go，T04唯一负责其QueueRequestChanges/refs tx插hook，T07负责legacy owner_rework.go中3行同tx guard hook；T04保持完全相同hook并负责其余v3内容。两agent Ready cap数组与server局部hunk由root合并，不能丢cap/互改授权段。证据tmp/t07-evidence。
- T05 r5已独审PASS并root集成签收：147 fullPython、39独审、当前组合binary19真实driver/helper故障恢复全部PASS，12file manifest ac10bb1dadeec8da9cb7a60a18b6acab60d6a2bf43d1afc520a01f593ac0b7d2。r1半frame P1、r3 socket初始mode/后chmod竞态、fixture upgrade EOF spin、r4 owner退出与historyEOF竞态均有因果红绿证据；未放宽生产guard、不重发/autoACK。当前commit/push T05，T06A已从a14affc4建立隔离t06a，/root/implement_t06a负责wait决策局部heldFD/强hash分层和基准，root采用<=3FD、cheap kernel/socket每轮核、decision返回前force强核、50/100/200ms退避合同。没有活动T05测试；外部t05-evidence全部旧failure保留。T04 r4原hardlink问题已独审转绿，但新晚到path replacement反例会误删foreign inode，复审CHANGES_REQUESTED。r4独审正常62顶层/race20顶层PASS不能覆盖新误删；报告reviews/T04-review-r4.md。所有独审会话结束，root已移交worker唯一写权做r5先隔离entry再核身份的最小修复；没有边跑边改。

root已读T04调查，笔记plans/T04-design-notes.md。T06A依赖T05/T07（T07已完成）；T06B依赖T03/T04；T08依赖T02/T03/T04/T05/T07；T09条件native gate可blocked但代码纳包需独审。最终T11冻结包/T12同包真实两方向/T13最终集成审查尚未开始。

用户live授权见docs/live-budget.md：本机现有账户、最低已验证档；第三方Claude默认模型不切换；每方向180秒、最多2worker、worker每段60秒总90秒。未跑live，不继承旧内存凭据。core/native final NOT_RUN，handoff DRAFT，不可称整体完成。

2026-11-05 13:30校正：T04 worker在r4 final后已idle；root误用send_message不会触发新turn，期间实际没有r5写入/测试，不能把等待当进度。现已用followup_task正式重新启动r5，采用私有0700隔离dir+no-overwrite原子rename捕获entry后核inode再unlink、无法证明身份保留charge的最小协议。T06A worker已final全部会话结束（4file patch98cf0539，full301pass8skip，含bindingwatch最终benchmark），/root/review_t06a只读独审中；其临时缓存已清理，源码与报告保留t06a-evidence。T08尚无生产实现，只在t08-research保存SDK1.8官方source+CLI0.166本地schema+tool信任草案，不误算done。root暂无活动工具会话。

T06A独审r1 CHANGES_REQUESTED：37聚焦PASS/12恢复PASS、3组独立计数核验，但root指出的optional metrics finally写失败已由reviewer真实driver复现（0644观测file导致failed/driver_status、ACK0）。reviewer全部会话已结束，root已用followup_task正式触发implement_t06a r2唯一writer；仅观测写best-effort EntryError/OSError，不吞CancelledError/业务核验错误，补永久真实红绿与cancel/ownerdetach保留语义。不得提交旧r1。

T06A r2已独审PASS、root组合28pass，准备提交。T04 r5 normal/race各51events/23顶层、vet/build0，源码b9c15ca0…7112f/patch2e4e0616…791f1全部会话结束。原review_t04 r5 turn因平台风险识别异常退出无结论；root已新spawn review_t04_final只读普通代码复核现有回归，未启用更宽权限/模型，不能算原review通过。未知entry原子隔离后保留inode/content及charge，v3 cleanup_reason说明barrier/tombstone metadata，不假称runtime全cleanup。

T04 r5新reviewer review_t04_final已稳定完成normal/race TestReworkRefs各51events/23顶层PASS，仍在已有legacy/CLI/Host/golden/ACK合同回归，未出最终结论。root尚未合入T04。预期server.go手动组合3hunk：追加RefsV3字段；Ready同时保留managed_owner_projection_v1、owner_rework_refs_v3；KindRequestChanges在authorizeOwnerAction后分legacy/v3且均传actionCtx。须新增5phase wrong-token/dead-owner组合回归，并在稳定integration树验证/另请reviewer签集成差异后才T04commit。T04 source manifest是全树2778文件，不可把它当changed_files；delivery-manifest-r5列25产品变更且排除T07helper/legacyhook。root当前无活动工具会话。

T04 r5已独审+组合签收PASS，root最终26file manifest匹配，5pkg race114pass与独立新coordinator12pass均终态完成；source仍冻结。现在提交/push T04，随后T06B依赖满足可隔离worker实施，T08依赖也满足由root隔离实施；共享Summary结构维持兼容，不并改同树。T09、T11/T12/T13仍待后续。root当前无活动工具会话。
