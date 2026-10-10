# Raw fixture RED evidence

`raw-fixture-final-red` used source `641bdb4` with a clean worktree. The native actor build had original Wait/tree success. The success and non-round-trip cases reached the live API stub and failed with `raw filter materialization not implemented`. The attribute-drift negative case now requires `CodeIdentityChanged` for candidate-tree attribute bytes that differ from the pinned source manifest. It was not rerun against the stub; final focused race must validate this oracle. Earlier `raw-fixture-red` and `raw-fixture-business-red` runs exposed fixture-root and manifest-digest defects and are retained as non-business failures.


The preserved runs predate the real `gitfilter.ResourceBroker` fixture rewrite and therefore do **not** establish a native Broker-chain RED. They remain only as raw evidence of the former stub/fixture failures. The rewritten fixture is pending the ROOT production merge and its single final focused race.

ROOT audit: the source641bdb4 stream parameter was an unused double, and its attribute branch returned at OpenFilterGit. These stub failures are preserved but do not establish the requested native Broker chain. The later draft98ca21d was incomplete (including a missing per-subcase context) and was taken over by ROOT. Final native proof and the separate unqualified interrupted worker attempt are accurately scoped in `tasks/filter-raw-workspace/docs/final-validation.md`.
