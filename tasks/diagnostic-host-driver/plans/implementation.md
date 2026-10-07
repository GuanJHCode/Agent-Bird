# M2c diagnostic Host driver foundation

Base bb70c55. ROOT alone writes integration and owns native Mac/Docker tests.
The separate diagnostics-abort worktree owns Writer.Abort. This batch freezes
and verifies the private driver; ordinary/managed-Codex execution hooks and
summary metadata failure projection remain subsequent required work.

- Only a verified detailed launch with HostID equal to the Host producer can
  open capture. Scope uses the exact run/task/attempt/producer/policy hash.
  Segment identity is `%020d:<segment ID>`; validate before any filesystem work.
- Fixed root is `spoolRoot/<attempt>/.diagnostics/<scope hash>`. Both directory
  levels of the existing event scanner skip dot names. Existing FD-chain
  primitives bootstrap exact0700 children; no symlink traversal or chmod repair.
- Observer copies into the fixed eight-slot Capture only; it never performs
  filesystem, RPC, Stop, or Wait. One consumer owns Writer operations, with
  periodic partial Flush and two-second operation contexts for lock waits.
- Execution cancellation does not silently cut off capture. The later Host hook
  calls Finish only after its original Wait/tree/drain proof. Finish disables
  observation, closes Capture, drains final loss, and closes Writer.
- Any Append/Done/Flush/Close/Offer error is latched, disables observation,
  wakes the execution through a nonblocking failure callback, and uses Abort.
  No lost lease can be followed by a successful normal Close. An unproved early
  return uses Abort, not Finish. Caller deadline stops waiting without creating
  a false join; cleanup remains observable through the original driver handle.
- Context bounds are not a claim that a blocked physical write/fsync can be
  interrupted. Native Host stop bounds and storage failure projection still
  require separate integration tests. No public read/ACK or M3 completion claim.

Verify default privacy, exact stdout/stderr and scope, event scanner exclusion,
periodic flush, finite queue/loss during a blocked writer, final-loss drain,
preaccept failure and no later Close, timeout/Abort ownership, resumed ordinals,
private path failures, and original source bytes remaining borrowed only during
the observer call. This batch does not invoke accounts or installed copies.
