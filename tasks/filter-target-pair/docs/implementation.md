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
and backlink pins; and the pair's original child HEAD/index descriptor. Pair
Close only closes that added index descriptor.

Focused evidence is in `data/`. `focused-pure-first` and
`focused-pure-second` retain two failed compile attempts (a same-type field
declaration, then an unused import). `focused-pure-final` predates the later
named-index repair and lacks command/source metadata, so it is not current
qualification evidence. `focused-pure-repair.command.json` pins source
`43ed874` and orchestrator tree `9869ad783713a9353a2fdb73f359294a0704d135`;
its stdout/stderr/exit record the updated focused pure guards, including named
index replacement. Native target fixtures were intentionally not run here; the
callback race oracle remains for ROOT's native validation.
