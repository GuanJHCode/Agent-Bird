# RSS diagnostic collection contract

Formal60 remains FAILED (old candidate51b8e3a, frozen128MiB peak/8MiB range). This is a diagnostic run using the same product source as a88f45b plus a task-private test entrypoint, with no acceptance verdict. No production source, listener, GC policy or installed tool is changed.

Collect1200 seconds: two fake workers1140s,300s valid flood +300s unknown flood +540s quiet, then60s exact pending tail. Keep1MiB/s/worker,4MiB giant/30s, report/10s with original helper Wait/durable ACK. Coordinator/Host/Owner each record runtime.MemStats every5s and at start/end, in private per-role/PID files. Fail during setup if any enrolled role is missing instrumentation. These metrics bypass Owner stderr, which production discards.

Sample per-actor RSS each second. Do not apply or change formal acceptance thresholds: record RSS numerically and label acceptance_verdict=not_applicable. Preserve the original helper/threshold source unchanged and pin candidate/worker/runner/helper. The extra test code, ReadMemStats and synchronous file writes perturb the observed process; this is not a release-binary qualification.

Read-only observers: vm_stat and sysctl vm.swapusage every10s; vmmap -summary at0/300/600/1100s if the exact owned actor is still live. Each observer gets an original wait4,4s bound and separate raw output. Record access denial/timeouts without escalation. Do not use memory_pressure: local help says it allocates by default and -Q is quiet mode, not a query switch. Do not force GC.

After collection, require both original workers Exited0/reaped, emissions/report proofs, natural Owner/Host retirement, original Coordinator Wait0, exact pins and no unregistered/residual product processes. Failed diagnostics and cleanup evidence remain preserved. Analyze live heap floor, HeapInuse/Idle/Released and RSS/VM pressure together; absent reproduction cannot exonerate hour-only behavior.
