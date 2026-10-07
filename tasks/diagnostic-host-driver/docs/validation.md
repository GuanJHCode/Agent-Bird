# M2c driver foundation validation

The private detailed-log driver now derives its Scope and increasing segment ID
from the grant, creates `attempt/.diagnostics` through pinned private FD chains,
and consumes the fixed Capture queue with one Writer owner. Its observer copies
borrowed stdout/stderr only, without disk/RPC/process work. Periodic partial
flushes preserve small output. Disabled observation and Capture.Close share the
same lock as Offer, so normal Finish cannot race a late accepted callback.

Writer.Abort releases ownership without flushing dirty data or overwriting a
pending manifest. Unclean/recovery state remains visible to an independent
Reader and remains incomplete after reopening. Abort preserves the initial
cause, prior fatal and actual FD-close errors across repeated calls. Cancellation
before acquiring the operation gate leaves live FDs/ownership available to retry.

The execution owner must choose Finish only after original Wait/tree/drain proof.
Every unproved early return chooses Abort. Later terminal calls only join the
first choice, including after a caller wait timeout. Asynchronous storage errors
notify that owner once; the driver never signals processes or invents a Wait.
An error after Close committed a complete manifest is propagated without
retroactively asserting that the raw data was incomplete.

## Evidence

- ROOT reproduced the rejected old caller pattern using a real Capture lease,
  canceled preaccept Append, Done, then ordinary Close: the resulting Reader
  falsely appeared complete for that dropped lease. Session68083 ran the failing
  fixture (test process exit1); the exact source/log/hash are archived. This is
  a driver misuse boundary, not a claim that Writer.Close can infer unaccepted
  external data. The transient test source was removed after archiving.
- The initial new driver tests had missing-API compile RED9894af. It is not
  counted as a behavioral regression. Focused driver36784 and combined53839
  subsequently passed; final validation supersedes those intermediate snapshots.
- ROOT full diagnostics race70247 exit0:58top114named,0fail/skip. Includes original
  native writer/update lock actors with original child Wait. Final Host driver
  subset race35912 exit0:10top23named,0fail/skip. Earlier84583 differs only in the
  concurrent Offer test's proof of an actually accepted first lease.
- Whole-module build46285 and vet38822 exit0. Production did not change after
  those checks; the final test strengthening was compiled by race35912 and Linux
  crosscompile45631. Other crosscompiles53343 and14569 passed.
- Final Docker Linux full diagnostics15098 and driver74814 use cached offline
  image, read-only mounts, unprivileged UID, bounded resources and exact container
  removal/absence verification. Final counts, CIDs, hashes and exit evidence are
  recorded in `data/root-final-receipt.json`. No Linux race claim.
- Independent production and final fixture reviews PASS. The concurrency test
  proves a first lease is in flight; it does not force a later Offer to wait at a
  particular lock instruction. Parent replacement around OpenWriter has no new
  dedicated failpoint fixture; it relies on the existing FD-chain/M1 checks.

The original worker supplied only partial Abort tests and overstated its RED
evidence: its first command created no test, subsequent failures were noncanonical
TMPDIR fixture failures, and a missing-import failure was compile-only. ROOT
added the remaining regressions and its own reproducible behavioral evidence.
The worker also ran full diagnostics normal/race despite its pure-unit-only
assignment. ROOT inspected both archived package terminal PASS records and the
native lock tests' PASS/original Wait paths; no worker session remains active.
Those earlier runs are retained as historical evidence, not final acceptance.

## Remaining work

This driver is not yet called by ordinary Host or managed Codex execution.
Their original lifecycle hooks, metadata-write error projection, genuine duplex
capture and native stop/flood/RSS tests remain required. Public diagnostics
read/ACK, reservation activation/retained verification, M3 intake/deletion and
complete artifact allowance coverage also remain open. The fixtures use local
files and controlled writers; they do not establish physical disk-failure timing,
real Provider/model behavior, Intel Mac or Windows acceptance.

No account/model call, installed-copy replacement, push or release. Preserve raw
validation logs and tested binaries. Only the owned temporary RED test file was
removed; its exact archive and hashes remain.
