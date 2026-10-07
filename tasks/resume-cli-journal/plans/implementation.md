# Resume CLI durable request journal

Base62e2091，唯一writer的隔离WT resume-cli-journal；仅拥有cmd/orchestrator/resume_journal.go、resume_journal_test.go与本任务artifacts；ROOT后续明确授权新增resume_journal_publish_{darwin,linux,other}.go用于窄原子exclusive publication。不改main/task_entry/Store/contract；不派生、不启动Host/RPC、不容器/race/full/native/安装/push。ROOT集成与共享验收。

内部API沿ROOT冻结名称，request Version/State/ControlFile/TaskID/WorkRevision/CommandID，不存token/receipt。implicit复用唯一same canonical controlfile/task/rev pending；none随机ID独占durable create；multiple typed ambiguity列IDs。completed不implicit复用；explicit仅same tuple可重放，健康目录未出现ID可新建。ID与Store相同UTF8/nonempty/<=128B/noNUL/trim exact，hash filename不限制punctuation/Unicode。

State输入规范化existingcanonical0700/euid目录，child resume-requests0700 descriptoranchored。select才可Mkdirat child并fsync parent。lock .lock openat O_CREAT|NOFOLLOW0600/nlink1/euid/zero，FlockEX|NB忙立即拒，操作前后命名inode校验。锁仅覆盖scan/select/create或complete，不跨RPC。全目录strict canonicalJSON+newline（16KiB），坏/unknown/替换/权限/链接异常failclosed。exclusive create fsyncfile+parent；complete temporary+fsync+renameat+parentfsync，不创建missing。temp仅本操作已证明归属可谨慎清理。

ROOT裁定：当次controlFile用Abs+EvalSymlinks可以，caller仍必须先loadControlCapability鉴权，不把leafsymlink当cap认证。历史record.ControlFile只验abs+clean与字段语法，不要求存在；旧completedcap已清理不影响健康新请求。不读写cap/token。

里程碑：1测试与API scaffold观察行为RED；2最小安全journal实现；3真实filesystem lifecycle/crash/explicit/ambiguity/busy/concurrency/安全矩阵，focused uncachedJSON+vet；4source anchors/counts/局限report并本branchcommit，ROOT独立review/集成。

## ROOT后续原子发布裁定

ROOT批准pending和complete先在held state FD（records扫描域外）写0600/nlink1 temp，前缀.resume-journal-pending-/.resume-journal-complete-，完整filefsync后跨FD原子publish/rename到held child。pending必须Darwin RENAME_EXCL / Linux RENAME_NOREPLACE（模式参考internal/install private helper），不支持平台明确拒绝，不copy/link fallback；成功后state+child两目录fsync并复验。成功发布pending之后任何失败不删除record。只完整成功后ROOT才可RPC。

abrupt crash遗留state-prefix temp保留，不按前缀猜归属自动GC，也不阻塞records全扫描。正常异常cleanup仅本调用exclusive inode且命名/owner/mode/nlink/stat/目录复验通过。未来GC独立授权，真实断电不在本批验收范围。

源码已冻结给ROOT独立安全审查。最后focused命令13 top pass /58 named pass /4 UID-变更子例SKIP，0fail；本机chown EPERM，ROOT将用受控capCHOWN matrix补验。集成仍由ROOT新owner-public-wiring WT负责。
