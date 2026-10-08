# Offline pending-delivery resource runner

This task uses the same release candidate as the 60-minute resource soak. It
starts a private candidate and an owned origin controller, submits through the
public native owner path with a controlled fake worker, and retains the exact
durable result pending. It requires matching running/exited process tuples and
Exited0, observed worker reaping, one bound Owner/Host, and a normal original
controller Wait. The runner enrolls live Owner/Host/worker identities before releasing the worker.
After worker completion and controlled origin exit, the exact Owner/Host tuples
must be absent within 13 seconds before measurement; they may naturally retire
before origin exit. This does not claim origin loss caused their retirement.

The entire measured interval keeps the same task/attempt/segment/result event,
requires only the Coordinator to remain, samples RSS and cumulative CPU, and
watches the Coordinator with Darwin kqueue NOTE_FORK/NOTE_EXIT. A final sample
and event drain occur at or after the deadline. Frozen limits are 64 MiB RSS and
1% of one CPU core. The sampling Python/ps processes are harness overhead;
product children are forbidden during this offline interval.

The manifest pins candidate, runner, controller, reused resource-soak module,
its thresholds, and this task's exact threshold object. Executed dependencies
are copied from one no-follow source FD to private exclusive files. End checks
compare private file pins and source hashes. CLI requests/evidence use 0600;
runtime directories are private. Original controller and Coordinator handles
are waited on with bounds; cleanup errors, forced cleanup, residual actors,
pin drift, and nonzero Wait make the final summary failed. Failed state and raw
streams are preserved. This does not call collect/accept to simulate offline.

Only syntax and two harness guard checks are complete at the review snapshot.
No native offline run has been performed yet. The accepted 60-minute candidate
must finish before this runner starts, to avoid competing measurement loads.
This is not the separate 30-second idle or four-source-host acceptance, not real
Provider/model qualification, and not new original-Wait evidence for Owner/Host
(the runner observes exact identity disappearance for those actors).

First native attempt failed before measurement: the `/bin/echo` worker completed
and the original Owner had naturally disappeared before enrollment. Both
controller/Coordinator original Wait0 and no residual actors were preserved.
The controlled shell worker now waits on one private release file so the runner
can record live exact Owner/Host/worker identities before releasing it. It must
then produce the same durable pending result/Exited0; this does not extend the
measured window or alter any product lifecycle/budget rule.
