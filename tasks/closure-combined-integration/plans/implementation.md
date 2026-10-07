# Combined closure integration

ROOT is the only writer and holds the macOS/Docker test token. No user installation, remote push, publication or model/account invocation is authorized by this merge.

Inputs (all clean before merge):
- integration: 3d61c08952338584eb0d162c104cb55251544ed0 (indexed review and candidate policy retry, schema16)
- original owner: ea8389f7e0f620179bf648f465de3e6baeda570e (authenticated owner/runtime and bounded control/admission, schema17)
- budget: 9adb91afb1e2d6272bef973f3b8a49ad5bbb4d68 (revision/CAS/public CLI and actual capture truncation, schema18)
- integration/feature merge-base: 1faa061606e5e19c776c9d59fbdb5a1096f1ba12
- owner/budget merge-base: 6b0ad5cf4c1bbd8ef6a9f63bafd39d846d0143d8

Milestones:
1. Merge owner into the existing integration branch. Preserve all 14 integration-only commits. The automatic merge touches candidate_test.go, coordinator/server.go and gitops/candidate_runner.go; review semantic interactions, not merely textual conflicts. Verify schema17, Store policy/indexed/owner behavior and focused native candidate/owner CLI. Commit only after evidence and independent merge review.
2. Merge budget into that combined baseline. Preserve original-owner recovery and schema17 before schema18 migration. Review policy retry/ACK/Host-retention and effective-budget interactions; verify actual schema migration and native explicit budget retry/resume plus candidate policy paths. No new schema19 until this baseline is stable.
3. Run appropriate combined regressions/build/vet and independent integration review. Evidence already valid on unaffected files is retained; repeat affected combinations. Report exact test counts, failures, skips, original process Wait and platform scope.

Status: owner merge committed as 2e607ad. Budget 9adb91a is staged; readonly diagnostic retry, dependency restoration and retained configuration pins have passed independent review and normal/native checks. Final race and combined CLI checks passed; the validated code, this record and receipt are ready for the local budget merge commit. Full F01-F13 remains open; this milestone does not replace native publisher/durable-write, B3 sealing, full logging, platform/provider or final package requirements.


Owner merge validation in progress: build42688 exit0; full Store34415 exit0 (322 top / 887 named, no fail/skip). Independent merge review found the indexed streaming path dropped a nonnil Handle on post-Start error. Real-handle RED40131 exit1 reproduced both standalone cleanup and lifecycle transfer failures; private stream start helper now records original identity as unknown and uses CleanupFailedStart. Original buffered assertions were preserved and shared with two added stream tests. Independent re-review PASS. Normal95221 exit0: Gitops8/18, Host6/23, CLI3/3 (17 top /44 named). Race51286 remains running; no completion claim yet.

Budget pre-merge independent inspection: preserve integration indexed/readonly candidate policy admission together with budget latest absolute caps; preserve indexed accept plus retry exact business replay before ACK-close guards and effective-cap checks; retain all fixture routes and budget-revise authorization. Planned new combined Store regression binds indexed/config/vector/policy identity and original pending budget promise across amendment, retry and schema17→18. Actual budget merge has not started.

Budget combined regression fixture draft (read-only investigation; not implemented): integration review_retry_test.go newReviewRetryPolicyFixture(t, adapter.ProviderGrok, true) supplies real policy and dependency-vector grant, but has neither config generation nor indexed assignment. Its WithOptions helper accepts a config ref; bindIndexedConfigPlan supplies config-history setup. IndexedReview assignment must be added to the payload before SubmitPlan, not retroactively after binding. Reuse budget_revision_promise_test.go identities/hold assertions: old action_slot/policy hash/payload/due/reserved_active_ms must remain exact after amendment/reopen; new claim consumes original hold, while later new decisions use effective caps. Cover config/index/vector/policy identity together, and independently exercise ACK closing the first retry plus exact old business tuple replay.


Owner merge milestone validated: Store34415 322 top/887 named, normal95221 17/44, race51286 60/195 plus targeted migration race35648 (see receipt for exact counts), all macOS reports pass with no skips. Final build/vet/diff checks and Linux cross-compile in35648 exit0. Independent merge+stream-fix review PASS. Two Docker Linux real-Handle tests failed immediately at the existing darwin-only process.Start guard (unsupported_platform); this is a remaining F07 implementation boundary, not Linux acceptance or a successful test. CID dea85a985e11861489ad71f6fa288c2c0ea8603a106f0f225232eae25fcf59c3 exited1, notOOM, removed and inspect confirms absence. No product guard or assertion was changed to hide that result. All original sessions terminal. Full logs/hashes/scopes: data/root-owner-merge-receipt.json.


## Budget merge and diagnostic retry gap (2026-10-07)

Budget 9adb91a is staged into integration 2e607ad with MERGE_HEAD retained; ROOT remains the only writer/test-token holder. Independent review of the actual four auto-merges passed. Baseline build81220 exit0. New actual Install/Prepare/Submit Store fixtures bind config/index/vector/policy, preserve the original pending hold/grant/payload/due through budget revision and real schema17-to18 reopen: Store73422 exit0, 1 top / 3 named, no skips. Earlier fixture compile/SQL mistakes are preserved but are not product RED.

Scope correction: existing indexed and review-retry public CLI fixtures retain owner-bind's local owner mode. Their prior successful tests do not prove supervised original-owner/indexed integration. The new indexed native variant explicitly asserts supervised-v1, original binding and live worker ancestry via the existing controlled test identity entry. It does not claim installed config-generation support (that public path remains local-owner only).

Native27997 exit1 is an actual product RED: original readonly reviewer Exit7/unique Exited/reaped, same Host ready, old cap denial and public revision succeeded, then original failure slot retry was rejected as candidate_review_retry_unsupported. The Host stored its fixed 88-byte candidate_review_invalid diagnostic; both QueueRetry and pending decision proof reject any artifact. Runtime /private/tmp/indexed-cli-4114306803 retained. Original Coordinator cleanup Wait0; all six registered actor identities observed ESRCH. No running original test session or container remains.

Minimal fix in progress: permit only explicit readonly Claude/Grok review retry with exact fixed Host diagnostic; require exact completed failed/Exited process tuple, no conflicting evidence, bounded canonical owner-only no-symlink artifact path/content/hash. Keep input/vector/config/adapter fixed, bind physical Host identity in the existing decision field, and revalidate evidence before Claim. Automatic prelaunch rules and writable stages unchanged; no schema change. New Store regression58021 exit1 (2 positive provider cases plus 2 pending-proof cases reach the existing rejection); malformed-evidence rejection cases pass. Native must be rerun through complete indexed accept/integrate, and stable code independently reviewed before merge commit.


## F01/F02/F13 真实失败重试与阻断下游（2026-10-07，待最终集成）

ROOT继续单写integration，HEAD2e607ad/MERGE_HEAD9adb91a尚未提交。新readonly诊断retry只允许canonical spool的88字节固定candidate_review_invalid语义、唯一原Process Failed/Exited正数退出，openat逐层NOFOLLOW/owner0700、file0600/nlink1、hash/严格JSON及同inode复核；在既有decision里绑定原Host并于Claim重验。自动prelaunch未扩，旧nil-artifact重试保留。独审PASS。

native8245 exit1真实暴露第二缺口：FailedExit7→增额→原slot retry→新Exit0/ACK重放已成功，但三个review全部completed后integrate仍blocked_dependency；现场/private/tmp/indexed-cli-1454180180。ROOT只读确认8个登记actor均ESRCH，原Coordinator清理Wait0，现场保留。依赖恢复限定同run/root可达、从未启动且无历史binding下游，其他永久失败祖先保持阻断。恢复queued仍等业务accept，原receipt replay不重复恢复。

独审进一步发现blocked后代配置pin可能已释放。实际Install/Prepare/Submit测试73880有效RED；新增递归祖先delivery/pending-retry hold，枚举与ReleaseConfigTaskUse事务重检共用。带配置的恢复采用两阶段只读requirement→install锁→Store事务重新校验精确闭包，并复用原grant/run/rebind证明；原subset pin、prepared摘要一致性、inode/内容在提交前复核，缺失旧pin拒绝且不重建，不新增schema。changed_closure25881实际RED后修复second core空闭包不得绕过proof比较。最后生产guard/tests/docs增量独审PASS；owner-only信任边界下不宣称检测所有pin离线一致改写。

最终native96151 exit0（root-budget-indexed-native-v4.jsonl）1top1named，完整supervised indexed DAG最终integrate及重开验收通过，原CoordinatorWait0，成功runtime清理。Store66072完整347top979named PASS（最后empty-closure两guard修复前）；最后受影响normal3527 exit0 70top201named全PASS，0skip。最终race36113仍运行；Linux crosscompile89071 exit0，container 9bcaca2a35f21acd6f34d64eea8772c51ec9bd46d7685a604d67a743719ee6f2 start70620运行中，未提前称通过/清理。后续检查原session终态并记录准确范围后再合并commit。原研发16tracked修改保留；整个F01-F13继续ACTIVE。


最终预算组合验收：final normal3527 70top201named；race36113 Store70/201+Install34/98，native组合race83827 Process2/2+Host2/6+Coordinator5/5+CLI8/27，最终race121top339named无fail/skip。build29208/vet56327 exit0，Go1.26.8 darwin/arm64；diffcheck通过。Linux89071交叉构建成功，纯Store/实际合成安装配置pin3top19named PASS，container9bcaca2a…ee6f2 exit0非OOM，已精确rm且inspect absent。独立审查最终PASS后没有生产代码变化。所有原session终态、成功native现场由通过用例清理，必要RED现场保留且登记actor均ESRCH。完整scope/hash/原始失败分类见data/root-budget-merge-receipt.json。仅本地集成，完整F01–F13目标保持开放。
