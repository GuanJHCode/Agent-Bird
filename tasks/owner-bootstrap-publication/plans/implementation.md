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
