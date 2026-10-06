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

`process-milestone-retry.log` covers parser, self, real parent/child/gone,
and UDS tests. `group-milestone.log` covers a waitable group leader while
active and after its original Wait. Both logs include post-removal CID checks.

## Remaining work

The Coordinator real-UDS authorization/worker-control loop remains open for
the next milestone. Zombie-only group quiescence is not claimed as a native
actor result. No claim is made for Linux Provider execution, native Codex
behavior, or plugin installation.
