# F02 bounded Linux filesystem exhaustion

Actual Linux checks passed on the existing production code; no production behavior change was needed. Fixed-snapshot independent review of `28b108b` by `new_base_store_review` passed after the runner corrections below. ROOT separately verified both actual container runs and cleanup; the review did not run tests.

## Writer publication

Source `e3948a1`, Linux arm64: **1 top-level /4 named PASS**, zero failures/skips, 0.02s. Three phases fill a dedicated 2 MiB tmpfs after truncating the publication stage: pending manifest, chunk, final manifest. The existing checkpoint returns nil, and production WriteAt returns kernel ENOSPC. This is not an injected error value.

The failed writer remains failed. Reads retain `old` for the first two phases and the already durable `oldnext` for the final phase; none reports complete/Closed. Writer reopen also fails ENOSPC while full. After removing only the test-owned filler, reopen/close succeeds with the same bytes and sticky Incomplete. The complete raw output is `data/linux/publication-first.log`.

## Host ownership

Source `28b108b`, Linux arm64: **1 top-level/named PASS**, zero failures/skips, 0.05s. Host state, spool and diagnostics share the limited tmpfs, filled only after durable Running. The controlled original child receives termination, its stdout tail is drained, original Wait returns 0 and its execution tree is confirmed exited. ExecuteLaunch returns UNKNOWN/ENOSPC because it cannot persist terminal state; the complete previously committed event sequence remains unchanged and ends at Running. A fresh Host object reads exactly that sequence after the owned filler is removed. Raw output is `data/linux/host-first.log`.

No terminal event is invented from in-memory exit evidence. This check reconstructs a Host object in the same test process; it does not kill/restart a Host or Coordinator process, perform Store reconciliation or establish a new retry permission.

## Isolation and receipt

Both runs used the already cached Linux image, network none, nonroot UID65534, read-only rootfs, all capabilities dropped and an explicitly bounded tmpfs. The tests refuse a non-tmpfs or capacity above 4 MiB, and remove only their own temporary paths. No workstation filesystem was filled. Exact CID, actual exit0, non-OOM and removal receipts are retained; ROOT independently reparsed raw outcomes, inspected each exact CID absent again, verified binary SHA256 values and removed only those two owned test binaries.

The first runner had two independently identified issues: exception cleanup and accepting empty/skipped runs. Its successful writer run remains valid through ROOT's raw and cleanup verification. `28b108b` hardens cleanup with exact-CID finally and requires exact expected pass names with zero failures/skips; the new Host run used this runner.

Production diagnostics/Host/process code is unchanged, so prior applicable Mac/race evidence is reused and not represented as a new run. This establishes Linux tmpfs ENOSPC behavior only. Physical disk faults, power loss, quota/GC completion, sustained flood/RSS and F02 overall completion remain open. No real Provider/model/account, user installation replacement, push or release occurred.
