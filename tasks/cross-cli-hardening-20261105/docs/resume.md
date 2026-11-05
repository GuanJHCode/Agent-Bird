# 当前恢复入口

集成worktree /Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/integration，分支 feat/cross-cli-hardening-20261105。原仓库 /Users/guanjunhui/code/ai/codex-cli-orchestration-design 保持原分支与未跟踪文件。root唯一写集成树；子agent不递归，最多两个并发子agent；每功能另一agent独审PASS、root核验/签收后才commit并push任务分支。未授权merge正式分支/release/全局安装替换。

已独审/签收并push：T00=08aa0184，T02=428a5877，T10=bde4bacf，T01=c29ac2bb，T03=2347d34c；T07独审73pass/集成55pass，当前提交。相关reviews和new reports可查，未将skip/旧树报告混为当前全验收。

活动负责人/树（均位于上述integration的同级tmp目录）：
- /root/implement_t04，t04 HEAD2347d34c：v3正文refs/schema11，ADR004已root采用（256KiB正文，512KiB最终stdin硬限，chunk16KiB；旧v1/v2golden保留，显式CLI v3）。registry/uploads/pins/生命周期、Host同bytes组装/门禁、CLI已部分实现；还在故障/端到端验证，不可提交。证据tmp/t04-evidence。它只读依赖T07 owner_action_guard.go快照SHA d3704ec2d0d1ace2b2575af7ab53238b9868240ecd593afb27733d12f9649150，必须排除其feature patch，由T07先集成。
- /root/implement_t07已final，/root/review_t07正在只读独审冻结t07 HEAD2347d34c：owner投影采用显式owner_projection_version:1与Ready managed_owner_projection_v1，legacy默认response不含新字段；全部KindOwnerBind仅managed_process，非native会话证明。local新提交空delivery=>collect、显式非collect拒绝。实证旧dead owner可用旧control排resume_queued，root授权run-specific live/birth+local peer ancestry与同tx snapshot/CAS guard，仅六类resume/retry/rework/request_changes/accept/answer；query/stop/ACK保留原bearer收尾合同，recover-host专用新owner能力/对账路径不套旧owner-live通用guard。shared helper在store/owner_action_guard.go，T04唯一负责其QueueRequestChanges/refs tx插hook，T07负责legacy owner_rework.go中3行同tx guard hook；T04保持完全相同hook并负责其余v3内容。两agent Ready cap数组与server局部hunk由root合并，不能丢cap/互改授权段。证据tmp/t07-evidence。
- root目前唯一写t05树，源码r4已冻结（12文件manifest SHA9ddaf0d5fe7faf2593a72c0fa953d4a6451557580f8988268fe504a057a47204，patch SHA86a5a28724840cf922f96fc9dfa60dbb66f86b5444a4e4a83159927f102e4c29）。T05生产半frame超时P1已修，r3独审曾因ownerready超时BLOCKED。root先确定性修fixture HTTP upgrade EOF死循环，再以catch-only traceback捕获activation_service.py:846 `backend socket identity changed before owner receipt`。fixture start_unix_server先按继承umask创建socket、await后chmod600，在严格identity快照中产生mode变化；受控暂停server-return测试确认旧树022 umask创建0755红，新树手动bind期间umask0177从birth即0600（继承022/077两测绿）。生产guard完全未改。诊断monkeypatch源码已外部备份后从交付树撤除，所有旧failure/JUnit私有现场保留t05-evidence/root-startup-fix；不将十次未复现当归因证据。r4 deadlines31pass；全Python145pass1fail暴露owner退出与EOF同发生时awaiting_history停滞，已加私有repinned deterministic SIGKILL/restart+EOF红绿并修投影前attachment重核。r5全Python147pass/0fail/0skip，需独立复审；Go生产源码与此前race10pass/vet0树未变。需全跑终态和另一agent r4独审通过，仍不可提交。

root已读T04调查，笔记plans/T04-design-notes.md。T06A依赖T05/T07；T06B依赖T03/T04；T08依赖T02/T03/T04/T05/T07；T09条件native gate可blocked但代码纳包需独审。最终T11冻结包/T12同包真实两方向/T13最终集成审查尚未开始。

用户live授权见docs/live-budget.md：本机现有账户、最低已验证档；第三方Claude默认模型不切换；每方向180秒、最多2worker、worker每段60秒总90秒。未跑live，不继承旧内存凭据。core/native final NOT_RUN，handoff DRAFT，不可称整体完成。
