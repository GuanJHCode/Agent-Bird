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

Status: owner merge is ready for local commit after macOS validation and independent review; budget merge has not started. Full F01-F13 remains open; this milestone does not replace native publisher/durable-write, B3 sealing, full logging, platform/provider or final package requirements.


Owner merge validation in progress: build42688 exit0; full Store34415 exit0 (322 top / 887 named, no fail/skip). Independent merge review found the indexed streaming path dropped a nonnil Handle on post-Start error. Real-handle RED40131 exit1 reproduced both standalone cleanup and lifecycle transfer failures; private stream start helper now records original identity as unknown and uses CleanupFailedStart. Original buffered assertions were preserved and shared with two added stream tests. Independent re-review PASS. Normal95221 exit0: Gitops8/18, Host6/23, CLI3/3 (17 top /44 named). Race51286 remains running; no completion claim yet.

Budget pre-merge independent inspection: preserve integration indexed/readonly candidate policy admission together with budget latest absolute caps; preserve indexed accept plus retry exact business replay before ACK-close guards and effective-cap checks; retain all fixture routes and budget-revise authorization. Planned new combined Store regression binds indexed/config/vector/policy identity and original pending budget promise across amendment, retry and schema17→18. Actual budget merge has not started.

Budget combined regression fixture draft (read-only investigation; not implemented): integration review_retry_test.go newReviewRetryPolicyFixture(t, adapter.ProviderGrok, true) supplies real policy and dependency-vector grant, but has neither config generation nor indexed assignment. Its WithOptions helper accepts a config ref; bindIndexedConfigPlan supplies config-history setup. IndexedReview assignment must be added to the payload before SubmitPlan, not retroactively after binding. Reuse budget_revision_promise_test.go identities/hold assertions: old action_slot/policy hash/payload/due/reserved_active_ms must remain exact after amendment/reopen; new claim consumes original hold, while later new decisions use effective caps. Cover config/index/vector/policy identity together, and independently exercise ACK closing the first retry plus exact old business tuple replay.


Owner merge milestone validated: Store34415 322 top/887 named, normal95221 17/44, race51286 60/195 plus targeted migration race35648 (see receipt for exact counts), all macOS reports pass with no skips. Final build/vet/diff checks and Linux cross-compile in35648 exit0. Independent merge+stream-fix review PASS. Two Docker Linux real-Handle tests failed immediately at the existing darwin-only process.Start guard (unsupported_platform); this is a remaining F07 implementation boundary, not Linux acceptance or a successful test. CID dea85a985e11861489ad71f6fa288c2c0ea8603a106f0f225232eae25fcf59c3 exited1, notOOM, removed and inspect confirms absence. No product guard or assertion was changed to hide that result. All original sessions terminal. Full logs/hashes/scopes: data/root-owner-merge-receipt.json.
