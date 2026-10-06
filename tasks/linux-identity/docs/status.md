# Linux identity status

Scope is F07 identity observation only, not Linux Provider support.

## Evidence

`red-original-api.log`, `red-executable.log`, and `red-peerpid.log` record
the original Linux failures for Birth, ExecutablePath, and PeerPID. The
corresponding `green-process.log` and `green-peerpid.log` record the focused
container checks after implementation. Each log contains its Docker CID and
the post-exit inspection result.

The container used the pinned Redis image digest, network isolation, a
read-only root filesystem, user 65534:65534, no capabilities, no new
privileges, 64 PID limit, 256 MiB memory, one CPU, and a 128 MiB `/tmp`.

## Current implementation

Linux Birth derives `linux:<boot_id>:<starttime ticks>` from procfs; Linux
ExecutablePath validates the procfs executable link and birth before/after;
Linux PeerPID uses `SO_PEERCRED` and rejects a mismatched effective UID.
Linux ancestry observes `stat`, effective UID from `status`, and `stat` again
for each bounded node. Linux group observations scan procfs and fail closed
when a numeric proc entry cannot be parsed or read; a disappeared entry is
only accepted when it is absent from the directory snapshot.
Executable identity compares the kernel object at `/proc/PID/exe` with the
canonical target using `SameFile`. Ancestry derives its birth from the already
double-checked stat snapshot and boot ID; it does not issue a third stat read.

`process-milestone-retry.log` covers parser, self, real parent/child/gone,
and UDS tests. `group-milestone.log` covers a waitable group leader while
active and after its original Wait. Both logs include post-removal CID checks.
The post-review regressions for inode mismatch, stat reuse, and cancellation
passed the final native run. Its exact inputs, binary hashes, named-test count,
and CID cleanup receipt are recorded in `final-native-manifest.md`.

## Remaining work

The Coordinator real-UDS owner-binding, spoof rejection, and Store-registered
worker-control rejection checks now pass; see `coordinator-native.md`. Zombie-only group quiescence is not claimed as a native
actor result. No claim is made for Linux Provider execution, native Codex
behavior, or plugin installation.
