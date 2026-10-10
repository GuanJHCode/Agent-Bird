# Kernel peer-anchor design

## Scope

Add a Go-only, in-memory `(*process.Handle).VerifyLiveDescendant(ctx,
peerPID)` authority check.  The authority belongs only to the `Handle` returned
by the original successful `Start`/`StartDuplex`; it is neither exported in
`Identity` nor representable in JSON.  This change deliberately does not touch
the filter carrier codec, outer integration, or existing Start/Wait/tree
semantics.

## Lifecycle and locking

`startCommandUsing` creates the `Handle`, calls `cmd.Start`, records the
original process group and public `Identity`, then starts the one original
`cmd.Wait` goroutine.  `Handle.mu` protects `waited`, identity and output; the
Wait goroutine sets `waited` before closing `done`.  The new verifier will take
short lock snapshots only: it must see a non-terminal handle before its first
kernel read and again after its final reads.  It will never hold `mu` during a
kernel read or wait.  A terminal (or concurrently terminal) handle is rejected.

Immediately after `cmd.Start` has supplied `cmd.Process` and the fixed PGID is
known, Start will capture a private platform leader anchor.  Capture is one
kernel-only observation of PID, full birth tuple, effective UID and PGID; it
does not use `Identity.Birth`, `ps`, locale formatting, or a later lazy PID
lookup.  Capture failure leaves the existing returned Handle and Start result
unchanged, but records no usable anchor, so the new verifier always rejects.
The captured PID is the `exec.Cmd` original process PID.  The anchor is taken
only at that original Start boundary, never reconstructed from a serialized
identity or a caller-supplied PID.

## Platform observations

Introduce a private `peerRecord` and platform helpers:

* Darwin builds obtain `PID`, `Ppid`, `Pgid`, effective UID, and `(start sec,
  start usec)` from `darwinProcessRecord`/`KinfoProc`; they validate all raw
  fields.  The existing kernel sysctl path is reused, with no `ps` fallback.
* Linux builds reuse `linuxObserveAncestor`'s stat/status/stat double-read
  stability check and retain the complete start tick plus PGID, parent and
  effective UID.  A private helper combines the already-stable record with the
  boot identity where needed without publishing it.  Non-Darwin/Linux builds
  return an unsupported authority error.

Each record read is bounded by the caller context.  Missing proc records,
invalid raw fields, an unstable double read, or cancellation is an error.

## Verification algorithm

`VerifyLiveDescendant` rejects nil/invalid peer PIDs, missing anchor, cancelled
contexts, terminal handles, or a leader/anchor mismatch.  It snapshots the
private anchor and handle terminal generation under `mu`, then reads the
current leader and peer from the kernel:

1. Confirm the leader exactly matches the captured PID, full birth tuple,
   effective UID and original PGID.
2. Walk `peerPID -> ppid` for at most 64 unique nodes.  For every node require
   the captured UID and original PGID, and use platform double-read stability
   before accepting its parent relation.
3. Succeed only when one observed node exactly matches the complete leader
   tuple.  Hitting init, a cycle, a different UID/PGID, a missing/reused PID,
   or 64 nodes is an error.
4. Re-read both leader and peer using the same raw observations, repeat the
   leader-anchor equality check, and under `mu` reject if Wait has become
   terminal or the anchor changed/vanished.

No `AncestryContext`, provider event, public `Identity`, serialized birth
string, `ps`, or locale-dependent formatter participates in this decision.

## Tests and evidence after approval

Add focused process-package regressions for: valid live direct descendant;
foreign same-UID process rejection; leader terminal rejection; missing-anchor
rejection after a Start whose private anchor capture is injected to fail; and
context cancellation.  The native Darwin helper will use the existing original
Start/Wait/Stop/tree cleanup pattern, with all fixture exits bounded and no
claim based on `ps` disappearance.  Linux receives a cross-build check only
on this Mac host.  Run only the new focused Go tests plus `GOOS=linux go test`
compile coverage, using the prescribed Go 1.26.8 environment.  Raw runner
evidence will be retained under this task after a clean pre-command commit.

## Risks and decisions requiring review

* `exec.Cmd.Start` returns after fork/setup but cannot prove that a dynamic
  wrapper has completed a later `exec`.  The anchor therefore captures the
  original kernel process identity at the authorized Start boundary; a later
  UID/PGID drift fails closed.  It must not be lazily recaptured to make such a
  wrapper pass.
* Darwin full-birth precision comes from raw seconds and microseconds; the
  public second-resolution `Identity.Birth` remains insufficient and unused.
* A peer reparented before or during the bounded walk fails because the chain
  no longer reaches the exact leader.  Races and partial observations fail
  closed rather than retrying through a different PID.
