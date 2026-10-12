# Join validation checkpoint

Root implementation (review pending): explicit public action, two accepted managed artifact inputs, fresh signed helper pin, exact live C/R permits, normal pinned merge, and both cleanup receipts.

- `go build ./...`: exit 0 (build.stdout/stderr/exit).
- focused race: six top and ten sub tests passed. New public Store test failed at legacy predecessor bound; raw failure retained in focused.jsonl.
- corrected only admitted filter_integrate chain bound to six. `store-fixed.jsonl`: new public claim/PendingLaunches/atomic settlement test PASS (1 top, no fail/skip, 9.39s).
- first public native: five real controlled stages accepted, final claim rejected by same old bound, 314.04s FAIL; no integration process started, pending=0. Retained fixture is evidence, not an original live owner. `public-native.*` preserves failure.
- root D prior happy-path: 1 top PASS 123.71s, no carrier; raw d-native.*. D callback-gap correction still delegated.
- Optional accepted-continuation diagnostic can reuse immutable accepted managed artifacts and create entirely fresh final-stage owners; it does not import saved runtime ownership and is not a standalone all-stages CI claim.

No real provider account/native TUI, broad matrix or long-duration resource acceptance was executed.

## Final accepted continuation and D guard

- D pre-start correction `9cd3a89` independently reviewed PASS. New original native regression: 1 top + 1 sub PASS, 0 fail/skip, 104.636s, stderr empty. Raw copied byte-for-byte from the isolated worker into `d-prestart-green.*`. Worker did not preserve a shell exit sidecar; only original Go package/test PASS is claimed. The earlier worker output was corrupted by overlapping output writers and is not used as passing evidence.
- `accepted-continuation.jsonl` first diagnosed a test-only validation invocation indexing an empty command. No Host execution/spool had started; test now uses the same zero-command filterInvocation as preflight. Pending launch replay is allowed by this diagnostic only when no final-stage spool exists. Production code was unchanged.
- `accepted-continuation-fixed.*`: 1 top PASS, 0 fail/skip, 165.91s, shell exit 0, stderr empty. Reads both real managed accepted artifacts from the earlier five-stage run; creates fresh final-stage Broker, carrier and transition. Actual normal Git merge exits 0, target branch/bytes match the candidate; original Wait/tree and carrier removal are proved, both Store reservations are released by the same Exited event, result accepted.
- Evidence is a five-stage original run followed by a fresh last-stage accepted-input continuation, not a new single-run six-stage PASS. Immutable accepted evidence was reused; no saved scalar/journal reconstructed a live original owner.

Final code review: B2, C, Host evidence, public join, and D guard each PASS. Remaining real-provider/TUI, platform, resource/physical fault limits above remain unchanged.
