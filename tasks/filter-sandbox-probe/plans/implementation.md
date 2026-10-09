# F04 Darwin filter sandbox feasibility

Baseline `9552f0e`; tests and a compiled test actor only. No production gate, Host or public action changes. ROOT is the sole writer of this worktree.

Run one bounded native matrix using task-owned paths and loopback listeners: an unrestricted positive control, then two isolated reversible stdin/stdout transforms. Pin the compiled Mach-O and `/usr/bin/sandbox-exec`, keep the original process.Handle/Wait/tree for each launch, and verify original sentinels and executable hashes after every isolated run.

The isolated actor may read its own executable/system runtime and read/write its private scratch. File metadata lookup is explicitly allowed; content reads, directory listings and writes to candidate/common Git/Host state/external sentinels must be denied, along with hardlinks, loopback/Unix socket connects and child process creation. Actual Stat/ReadDir/ReadFile child probes distinguish these boundaries from nonexistent paths. A successful `clean(smudge(blob)) == blob` here proves only this actor and policy, not public Git filter round-trip, LFS, hooks, signing, receipt propagation, executable publication race protection or resource qualification.

Actor compilation uses a canonical pinned Go executable, exact offline build environment, and private HOME/cache/GOPATH/TMPDIR within the owned fixture. All original build and actor handles are waited and their trees checked before removing that fixture. The outer `go test` command retains its ordinary shared compilation cache; this evidence does not claim the toolchain itself is sandboxed or that no shared tool cache is used.

Store exact source, command, toolchain, raw go test JSON/stderr and exit/counts. Do not run providers/models, alter user installation or use external network. Review the stable snapshot before accepting this limited evidence. Keep failed raw output; clean only the task-owned binaries after original Wait/tree completion.
