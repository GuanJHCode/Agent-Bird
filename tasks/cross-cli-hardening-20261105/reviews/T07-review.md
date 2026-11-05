# T07 independent review

Verdict: **PASS** for the frozen managed-process projection and protected owner action guard code scope. No blocking findings. This is not a native-session/live-parent acceptance claim.

Reviewer: /root/review_t07 (independent of implementer). Read-only source review; no source changes, commit, push, delegation, model calls or credentials access. Base: 2347d34ce1e5ca4e40751f53064f3d99001f4832. Reviewed implementation.diff, implementation-report.md, source-manifest.json, validation-summary.json, handoff T07/OWN-01..04 and approved T07-owner-guard-contract.md.

## Snapshot and scope

All 26 manifest files matched before and after independent validation; git diff --check passed. implementation.diff SHA256: 41a838ea5b21d65e59cef4a88a0876c4990b1bb7b90238f9c10e4d748a02c49e. Store helper SHA256: d3704ec2d0d1ace2b2575af7ab53238b9868240ecd593afb27733d12f9649150.

## Reviewed contracts and source evidence

- Projection negotiation: internal/coordinator/local_owner.go:81,95,143 and cmd/orchestrator/local_owner.go:40. Version 0 omits both extension fields; only version 1 enables managed_process; unknown versions reject. Ready at internal/coordinator/server.go:479 only advertises managed_owner_projection_v1. No CODEX_THREAD_ID/MCP label is used as native conversation proof. Native push, typed resume, automatic rebind and chat-switch observation remain false. Legacy strict consumer test and no-capability CLI negotiation test pass.
- Local submission: internal/coordinator/server.go:489 checks capability and complete authority tuple before defaulting empty delivery to collect at :500; any explicit non-collect rejects at :503. Existing separately authenticated native route is preserved. Capability schema/files are unchanged by informational projection.
- Protected public resume/retry/request_changes/rework/accept/answer: internal/coordinator/server.go:613,627,642,657,672,689 always pass the guard-produced context into their Store mutation. internal/coordinator/owner_action_guard.go:11 first validates control via store.TaskOwnerActionBinding; :16 checks current kernel birth; :19 requires local owner ancestry. Native live-owner bearer delegation remains as approved. Existing worker and unidentified-segment guards still run in server.go:453.
- Local classification is solely owner_grants.id=run.origin_context_id, independent of delivery fields, at internal/store/owner_action_guard.go:29. Rebind preserves original context; snapshot binds task/run/controller/context/PID/birth/HostGeneration/local flag. Tx helper :52 compares the current binding then rechecks kernel birth.
- All six hooks run within transaction and before receipt replay/business mutation: internal/store/decisions.go:22,78,155,273; rework.go:76; owner_rework.go:281 (legacy QueueRequestChanges included). These methods and RebindOwner use the same DB mutex; snapshot comparison/mutation cannot interleave with in-process rebind. Regression covers six stale-snapshot paths, same PID/birth with changed generation, retry receipt replay after rebind and explicit rebind without budget reset.
- Query/stop/ACK retain original bearer cleanup authority; no unsupported extra ancestry/liveness policy was imposed. Recover-host keeps its dedicated local capability plus original control inspection and existing HostHello identity validation (internal/coordinator/host_recovery.go:20,87); the new action guard is not used there.

## Independent completed validation

Go 1.26.10, GOENV=off, GOTOOLCHAIN=local, GOPROXY=off; isolated review/go-cache and explicit pinned Codex PATH. JSON terminal Test actions were counted only after each subprocess ended.

- independent-race.jsonl + validation-summary.json: race, uncached, coordinator/store/CLI selected owner/projection/control/rebind/worker/late-event/replay paths; **63 pass / 0 fail / 0 skip**, all three packages pass, exit 0.
- independent-lifecycle.jsonl + lifecycle-summary.json: race, uncached CLI currentOwner untrusted managed-context rejection, controller termination, local collect without native bridge; **10 pass / 0 fail / 0 skip**, exit 0.
- snapshot-check.json: 26-file SHA match, helper SHA match, diff whitespace check exit 0; all review tool sessions ended.

Implementation evidence additionally records final pre-last-hook full module 1291 pass / 8 skip, followed after final legacy hook by store/coordinator race 116 pass, CLI race 7 pass, build/vet exit 0. These are implementation evidence, distinct from the independent 73 passing test/subtest terminal actions above. Full module was not independently rerun. Native/model opt-ins were not run and remain explicitly unverified.

## Remaining limits and integration

No verified_native_session, live-parent clear/resume, multi-chat isolation, native original-thread delivery or typed native resume claim is approved. managed_process collect/rebind is the reviewed fallback. OWN-02 native claim branch remains unverified; no native capability is enabled by this change. PID reuse is covered by same PID/wrong birth fixtures rather than forcing real OS PID recycling. Root must retain the QueueRequestChanges tx hook while integrating T04 and validate the combined snapshot; this report signs only frozen T07.

Temporary independent Go build cache was removed after all sessions completed. JSON validation evidence and this report are retained outside source worktree.

## 主会话集成签收

主会话逐文件核对集成树26文件与冻结manifest相同，复核公开guard与Store同tx/replay前snapshot核验；集成race三包55pass/0fail/0skip，进程完整结束，见data/public-evidence/T07-integrated.json。scope限制保留，T04后仍须保留legacy hook及复验组合树。允许本功能独立提交并push任务分支。
