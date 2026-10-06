# F02 budget revision

- Scope: schema 18, immutable owner-authorized budget revision ledger and the
  related Store/IPC/CLI/coordinator projections defined by the frozen brief.
- Owner: ROOT is the only writer; both implementation workers have handed back ownership.
- Baseline: `6b0ad5cf4c1bbd8ef6a9f63bafd39d846d0143d8` on
  `feat/closure-budget-revision-20261007`.
- Milestone: Store schema 18 and the owner-authorized revision path are
  implemented. `budget_revisions` and `budget_revision_receipts` are
  append-only, quota-accounted ledgers. `ReviseBudget` authenticates the owner,
  replays exact commands before CAS, persists complete absolute snapshots, and
  updates only the effective reader used by future capacity/grant checks.
- Migration fixtures now remove v18 objects before declaring historical schema
  versions, keeping production DDL strict. Tests cover real v17->18 upgrade,
  DDL-failure rollback, too-new rejection, immutable update/delete refusal,
  CAS/replay, effective caps, and quota rollback.
- Evidence: `data/store-budget-revision-api-quota.json` is the focused Store
  suite; `data/store-budget-revision-suite-final.json` is the complete Store
  suite (852 named passes, 0 skips, 0 failures; session 95379).
- Follow-up Store contract evidence: `data/store-budget-revision-contracts-final2.json`
  covers revision-2 replay/payload conflict, cross-handle CAS, complete shared
  snapshots, invalid command rollback, active policy/grant preservation, and
  migration/receipt-insert rollback (17 named passes, 0 failures).
- Remaining scope: Host/ACK/lifecycle retention, summary projection, IPC and
  CLI are intentionally not included in this Store milestone.
- Frozen semantics: revision 0 reads the baseline; every later revision stores
  the complete absolute effective-cap vector, never an arithmetic delta. A
  policy-failed task with a pending runtime/report event pins its Host until the
  owner handles every failure action slot; a revision never revives an already
  released or dead owner. For this feature's policy-failed retry, ACK states
  `handled`, `waiting_user`, `stale`, and `rejected` close first-retry
  eligibility; an existing exact retry replay remains valid, and a revision
  itself never consumes the slot.
- Shared brief review passed before production implementation. Native
  CLI/process/sandbox checks remain ROOT-owned.

ROOT接管与Store里程碑验收（2026-10-07）：

- a42db2f之后ROOT独立发现groupcap可降低和segment受旧attemptcap误拒，worker7e8b770已以实际API RED→GREEN修复。ROOT补legacy拒绝，以及真实policy retry promise→修订→DB reopen→Claim原200ms hold，hash/payload/due/slot保持。不是用SQL伪造promise。
- ROOT最后把owner token hash比较对齐既有ValidateTaskOwner的constant-time惯例；完整Store增量与最后鉴权小修分别独立审查PASS。ROOT是本WT唯一writer，两个实现worker已交回。
- Mac normal98021/race2272各11top19named PASS，0fail/skip；最后鉴权小修由普通43049/race30716各1top7named覆盖，替代相同旧case、其余复用，不能累计算独立case。ROOT build/vet/gofmt-l/diffcheck通过。a42的fullStore95379 318top852named是修改前广泛回归，不当作最后API正确性的证明。
- Docker Linux arm64同范围11top19named PASS、无skip。crossbuild99414 exit0；run terminal chunk8dc95c exit0，container exit0；精确CID5aba338ffa9baa05c8013b84d46f6014116723925f267573132f28522720fbe6已删除并inspect确认不存在。无账号/模型/native Provider，也未跑Linux race。
- 原日志/session/hash与复用规则保存在data/root-store-milestone-receipt.json。此为schema/API/reader阶段，Host retention、ACK slot closure、lifecycle/summary有效预算投影、严格JSON public IPC/CLI及真实failed-root retry/interrupted resume仍须接入验收；完整F02未完成且本分支未集成。

ROOT公开接线里程碑（2026-10-07）：

- worker的初版守卫影响legacy且漏report pending，ROOT以真实Store Failed/Exited、runtime/report双路径和四种ACK有效RED修正。policy failed保留待决策Host，先retry后ACK仍可按原业务tuple重放，同delivery新command ID不新建决定；legacy合同保持。增额本身不消费failure slot。
- status/summary新增可选effective_budget，明确revision 0并保留baseline execution_policy；快照读在同一TX。配置pin枚举和删除TX共用最新预算predicate，四类旧cap误释放均有有效RED→GREEN。
- 公开budget-revise经private 0600/64KiB请求文件、严格字段/重复key/显式CAS解码、peer/worker/owner权限验证到Store；原有ready调度可wake，增额不创建attempt、不复活原Host。真实Coordinator UDS + CLI run入口校验通过，尚不是build binary→原worker链验收。README记完整请求字段与增额/retry/ACK顺序。
- fullStore77626 exit0：330top884named，0fail/skip。public normal94683 exit0：4top23named。后补sameattempt resume/UNKNOWN/blocked及业务tuple重放最终normal72832 exit0：4top6named。旧fullStore原有测试有效，新增测试以新日志为准。
- race41966整体FAIL：UNKNOWN fixture仅1槽，正确保留旧reservation返回no_execution_slot；改为留第二槽且仍要求ErrNoReady后66846 exit0，2top4named，同时覆盖新增replay断言。未改生产；替代受影响case后有效race31top82named，0fail/skip，其余v1 PASS复用不重复累加。interrupted/blocked为真实Store事件范围，不升级为原生Provider证明。
- Linux arm64预算范围19top39named PASS无skip，crossbuild19752/run93801/container均exit0；owned CID2cd11ecf...c8cdc9e精确删除且inspect已不存在。固定缓存镜像、禁网、只读root、nobody、无cap，未跑Linux race/CLI/Provider。Mac build64838/vet950e6f/diffcheck0。
- 独立审查PASS。原CommandID blocker经既有协议和review_retry_test明确新delivery重放合同撤回，未按错误建议破坏legacy。来源/原session/JSON及SHA见data/root-public-milestone-receipt.json。
- 下一步：实际构建CLI、受控worker Failed/Exited→旧预算拒绝→原Host保留→增额→原failure slot retry→实际新worker/newattempt；随后原生resume与组合验收。详细日志、owner分支/最终集成继续OPEN，不声明完整F02或全目标完成。

ROOT真实CLI预算retry/resume验收（2026-10-07）：

- 在c342ecb之上只增加/扩展三个测试文件，未改产品。复用既有真实CLI/original-owner harness，独立fixture的两层O_EXCL marker区分首worker与第二worker，并拒绝第三Start。真实build binary经公开submit、签名owner与原OS child，不是直接调用Store模拟。
- retry链：首worker原PID/birth的唯一Exited=7且ESRCH；原Host保持ready；旧attempt cap明确拒绝retry；公开budget-revise只增额、不改failed状态、不占failure slot；原slot explicit retry启动新worker和恰第二attempt。resume链：公开stop产生owner_stop_requested/非零原退出，增额后resume使用同attempt/新segment与新worker。两条链均保留原owner/Host和原binding JSON字节；旧used/grant/deadline保持，新retry grant45000，新resume grant45000-old_used。
- 最终第二worker均有唯一ExitCode0与reap，Coordinator原Cmd.Wait0；source/owner只凭已登记tuple确认退出，不当它们的Wait0。所有成功runtime由既有cleanup确认后删除，无本轮遗留actor/容器。
- normal4367 exit0，2top2named；最终race83926 exit0，含既有首次链共3top3named，全部0fail/skip；vet8eda82/diffcheck0。产品未改，实际每个native场景go build成功，已有c342ecb Store/Linux证据仍有效。独立fixture审查PASS。
- race仅覆盖test和复制的受控worker；单独go build的CLI/Coordinator/owner不是race binary。不证明真实账号模型/Provider/TUI或其他平台整链。receipt=data/root-native-budget-receipt.json。预算修订受控本机垂直链已通过，F02有界详细日志、分支组合/最终集成仍OPEN。


## 有界捕获截断判定（5ebb6fb后，进行中）

ROOT唯一writer修复明确F02日志误报，不扩展日志权限/cap/原始行持久化。真实Host /bin/cat 4096条合法短行总计1MiB的RED30415 exit1：无drop却truncated=true；+1byte case通过。Handle在实际丢字节时记录flag，同锁OutputSnapshot返回内容+flag，旧Output兼容；Host将capture flag与parser长行/record cap截断合并。生产三文件/测试两文件独审PASS。最终normal98943 exit0，process3top3named+Host12top16named，0fail/skip；最终race34333待ROOT终态重数。build/vet41929 exit0、diffcheck0；后续只追加parser regression和失败路径join，生产未变。Linux pure OutputSnapshot交叉构建首次错误cwd导致no main module（非产品RED），已切正确Go module。详细日志入口、公开metadata投影、持久失败可见性仍OPEN。


ROOT截断修复最终证据：normal98943/race34333均exit0，每种15top19named（process3/3+Host12/16），0fail/skip。真实Host精确1MiB未drop=false、+1byte=true，原流仍完整drain；parser巨行/4097records仍truncated，脱敏聚合保持。build41929/vet b4512b/diffcheck0，最终5文件独审PASS。Linux arm64 pure OutputSnapshot 2top2named无skip，build d99e56/run26416/container exit0；CID1042f5bd0e1b1c1372bd8ab9022641f5fa1ca132c2c890b87029eabede4b9690已精确rm且inspect不存在。无Linux Provider/Start/race声明，证据binary保留。全部session终态，raw/hash/范围见data/root-capture-truncation-receipt.json。F02详细日志入口/公开投影/写失败可见性及集成继续OPEN；预算功能原证据保持有效。
