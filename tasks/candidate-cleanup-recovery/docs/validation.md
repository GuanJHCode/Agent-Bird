# Candidate cleanup recovery validation

Product candidate source: `b711b5a45b69d69ae0d3b8fe4f4df8810f6ce209`. Initial prototype 1559092/1a3e328 remains unmerged. Root took over the filesystem state machine and the remaining journal reservation work after partial worker deliveries.

## Behavior delivered

A durable journal now records one immutable receipt-bound plan with held original root/admin/parent identities, exact inventories and private 0700 sinks. Ordered intents precede exclusive rename, individual move/verify/unlink, root and sink removal, admin removal and final expected-OID private-ref deletion. Each parent is synced before the next intent. Plan-only and effects-before-next-record states resume; exact final replay does not append. Legacy pending history never gains deletion authority. Canonical existing-parent resolution survives a missing leaf and benign parent aliases. A moved or rebuilt foreign object is retained and keeps cleanup pending.

One inode/size/modtime-stable, locked journal scanner validates all records before interpreting them. Append validates the named and held journal/lock owner, mode, inode and nlink; total capacity is 16 MiB and each record at most 1 MiB. Plan admission reserves all remaining action records, the dynamically sized final outcome (at least 4096 bytes), and 60 KiB additional process-record allowance. Other append operations cannot consume the action/final reservation. Exact replay records consume no further space.

## Evidence

- `root-recovery-final-race`: **gitops package only**, 4 top + 4 sub, PASS, pkg 8.058s. The same shell command initially failed to compile gitopsworker due to a test-local `:=`; that package result is excluded, shell exit 1 is preserved.
- `root-journal-final-race`: 5 top + 7 sub, 0 fail/skip, exit 0, stderr 0, pkg 4.547s. Durable close/reopen after every action, final append failure, exact third replay, legacy history, unsafe authority and ordered scanner.
- `root-journal-capacity`: ordinary focused test, 1 top + 2 sub, 0 fail/skip, exit 0, stderr 0, pkg 3.597s. Near-capacity rejection before plan and guaranteed remaining checkpoint/final capacity. The earlier race command `root-journal-race-1` was explicitly interrupted after its durable-replay case passed because constructing the large fixture by repeated rescans was needlessly slow; it is not a passing suite. The replacement constructs the same bounded legal fixture once and checks production admission.
- `root-alias-race-2`: 3 top, 0 fail/skip, exit 0, stderr 0. Full durable sequence requested through a parent symlink, final canonical root, plus legacy final/rebuilt-root replay. The first alias command had a local variable redeclaration compile failure; both raw results remain.
- `root-native`: one original source→accepted→validate native test, PASS 4.64s (pkg 5.347s), exit 0, stderr 0 at 608ce6b. Later alias normalization is covered by the targeted race above; the canonical native path and production Host wiring are unchanged.
- `root-build`: full Go module build exit 0, stderr 0 at 608ce6b; final source changes additionally compiled and executed by alias race.
- Independent review: 6bb2245 admin identity blocker fixed at 608ce6b; 608ce6b alias blocker fixed at b711b5a. Final targeted independent review PASS. Earlier unmodified findings were reused.

`data/verification.json` binds the 11 changed Go source/test files to source SHA256 values and records actual report counts. Raw JSON, stderr, exit and commands are retained, including interrupted/compile-failed runs.

## Limits and next work

This completes recoverable cleanup machinery and preserves the production temporary-worktree path. It is not the full candidate TTL service: original sealed registration, durable consumer fencing, owner-authorized maintenance execution and scheduling remain next. No real user runtime was collected. No full suite, long provider chain, real accounts, power-loss injection, Intel/Windows acceptance or installation replacement was performed. Interrupted pre-plan creation may retain empty private metadata directories; unknown directories are never scanned/adopted. Same-UID non-cooperating mutation during individual verification/syscall windows remains outside the cooperative locking protocol. No strict wall-clock guarantee is claimed for a blocking filesystem syscall.
