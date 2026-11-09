# 当前交接

当前唯一私有候选为 `.5`；源码修复、PKG、同包回归与无模型bootstrap均独立/root PASS，当前文档独审/root资料签收PASS，handoff为PREPARED。**产品 core/T12/T13 product=BLOCKED，native push=BLOCKED；`.5` 产品真实双向 LIVE=NOT_RUN。** 交接资料通过不等于产品通过。

## 冻结源码与包

分支 `feat/cross-cli-hardening-20261105`；包源 `7eb8c8d434a584fd109d9bed96d8c3a82f3fea98`、tree `2f90c500965cfe9098a9ff203078e5dfd896be36`，已推送。后续文档提交不改变包源；当前文档HEAD用git rev-parse HEAD核对。原工作区及用户未跟踪文件保留。

版本 `0.1.24-hardening.20261105.5`，本机目录 `/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t11-package-r5/artifacts/`，归档 `agent-bird-0.1.24-hardening.20261105.5-full-native.tar.gz`。

| 材料 | SHA256 |
|---|---|
| archive | `5ad3caec4f13aaf457df27422998a12a60d6c63460ea305da27cb004a1da5db1` |
| Go binary | `6b1d91d56d3189b04662fb56e4b6b733712fec10fb720f05c840e33becdd345b` |
| package manifest | `7f0200eefb119fe7a9360dc931ddfffd5413bc8cf4e4efac0665f01748bee3a8` |
| installed manifest（普通路径） | `34430957582f29d35223858c1a4eca7b6a2c5defa2383798367b9cb1641d3083` |
| identity file | `75f7358740914e1bcfe9463322cfda6ce1457e1f85475cd235c5b0f585761069` |

仅本机macOS arm64私有验收，固定Python绝对路径/hash；公开下载仍为v0.1.0-preview.1。没有release、签名/公证、正式分支合并或全局安装替换。

## 已完成及证据

- T00～T08/T10的实现及单项独审见[TASKS](../TASKS.json)。最新修复保留Claude cli-default来源六字段模型选择、缺失/空值区别；显式Model原合同和认证/私有HOME/ExactEnv边界不变。源码独审与root验收通过并推送，见[修复](../data/public-evidence/T08-Claude-source-default-model-root.json)。短引用route79/review47、完整HMAC/owner/完整tuple校验、8普通MCP工具/noTasks保持。
- T11 PKG-01～04独立/root PASS：一次成功build/一次package、0失败；180项index，37archive members/25files，两安装各24文件、4entries、MCP8/noTasks；5负例均拒绝，同FS整目录move保留inode仍拒绝；2ownedruntime idle/exited、SQLite11。实际installed Go smoke7pass及guard8pass，0fail/skip。见[PKG](../data/public-evidence/T11-package-r5-root.json)及[审查](../reviews/T11-package-r5-review.md)。
- REG-01 normal/race各1659pass（659顶层/1000子测试）、0fail、8既有opt-in skip；vet/全模块build0。149生产材料逐文件绑定到包源，aggregate `6f0bf52cdd3659c4ed3860f1879c419eef68c2e6e1d2606576df12983916fabb`。首normal的1个AGY二秒版本probe失败保留，底层原因未序列化；源码不变count5和fresh normal/race均通过，未放宽超时/断言。见[源绑定](../data/public-evidence/T12-source-binding-r5.json)。
- REG-02 同installed .5 binary的Python301pass、7明确native opt-in skip、0fail/error；保留真实JUnit及既有SyntaxWarning。实际Codex0.166.0/Claude2.1.316 metadata各8tools、完整source projection相等、OS拒网络、0model turn、owned groups无残留。Claude不暴露Tasks capability，由同包PKG initialize补noTasks证据。见[REG](../data/public-evidence/T12-regression-r5-root.json)及[独审](../reviews/T12-regression-r5-review.md)。
- 无模型bootstrap已独立/root PASS：固定.5 identity、51pins、全7scripts、两锁/合同/config/argv、权限/来源proof一致；无live-run、started/completed、auth-preflight、controller/worker。密钥仅root内存/精确子进程ENV，owned文件扫描无明文或编码命中。见[bootstrap](../data/public-evidence/T12-bootstrap-r5-root.json)及[安全审查](../reviews/T12-live-preparation-r5-review.md)。严格串行root lock/永久消费marker已通过110项PUBLIC fixtures，不重置方向或旧预算。

## 未完成及下一步

`.5` 真实父模型短引用传递、worker候选、父审查、真实accept/ACK与root私有FF均NOT_RUN。旧`.3`两次有界试验已结束，不能借新ID重置；需在本材料签收后确认本轮新的具体额度，再由root单次执行。拟两方向严格串行，每方向最多180秒wall含cleanup，worker每段60秒/总90秒/最多2attempt（仅自然必要返工）。合计最多6分钟scenario wall，不代表token/费用上限。Codex gpt-6-luna/low；Claude保留当前CC token/Bearer和来源默认Deepseek，不换key/账号/模型；不加付费探针，第一方向失败则不启动第二方向。

T09原线程idle/busy真实父续审仍BLOCKED，typed resume、AGY/Grok coding未开放。LIVE-03自然缺陷返工未触发，NOT_EXERCISED，不制造缺陷冒充覆盖。尚未执行.5卸载、非macOS arm64、公开发行。最终产品状态仍由必需LIVE与最终集成签收决定。

## 保留历史与现场

历史`.4`（source7b7f0421、archive1817a713…）私有包/原始证据完整保留；其首次modulecache build失败与一次受限恢复不继承到.5。`.5`从首次build即显式既有GOMODCACHE，唯一build/package均成功。旧`.1/.2/.3`也未重标/删除。

历史`.3` Codex→Claude dispatch/首inspect成功后父改写847字符引用，被正确拒绝；worker已interrupted/exited，active/unknown/reserved0，1attempt/53160ms。Claude→Codex在dispatch前10次401，无task/worker，watchdog收尾；两者无accept/ACK/FF。见[旧live](../data/public-evidence/T12-live-root.json)。后续旧Key探针首次API结果UNKNOWN、再试401，新CC最初403，均历史保留。用户将CC地址对齐ZCode后，单次独立nonce成功（input69/output24/2.332秒），只证明独立连通性；产品模型ENV缺口已修复并纳入.5。

原SDK/venv、私有runtime/request/capability、原始日志、包与审查副本保留用于恢复，维持0700/0600；没有清理归属不明文件。继承文档39处历史链接/36目标缺失保持已知限制，本轮新当前入口/证据链接需最终复核。
