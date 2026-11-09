# .5 新双向试验准备独立审查

PASS / BOOTSTRAP_PASS_NOT_LIVE。安全审查者 `/root/review_r5_live_preparation` 分别审查normal-Claude/source隔离策略、严格串行与永久消费状态机、唯一包identity pin窄改和真实51-pin bootstrap；root实际执行无模型setup并核验当前内存source proof/owned文件密钥扫描。

初次审查BLOCKED：两个方向缺少强制串行状态机。修复后独占root flock覆盖整个launch，锁文件owner0600/单link/不删除inode、CLOEXEC；不可重置started/completed绑定same setup/package/result路径与hash；第二方向须第一完整SCENARIO_PASS/owned清理/ledger收敛，并以同env执行固定scenario finish只读新鲜复验。受影响PUBLIC fixtures110pass/0fail/error/skip，首次collectionerror及中间108pass均保留；原192/32有效历史fixture证据保留。

唯一identity SHA75f7358740914e1bcfe9463322cfda6ce1457e1f85475cd235c5b0f585761069绑定source7eb8c8d434a584fd109d9bed96d8c3a82f3fea98及真实.5包。实际setup进程exit0/stderr0，独立重算51pins无缺失/漂移，全7scripts、原始providerlocks、合同模型/budgets、installed/receipt链一致；原source只持久化名称/摘要，没有ENV值。关键dirs0700/files0600，两方向无live-run/started/completed/auth-preflight/scenario-result，没有controller DB/handle/worker。owner-private MCP token-key由产品生成，不读取其内容。

[source/bootstrap证据](../data/public-evidence/T12-bootstrap-r5-root.json)。root的当前source内存比较及扫描由root签收，独立审查不读取当前settings/CC DB/auth或执行helper。此PASS只批准无模型准备，不是模型/nonce/LIVE签收或用户新预算授权。真实launch还必须重新执行source-proof gate，并先取得本轮明确有界确认；旧.3预算/失败不重置。
