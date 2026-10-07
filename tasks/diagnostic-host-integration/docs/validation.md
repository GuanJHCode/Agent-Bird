# Ordinary Host detailed diagnostics

The ordinary `Host.ExecuteLaunch` path now opens the consent-bound private
diagnostic driver before Start. The original process stdout/stderr observers feed
it; the default path creates no raw namespace. Original Wait, output-copy drain
and process-tree proof precede normal completion. Early or unproved returns
abort the writer and retain incomplete data.

Summary metadata write failures are no longer discarded. An optional version 1
`diagnostics_failure` on the original Exited records summary/detailed failures.
It preserves business outcome, identity, exit and measured active time, and adds
no action, retry permission or schema. Async detailed failure can cancel the
existing execution with `diagnostics_unavailable`; it does not signal or wait
itself. A late failure after process proof cannot cancel candidate finalization.
Cleanup joins share one two-second deadline; this is cooperative bounded waiting,
not a claim that physical filesystem operations can always be interrupted.

Store validates detailed authority in the same transaction, after exact replay
lookup and before event insertion. Run summary projects the first original
Exited of the latest segment, so later historical events cannot erase failure.
Strict DTO decoding rejects unknown, duplicate, wrong-case, null and invalid
values; nil preserves the old wire bytes.

## Verification

Go 1.26.8, macOS arm64, `TMPDIR=/private/tmp`; all Go tests use `-count=1`.
The compact receipt contains original sessions, commands, hashes, skips and
container identities. Counts below are top-level / all named passing tests.

| Scope | Passed | Failed | Skipped |
|---|---:|---:|---:|
| Full Host, ordinary | 399 / 1546 | 0 | 15 |
| Full Store, ordinary | 370 / 1053 | 0 | 0 |
| Full contract, ordinary | 36 / 250 | 0 | 0 |
| Final affected Host, race | 32 / 67 | 0 | 0 |
| Affected Store, race | 9 / 28 | 0 | 0 |
| Final full contract, race | 36 / 250 | 0 | 0 |
| Docker Linux arm64 Host diagnostics | 31 / 52 | 0 | 0 |
| Docker Linux arm64 Store outcomes/summary/settlement | 9 / 28 | 0 | 0 |

Full Store and ordinary contract are valid package results from session 14972;
that command exited 1 because its Host timeout snapshot assertion was stale.
Host was rerun to completion in session 90344. After that, only the two old
provider fixtures changed to the controlled Go helper described below; final
Host race 90497 and Linux 57145 verify those changes. Production, Store and
contract sources did not change after their listed verification.

The 15 full-Host skips are opt-in native checks and child/helper entry points,
including `TestNativeCodingHost` and `TestNativeSchemaContract`; see receipt for
the exact list. Controlled Go actors establish original-process ownership and
capture behavior, not real account/model or installed-provider acceptance.
Store fixtures establish transaction and event behavior with synthetic trusted
identities, not native process proof. Linux did not run race or full Provider,
sandbox, public CLI or installed-distribution acceptance.

Whole-module build 56724 and vet 20165 exited 0 on unchanged production. Final
diff check and independent production/fixture reviews passed. Every owned test
session is terminal. Linux containers ran offline, read-only, unprivileged and
bounded; their exact CIDs were removed and absence confirmed. Raw test logs and
tested binaries remain local evidence; compact receipts are committed.

## Failure evidence and corrections

- Session 33240 exposed absent detailed capture and ignored summary write errors.
  Its initial expectation of a business Failed result was a test assumption,
  corrected to preserve the original business result with observational failure.
- Session 45440 exposed explicit null-bool DTO acceptance. Earlier Store outcome
  failures used `exit_success_fixture` while expecting `owner_review` behavior;
  correcting that fixture was not a product behavior change.
- Session 14972's Host timeout cases expected `after_stop.WaitComplete=false`.
  Successful original Stop now includes original Wait and output-copy completion.
  The assertion now requires true while retaining tree, terminal, tail and privacy
  checks. The protocol comment was corrected consistently.
- Linux v1 rejected `/bin/cat` and `/bin/sh` aliases. Merely canonicalizing them
  in v2 exposed BusyBox multicall `argv[0]` semantics (exit 127). Both aliases
  resolve to `/bin/busybox` in the cached image. Final fixtures use the same Go
  test executable to copy a private finite payload verbatim and explicitly exit,
  preserving the exact 1 MiB / extra byte and privacy/count assertions. No product
  path rule was relaxed. Both failed runs and their cleanup receipts are retained.

## Remaining scope

Codex duplex is not wired: `codexsession.Run` closes the RPC client, whose failure
path closes stdout before necessarily observing EOF. Child Wait proves stderr
and process completion, not that the sole RPC stdout reader drained. A future
same-reader shutdown/drain receipt must precede any complete raw-log claim; no
second stdout reader or duplicate observer is permitted.

Public read/ACK authorization and replay, reservation activation/retained
verification, all-writer artifact intake/delete and the complete 2 GiB allowance,
native flood/RSS and physical disk-fault acceptance remain open. Intel Mac and
Windows native checks await environments. Other F01–F13 work remains active.
This is a local integration milestone, with no account/model calls, installed
copy replacement, push or release.
