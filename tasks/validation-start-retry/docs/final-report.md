# F01 受限 validate 启动重试交付

已完成授权的 bounded vertical slice。最后生产源码 `b5899d2`，最终源码/测试快照 `6b17be9`；ROOT确认最终生产与测试oracle独审PASS、无blocker。ROOT接管独立验收/集成，worker未修改integration、未安装/推送/发布。

## 行为与源码

- v1 validate + AutoDirectory + 显式有限policy + 原native tool pin。只有整个sandbox wrapper原cmd.Start EAGAIN/EINTR、nilProcess/State、Context live形成私有单次typed proof。原Handle始终由原Stop/Wait/tree管理；冷恢复不从JSON/journal重建proof。`process/never_started.go`、`host/validation_start.go`。
- Host原路径/副本双pin，32MiB上限、64KiB流式copy、private0500工具、4096byte journal/8192 metadata escrow；原live未启动或原Wait/tree之后精确inode/hash/目录身份cleanup。普通明确pre-Start拒绝也清理并带LaunchRejected；cleanup/persist失败UNKNOWN保留。`host/validation_tool.go`、`host/validation_tool_journal.go`、`host/host.go`、`host/profile.go`。
- 独立candidate_validation_retry冻结完整source receipt/body、vector、adapter、pin、revision、物理Host；ACK/释放/队列/binding/quota/backoff同事务，claim与bound replay重核，缺失/孤儿/retired新binding拒绝。source snapshot读FS在只读gate，写tx仅重核durable事实。`store/validation_retry.go`、`store/host_artifact_intake.go`、`store/host_events.go`、现有decision/policy生命周期文件。
- ROOT共享契约已cherry-pick：schema34 validation_tool_reservations、完整grant/intent hash、全局raw/disk账本、8192 retained metadata、原physical绑定与authenticated terminal release。未借用固定v1 slots，也未掩盖tool物理占用；无schema方案因实际资源证明缺口正式收紧为schema34。

源码均在 `tools/orchestrator/internal/`。必要公开字段/用法及容量、平台限制见 [usage-and-limits.md](usage-and-limits.md)。

## 最终有效证据

测试数只计具名test action；top为无slash的顶层，named包含top及subtest，不计package pass。

| 证据 | 实际结果 | 范围 |
| --- | --- | --- |
| [final-runtime-race stdout](../data/final-runtime-race.stdout.jsonl) | exit0，Host7top33named + Store8top48named = 15top81named PASS，0FAIL/SKIP | clean d663785；真实Host/Git/Store新attempt链、profile actor、Host proof/cleanup/prepare、Store queue/quota/actualCOMMIT/工具migration/release/history |
| [native-cleanup-01 stdout](../data/native-cleanup-01.stdout.jsonl) | race exit0，3top5named PASS，0FAIL/SKIP | 原Wait/tree guarded fixture cleanup改动后完整native链、profile、活original Handle保留反例；对应40123af增量 |
| [native-profile-cleanup-02 stdout](../data/native-profile-cleanup-02.stdout.jsonl) | race exit0，1top3named PASS，0FAIL/SKIP | 删除无条件子例chmod后一行cleanup delta，对应6b17be9；完整链/Store未受影响证据复用 |
| [store-facts-green-03 stdout](../data/store-facts-green-03.stdout.jsonl) | exit0，1top4named PASS | 独立validation_start authority guard；最终race regex未选择，之后生产delta未影响此路径 |

完整runtime race stdout SHA256 `ad273f45cfaee6627b2e999ec4a2a23d0a207cff35bf3636ac34f4c766eb8c42`。每batch command/source/dirty metadata、stdout/stderr/exit均保留，详见 [evidence-index.json](../data/evidence-index.json) 与 [milestones.md](milestones.md)。cleanup两批在提交前实际dirty测试源码上验证，其固定提交与仅受影响profile复验明确记录，不冒称clean HEAD执行。

ROOT独立verification WT记录：84e82a8 core Store/contract/adapter/process vet；b5899d2 whole `go build ./...`及Host/Coordinator/CLI vet；d663785 Host/Store/actor test vet；40123af cleanup Host/actor vet，均exit0且stdout/stderr空。最后一行删除由独审确认，不重复whole build。ROOT最终报告/receipt由ROOT集成。

真实完整链：controlled native source由实际Host profile写tracked file并Managed Git freeze；validate实际新worktree物化与sealed tool，原sandbox wrapper受控Start EAGAIN；authenticated Store ACK/cleanup raw释放/newkind队列；公开CollectPending/AckDecision处理原拒绝，fresh attempt成功、ValidationEvidence command/pin绑定、原Wait/tree Exited cleanup、owner accept。原LaunchRejected审计行仍acked，两份removed journal仍各8192metadata计费；exact重复terminal不重复cleanup/refund。

实际profile七操作正对照：同UID无sandbox self chmod/unlink、parent/ancestor chmod、peer chmod/write/unlink全部errno0，actual Host profile全部EPERM1；每个actor由原Handle Wait/tree确证。未以self-overwrite可能的ETXTBSY/签名限制证明sandbox。

跨physical未入Store旧spool反例拒绝退款/入队，reserved保留；Store已durable identical event仍可ACK replay。原Tool/decision历史不可擦除、真实FK COMMIT rollback及相邻历史migration复用ROOT有效证据，不称全Store通过。

## 失败、清理与边界

全部原失败保留，包括真实source形状/prepare业务RED、编译错误及测试oracle错误；不删除断言、不覆盖raw。唯一`store-policy-red` launcher误传`--`未执行Go，原始command/空输出存在但没有exit文件，明确缺失，不补造。完整37批原始索引包括所有FAIL，无隐藏最终batch失败。

native实际Start测试根采用owned os.MkdirTemp；setup Go build也用原Handle Wait/tree。任何Host UNKNOWN/错误、未确证Handle、failed test保留精确路径且不chmod/delete；只有全部原proof成立和成功测试后才显式清理。live actor等待stdin时cleanup确实保留，原Stop/Wait/tree后才删除。最早未Start boundary权限残留仅经exact inode/UID/nlink/hash/无actor检查后清理，见 [owned-temp-cleanup.json](../data/owned-temp-cleanup.json)；本轮正常native fixtures已清理。

未验证/未扩大：自然内核EAGAIN资源耗尽、真实provider/model/account、物理断电、Intel/Windows、Linux真实Git/profile链、已安装产品/F13新源码包、动态库/子命令完全hermetic。任意已启动validate失败或implement/compose/integrate失败、manual retry不在授权slice内。没有活测试会话，worker交回worktree后停止写入。
