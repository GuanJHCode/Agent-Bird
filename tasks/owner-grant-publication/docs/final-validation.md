# Owner bind authority publication 修复验收

修复实际公开 `owner_bind` 在capability发布失败后仍留下授权行的问题。固定生产 `df558ee`：Store先在rooted ownerTransaction内验证registration与INSERT，Coordinator在持有root/owners/file身份下完成O_EXCL0600文件及目录Sync，然后才COMMIT。没有schema或rebind协议变化。

callback不可重入Store且从不删除文件。callback/precommit失败先执行同步ROLLBACK；只有本次Rollback明确nil且未尝试COMMIT才返回live Go-only rolledBack结果。Coordinator随后必须重新验证root/held目录、named/held新文件、UID/nlink/mode/size/expected prefix才能删除本次对象。ErrTxDone、rollback异常、COMMIT尝试、任何身份漂移都保留文件并不授予删除权限；不从冷文件恢复授权。COMMIT失败使用现物理连接ROLLBACK+discard，留下文件亦无法通过DB grant校验。legacy CreateOwnerGrant也改为受控事务。

合法state root祖先symlink仍可用：按opened root dev/inode绑定Store authority，返回canonical capability路径，保留原localOwner校验契约。

## 已执行证据

- `bind-publication-red` 1top3namedFAIL仅为Unix socket路径过长的fixture失败，未到业务断言。改短私有路径后clean2703d50 `red-02` 1top3named真实FAIL，SHA267156f2e064c6b722daea9933a676e28e439c92a33fee0155fedfd5f49fe9b9：只读owners目录失败却有1grant、后续unidentified gate错误放行原ancestry；原autocommit失败也没有discard物理连接。
- cleanf23e861 `green-01` 1top3namedPASS：真实owner_bind、池内/独立连接零grant、后续ancestry拒绝、恢复目录后唯一valid grant；deferred-FK实际COMMIT失败保留精确无效文件、两连接零fault/zero grant、原连接discard、同DB再次成功。
- cleanefc5e9f `final-race` 整批 **17 PASS / 3 FAIL、exit1**，不得改判。Store独立子集3top13named全部PASS；Coordinator的两个/tmp合法alias回归失败和一个旧rebind协议oracle失败被识别。
- cleane50bf47 `delta-race` **6top8namedPASS、0FAIL/SKIP、exit0**；SHA7bc48bbe877f33a88afb4408495cf4621bb1383535bf3b662b6e62578fad2d7c。含显式ancestor-symlink bind与实际localOwner读取。旧KindRebindOwner按已集成F10合同前置protocol_required，其他unidentified gate仍要求deferred。
- e50仅Coordinator alias两行及相应测试变化，Store源码与efc一致，复用Store3top13named，最终有效组合为9top21named；不重复全Store。原Wait/tree直接IPC回归已有实际/bin/cat origin Wait0，本次owner-bind测试本身没有创建Host/Provider进程；unidentified segment只为Store门禁fixture，不冒充进程退出证明。
- ROOT wholebuild及Store/Coordinator vet在efc实际exit0/空stdout/stderr；e50相关修改后wholebuild与Coordinator vet再次exit0/空输出。df558ee的Coordinator私有写前root检查增量已由最终race编译且相关vet exit0；整合后的最终build另记。

原始command/HEAD/dirty/Go1.26.8、JSON逐具名结果、stdout/stderr/exit及ROOT汇总见[data](../data/final-receipt.json)。所有失败保留；测试源码在执行前固定commit。driver指针相等检查可发现未discard；极低概率地址复用只会造成假失败，不会制造假通过。

## 自动提交审计范围

此审计从clean521ea52指定七处d.sql.ExecContext出发，不重新宣称整库SQL故障关闭。CreateOwnerGrant是当前owner_bind公开路径，本轮已替换为显式受控事务。ConfigureConcurrency只在启动loadStartupLimits调用，失败NewServer关闭DB/lock；CreateRun/CreateTask仅legacy/test fixture；FinishAttempt仅旧DB-backed Host（当前公共Execute拒绝，实际NewIPC/ExecuteLaunch不persistDB）；AcknowledgeAttempt仅旧Host.Ack无仓内生产caller；CreateQuestion无调用者。后六处没有同等级当前运行入口，但尚无其ENOSPC/IOERR/INTERRUPT动态资格。原[显式COMMIT清单](../../store-transaction-audit/docs/audit.md)按其原范围保留。

文件创建后callback失败→rollback→unlink路径本轮只有源码审查，未用故障注入假冒物理fsync/断电；真实整盘故障、恶意同UID并发替换、冷恢复及全部权限/资源故障矩阵未由此证明。没有真实模型、用户安装替换、push或release；F12 Formal60仍FAILED，Intel/Windows原生延期。


## 最后写前root检查与闭合

增量独审发现alias在authority.Validate后、OpenRoot前切到另一root时，旧e50会先在replacement创建owners再拒绝。b33066a真实retarget RED 1top1namedFAIL，SHA441b897d8e07f7fd6fd29f303d6025a7d86ac0f448623b8695ec6ec73154f9d4，原失败保留。df558ee把同一heldroot UID/0700/dev+inode检查提前到任何Mkdir前，后续完整检查保留；固定增量生产/测试独审PASS。

ROOT最后df558ee Mac `bind-root-final-race` 3top5namedPASS0FAIL/SKIP/exit0，重验实际FS失败/COMMIT、合法alias和新retarget；具体SHA见final-receipt。旧e50 6/8中其余注册/IPC控制用例及efc Store3/13按未变语义复用，不再重复全量，也不把此前exit1整批改判。

Linux最终df558ee同3top5namedPASS0FAIL/SKIP，raw SHA dc6bd04d1374e92a9ec6cd95a16d5c1b020708d2389fa61b0247528caf59cb4c；此前e50 2/4只作历史证据，不冒称df5。ROOT再次独立核原attach/state exit0、nonOOM、闭集具名集合/原hash、两个exactCID均NoSuch及两binary精确absent；worker删除前UID/nlink/dev/inode/hash保留，合计42,698,106bytes，raw/empty任务目录保留。执行器复用已审F10 runner，差异只固定selector/任务路径/container前缀，无pull/network/用户安装。见[Linux最终报告](../../owner-grant-linux/docs/bind-root-02.md)。

本次没有后台产品进程或容器残留。Store、公开owner-bind及最终小增量审查闭合；发布和最终包验收仍单独绑定整合候选。
