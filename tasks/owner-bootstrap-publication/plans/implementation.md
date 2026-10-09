# F10 server-owned bootstrap publication

基线af5e72c（生产同3bd025e）。ROOT独占本worktree；F02 retention在另一WT独立实现，两者公共auth接线在集成时核对。已读architecture调查：CLI持lease→RPC改DB→client发布无法约束direct RPC；runtime status在Coordinator启动、reconcile与owner activate被覆盖，不能承载durable pending。

## 冻结契约

1. 新IPC `rebind_owner_published` / Ready capability `owner_rebind_publication_v1`；新CLI不持旧outerlease，调用前要求capability。旧KindRebindOwner在任何lease、localOwner、进程观察和mutation前返回`owner_rebind_publication_protocol_required`；不fallback。旧客户端/新服务端明确拒绝，历史source fixture不能叫发布版兼容。
2. 可信stateDir及认证run→唯一launch派生bootstrap/socket/spool/executable。client不给path、host token或publication声明。共享internal/hostbootstrap保留既有FD-relative lease/inode/UID/mode/bytes/lock checks；锁序FSlease→hostRegistrationMu→Store。CLI recover-host仍使用同lease，不反向上锁。
3. schema35独立publication记录，immutable scope包括run/launch/origin context/old+newgeneration/newPID+birth/attachmentproof、原bootstrap hash、期望bootstrap hash、冻结canonical目标bytes及created。unique(run,newgeneration)，同run最多一个pending，不能覆写/删除历史；mutable仅pending→published及最终timestamp。完整控制字节在变更前预约，published最大表示预付；普通旧owner_rebind行与pending在同owner transaction原子提交。
4. prepared后只接受原prior或expected bootstrap内容；发布完成fileSync/rename/dirSync及named identity/readback expected hash后Store CAS finalize。DBroot+lease身份复核；response只能来自published row，含run/launch/generation/hash。网络/IO失败不冒报success，不回滚已提交owner transition，不从目录存在猜finalized。
5. 重启不能自动finalize；exact authenticated replay验证完整immutable journal、owner_rebind与当前run/launchscope，不重新选择目标；ABA旧generation不能覆盖后续generation。bootstrap hash相同但physical inode被非法替换按现有lease拒绝。
6. pending gate独立于hoststatus：普通owner mutation统一检查，Store内部auth（bridge/ACP、resume、新base、budget、diagnostic ACK/delete）在事务内检查；Host Register/recovery、originalowner Prepare/Bind/Applied/Activate、scheduler Claim/PendingLaunches不能创建执行权限。只读status/collect/诊断可用于观察；rebind exact replay专用认证允许pending。旧bridgebinding已随rebind变STALE，不重开输出权限。
7. 不启动/接管进程、不伪造Wait；所有原身份、quiescent、UNKNOWN、预算、pendingauthorization与签名链约束保留。Windows提供unsupported编译stub，原生延期。

## 里程碑与验收

- 先复用现有public_authorization_rebind/host_launch_lease等fixture，保留direct IPC绕锁业务RED。
- schema/immutable journal+quota+COMMIT回滚与pending gate；使用现有ownerTransaction和rootauthority，不另造事务封装。旧schema测试只扩展空新表剥离规则，不擦历史。
- 共享lease搬迁及新server RPC、CLI capability接线。固定source独审与ROOT/独立WT build/vet可并行。
- 少量定向Store、真实IPC/CLI测试：DB后FS失败→重开仍pending→修复同request、FS后finalize前、finalize后response丢失、direct/direct及recover/direct锁顺序、旧RPC拒绝无DB/FS变动；真实历史固定源码CLI拒绝另列，不声称成功跨版本。保留全部raw/FAIL，最后必要race一次，复用未受影响旧恢复证据。
- 成功后主会话核原raw/源树/原Wait/cleanup，合入integration，更新支持矩阵/外层记录并对稳定最终候选封包。

禁止真实Provider/model/account、用户安装替换、push/release。Formal60仍FAILED，Intel/Windows延期；本slice不声明所有F10完成。

## 追加冻结：staging容量和真实mid-write崩溃

共享Lease.Publish的旧随机stage只用于已有CLI进程内错误模型；新server协议不能直接照搬。SIGKILL不跑defer，会使每次重放新增64KiB orphan。新方法使用由launchID+expectedSHA派生的稳定保留名 `.publication-<digest>`，同launch必须先完成原pending再允许新generation。

prepare前持lease核final仍prior、该稳定stage及同launch旧随机/未知stage均不存在；无journal的旧残留failclosed，不自动删或采用。schema35 journal冻结expected canonical bytes，并预付其持久control成本+一份len(expected) scratch；pending中exact replay只计一次，published后无stage且final durable/readback通过才原子释放scratch。仍用现有storage accounting/admission，不另建独立quota框架。

pending replay可打开同稳定stage：必须同UID regular0600/nlink1、size<=expected、FD/named inode稳定。exact expected复用并fsync；exact prefix（含空）才可补写剩余字节；非prefix/超长/硬软链接/身份mode漂移保留并拒绝。写全后fileSync，复核stage内容/inode及final prior/expected规则，rename、dirSync、named readback。普通错误最多删除本次新建且同inode的stage并Sync父目录，既有replay stage不盲删。新协议需要私有per-call checkpoint的真实child SIGKILL覆盖O_EXCL/partial/fileSync/rename前，重复exact replay证明stage数<=1、scratch一次收费/一次释放；不是旧random publisher的资格化。

## 事务gate检查点

共同requirePublishedOwnerTx必须位于各自写事务内；Coordinator预鉴权不是TOCTOU修复。至少核AckDelivery、各Resume、Continuation/Retry/Rework、ReviewResult/AnswerQuestion/RequestStop/ReviseBudget；authenticateOwnerResumeTx、authorizeNewBase、budget内部鉴权；Host Register/AuthorizeHostRebind/Begin+FinishReconcile/RecoveryScope；originalOwner Prepare/Bind/Applied/Activate；ClaimReady/PendingLaunches；bridgeOwnerTx/ACP；diagnostics Ack/Delete及新retention Apply每item底层Delete。只读状态/summary/collect/wait/diagnostics Read与只降低authority的offline/fence不需泛化阻断。启动不得抹publication表，schema34→35不为历史rebind猜bootstrap proof。
