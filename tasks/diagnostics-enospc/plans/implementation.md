# F02 real diagnostic ENOSPC acceptance

ROOT is the sole writer in this isolated worktree, based on integration `877a1cd`. F03 owns separate Store/schema/Host changes; this task only touches diagnostics tests and its own runner/evidence. No production change is planned without an observed defect.

Existing diagnostic publication crash tests inject errors at checkpoints. Add one Linux opt-in test with three publication phases: pending manifest, chunk, final manifest. At the existing after-truncate checkpoint, fill an explicitly mounted, bounded tmpfs using real writes. The checkpoint returns nil, so the production WriteAt must itself return ENOSPC. Never fill the workstation disk: require tmpfs and total capacity at most 4 MiB; the offline nonroot container provides a separate 2 MiB mount.

Assert real ENOSPC and unchanged sticky writer failure, no false Closed/complete report, readable committed prefix while full, and failure of writer recovery while full. After removing only the test-owned filler, reopen and close; preserve the exact durable bytes and Incomplete marker. The final-manifest failure may expose the already durable new chunk, as required by the existing publication protocol. No restart/power-loss, Host stop, Store quota or whole-disk exhaustion claim follows from this writer-level check.

Run the one new test in the cached offline Linux container, retaining raw output, actual test counts, source and binary hashes, exact CID exit/OOM/removal facts. Independently review the frozen test/runner and ROOT verify raw results. Reuse existing unchanged Mac/race evidence; do not run unrelated suites. If a defect appears, retain failure, fix minimally, and reverify only affected checks before integration.
