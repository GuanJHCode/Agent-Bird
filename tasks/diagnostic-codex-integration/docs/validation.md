# Managed Codex detailed capture

Managed Codex now opens the explicit-consent diagnostic driver before original
Start and captures stdout through the existing RPC reader and stderr through the
existing process observer. The validated grant is copied from the private launch
snapshot; mutation of the external grant cannot change capture authority.
Default summary launches retain their previous behavior and create no raw log.

Detailed sessions half-close stdin after a verified terminal and finish only
after the original Events channel closes and all buffered events have passed
through the same reducer. Late permission, foreign and malformed frames still
fail. Actual reader EOF is distinct from forced Close and from business success.
Host requires original Wait/tree proof and actual stdout EOF before finishing
capture; unknown or undrained paths abort and retain incomplete diagnostics.
Diagnostic faults cancel the original lease; no additional reader, signal owner,
Wait owner, business action or retry authority is introduced.

## Final verification

Go1.26.8, macOS arm64, TMPDIR=/private/tmp, uncached tests. Counts are top-level /
all named passing tests. Exact commands, hashes, sessions, skips and container
receipts are in `../data/root-final-receipt.json`.

| Scope | Passed | Failed | Skipped |
|---|---:|---:|---:|
| Full Host | 400 / 1547 | 0 | 16 |
| Full process | 69 / 137 | 0 | 0 |
| Final full RPC, race | 51 / 180 | 0 | 0 |
| Full session, race | 56 / 489 | 0 | 0 |
| Affected process duplex/observer, race | 10 / 10 | 0 | 0 |
| Explicit managed native actor suites, ordinary | 2 / 16 | 0 | 0 |
| Explicit managed native actor suites, race | 2 / 16 | 0 | 0 |
| Docker Linux arm64 full RPC | 51 / 180 | 0 | 0 |
| Docker Linux arm64 full session | 52 / 485 | 0 | 0 |
| Docker Linux arm64 pipe half-close | 2 / 2 | 0 | 0 |

The 16 default Host skips are explicit opt-in checks or helper entry points;
the exact list is retained. The two relevant managed actor suites were then
verified independently under explicit opt-in, including default/detailed
question-question-result continuation, immutable authorization, protocol and
permission failure, segment/all stop, UNKNOWN ownership and real manifest-stage
write failure. All36 original-process cleanup records across normal and race
runs prove original Wait, group absence and joined Execute. Retained stdout
after terminal is covered by pure session pipes, not a new native descendant
fixture. Four session tests have a Darwin-only build constraint; their absence
explains the Linux count difference and is not recorded as a runtime skip.

Native execution used the existing controlled Go helper under the managed
sandbox. The retained supported0.154.0 binary was only canonicalized and hashed
for the unchanged pin; it was not executed and no model/account was called.
Linux tests establish RPC/session/pipe behavior, not Provider/sandbox/install
acceptance or Linux race. Build and whole-module vet passed; RPC vet passed again
after the final test-only addition. Production and final fixtures passed
independent static review; the reviewer did not execute these tests.

## Failures retained and corrections

- Missing option/API logs are compilation RED only. The actual session RED with
  a no-op option showed premature stdout close and rejection of valid early EOF.
- The first native RED had no detailed namespace despite original child cleanup.
  Wiring the driver resolves it; it was not an unavailable-environment failure.
- An18-byte actual final pipe read against a16-byte frame limit exposed lost
  ReadEOF. The latch now occurs before oversize classification.
- Initial long fixture paths caused history-seal failure. The first detailed
  seal was15992bytes against the existing16KiB cap; the next question failed.
  Default capture also failed with the longer fixture root. The rejected next
  seal's exact size was not persisted. Moving only the controlled test root to
  a short owned /private/tmp path made both suites pass; the product cap remains.
- Independent review caught use of the mutable external grant and colliding
  suite directories. The immutable_grant RED failed before Start; frozen snapshot
  use and separate retained/diagnostic namespaces now pass.
- The first concurrency fixture did not control EOF classification and shared
  error-publication windows. New synctest tests explicitly model the reachable
  internal state, plus an original full-close barrier. Isolated Go overlays
  reverting each fix fail the corresponding assertions; final race passes.
  This proves state/publication ordering, not a real reader instruction schedule.

## Evidence and remaining work

All task test sessions and four owned Linux containers are terminal; each exact
CID was removed and absence checked. Original native runtime data was archived
without following symlinks, every regular member and link verified, and the
owned short runtime root removed. Four owned overlay temporary files were also
removed; failing variants, logs, successful receipts and tested binaries remain.
Archives preserve evidence, not a promise of replay from relocated paths.

Public diagnostics read/ACK authentication/replay, reservation activation and
retained-byte verification, all-writer intake/delete and full2GiB enforcement,
native flood/RSS/physical-fault acceptance remain open. Intel Mac and Windows
native acceptance awaits environments. This local milestone does not complete
F02 or the F01–F13 goal; no installed replacement, push or release occurred.
