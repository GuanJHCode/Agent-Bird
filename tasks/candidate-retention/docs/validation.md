# Candidate retention validation

Production source reviewed at `7f15c9d`; schema41. No user runtime was collected. All native fixtures were task-owned temporary repositories/directories. Go1.26.8 darwin/arm64, TMPDIR=/private/tmp.

## Accepted evidence

- `root-core-race-2`: 10 passing top-level tests, 16 passing subtests, no skips; one additional downstream test failed during fixture setup (`source_event_revision=0`). The passing named tests remain valid, but the command exit1 and Store package failure are retained and never called a full pass.
- `root-downstream-race-3`: the fixture reads Store's normalized revision; affected downstream test alone passes with race, 1 top, 0 sub, exit0/stderr0. Together these establish 11 distinct top-level/16 subtest race checks for Store retention/grants/GC, source authority and Host recovery.
- `root-core-focused-1`: 10 top/12 sub, 0 failures/skips, exit0/stderr0. Four seal tests not repeated by the race selection retain valid evidence: compose projection, unfinished/oversize, sealed replacement and source-directory replacement. Later changes did not alter these behaviors.
- `root-migration-public-1`: schema40→41 preserves original business/artifact metadata and does not adopt legacy resources; 1 migration top PASS. Its CLI test was superseded by the final public run.
- `root-native-public-final-1`: 2 top/0 sub, 0 failures/skips, exit0/stderr0. Public CLI exercises real plan/save/empty apply, resolve eligibility rejection, GC three actions and foreign owner. Native actor (3.26s) uses a real repo/freeze/origin/seal and pinned built child. It first denies MarkRunning and verifies no cleanup or journal write, then permits execution and verifies worktree/admin/private-ref removal plus retained source/operation journals.
- Native actor deliberately uses fake Store callbacks and a fixture state.db inode. It proves parent/actual-child/Git/filesystem execution and grant ordering, not a single real-Store end-to-end run. Real Store transactions, migration, ownership, generation, race, consumer fences and real IPC are covered separately.

Across the applicable evidence above: 18 distinct top-level tests and 18 subtests. No full module test suite was used for final acceptance. Intermediate worker broad/full test attempts are retained as historical failure/interruption evidence; the AGY source_host_start_failed observation remains untriaged and is not claimed to be a pre-existing failure.

## Independent review

- Initial seal and Host reviews blocked pathname-derived authority and missing Exited→registration recovery. Root23ab9a8 plus Hostab0eabe closed these, with origin recorded only by exclusive initial source creation.
- Core23ab9a8 review blocked incomplete downstream consumption and Store continuation admission. e961f63 adds recursive downstream quiescence/exact consumption/per-run resolution and source-ancestry SQL/live binding fences.
- e961f63 public/actor review blocked apply wire shape, post-Mark bootstrap-error convergence and public error prefixes. ff74986 closes all three and adds GC wake; ab73795 fixes stale default Code after decoding omitted JSON fields, found by actual native execution.
- Reviewer PASS at ab73795, followed by final narrow PASS at7f15c9d for compact public receipts (below64KiB IPC limit), native grant denial and successful deletion. The main agent inspected raw final evidence and integrated changes.

## Boundaries

Ordinary new producer/v2 compose only. Filtered, historical/unsealed, continuation/rework/new-base producer and incomplete/UNKNOWN process histories remain retained. Neither an old journal pathname nor copied origin grants collection authority. A missing original Git Wait stays uncertain; kernel observation is not fabricated as a prior Wait. Individual filesystem syscalls do not have a strict wall-clock cancellation guarantee.

Real external Provider sessions, native TUI/Hook user windows, Intel/Windows, long-run RSS/flood and historical Formal60/outer Seatbelt execution-domain issues are outside this focused acceptance. No real installation/Hook/account was replaced; no push/release occurred. The older855141c package does not cover this batch; a new exact-source package is required after the final evidence commit.

## Final package

Clean source `525d7d30fca936396c317272ee9485903cea8e1c` produced `0.1.0-closure.525d7d30fca9`. Binary SHA256 `01de574498197ecd88e35711a2554f85bb2441b89e220955f2f96055a276b964`. Two deterministic builds and packages matched; all12 verifier commands exit0/stderr0. Independent main-agent inventory checked28 package files and24 source mappings, zero mismatches. Task-owned install/replay/Doctor/uninstall/Doctor preserved the runtime sentinel; no real install changed.

Full receipt and raw command evidence: `tasks/closure-package-provenance/data/candidate-retention-525d7d3/`. Deliverable retained at that task's `tmp/candidate-retention-525d7d3/package-a/` in the candidate-retention integration worktree. The two duplicate standalone binaries and duplicate package-b were removed after exact identity/hash checks; package-a, sentinel, audit metadata and raw evidence remain. Cleanup ledger: `data/package-cleanup.json`.
