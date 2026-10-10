# Filter target and candidate live pair

Source `d6a7cd1` adds a Host-private `FilterTargetCandidatePair` that binds an
existing target observer and candidate materialization only when they retain
the same original source, stream broker binding, and accepted candidate. The
pair captures the candidate child's detached HEAD pin and an open, hashed,
header-checked index descriptor before either full verification can run a Git
command or journal callback.

Binding first runs the established target and materialization verification to
obtain their original Git lifecycle proof. Its final verification then uses no
Git or journal operation: it rechecks target anchors, metadata, inventory,
refs and source pins; candidate workspace anchors and inventory; child source
and backlink pins; and the pair's original child HEAD/index descriptor. It
rechecks the named child git directory after the named HEAD/index reads, so a
callback cannot rename the directory after the first directory check and leave
the held descriptors pointing at the old directory. Pair Close only closes
those added descriptors.

Focused evidence is in `data/`. `focused-pure-first` and
`focused-pure-second` retain two failed compile attempts (a same-type field
declaration, then an unused import). `focused-pure-final` predates the later
named-index repair and lacks command/source metadata, so it is not current
qualification evidence. `focused-pure-repair.command.json` pins source
`43ed874` and orchestrator tree `9869ad783713a9353a2fdb73f359294a0704d135`;
its stdout/stderr/exit record the updated focused pure guards, including named
index replacement. The later directory replacement guard is covered by the
source test introduced in `0bd7a64`; ROOT independently ran its focused pure
set after integration. This worktree has not run native fixtures.

`TestFilteredTargetCandidatePairNativeCallbackOracle` is an unexecuted Darwin
oracle. It reuses the accepted producer fixture to bind the same live source,
broker, target observation, and candidate materialization; records a callback
that changes an already checked target file during candidate verification; and
checks that old sequential verification can return while the pair tail and
`Pair.Verify` reject the drift. After restoring the fixture file and its
captured timestamp it uses that same pair to reject a named child-index
replacement. ROOT must run this single top-level oracle from its fixed review
snapshot; it is not native qualification evidence in this task artifact.
