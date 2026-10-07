# Public diagnostics read and ACK

Detailed diagnostics can now be read through `diagnostics-read` and acknowledged
through `diagnostics-ack`, including the corresponding `task` commands using a
task handle. The existing run control capability authorizes access to the original
task/attempt/producer/policy/reservation. Execution liveness and current epoch are
not prerequisites; reading does not start a Source Host or change business state.

The independent diagnostics ledger persists a reading fence before filesystem
work and the complete page before returning raw bytes. Exact retries replay the
stored page without opening the namespace. ACK compares the complete receipt,
advances only the diagnostics cursor and remembers the latest duplicate. An
expected revision prevents an old EOF request from becoming a new read when its
cursor is unchanged. Read failure persists an unavailable page; cancellation or
failed persistence keeps the exact reading request recoverable. ACK releases no
artifact reservation.

The persisted envelope is at most 60 KiB with at most 32 KiB of raw records.
Actual outer IPC encoding is checked against 64 KiB using the maximum permitted
128-byte ASCII transport request ID and epoch. Escaped metadata can reduce the
record count without skipping records. CLI request files retain private owned
regular-file, no-follow, link-count and before/after identity checks. Public
errors contain stable codes, without provider bytes or filesystem paths.

## Verification scope

Final commands, source hashes, test counts and container identities are recorded
in `../data/root-final-receipt.json`. Tests use Go 1.26.8, macOS arm64 and
`TMPDIR=/private/tmp`; Linux uses an already cached arm64 image, without network,
with a read-only filesystem, non-root identity and bounded resources.

Counts below are top-level / all named passing tests. Each accepted package has
zero failed and zero skipped tests.

| Scope | Passed |
|---|---:|
| Final full Store, ordinary | 383 / 1095 |
| Final affected Store, race | 13 / 42 |
| Final public Coordinator and peer/business guards, race | 3 / 3 |
| Final public CLI and unsafe request files, race | 2 / 11 |
| Full diagnostics core, race | 59 / 115 |
| Docker Linux arm64 full diagnostics core | 59 / 115 |
| Docker Linux arm64 affected Store | 13 / 42 |

Whole-module build and vet passed after the final production change. Independent
production and fixture review passed, including the last mutex increment. All
task sessions are terminal and all four exact owned container IDs were removed
with absence confirmed. Logs, tested binaries and failed-run evidence are
retained; no disposable temporary file was left by this final verification.

Store tests use real SQLite Submit/Claim records and private Writer/Reader files.
They cover durable replay after reopen and namespace removal, latest/old ACKs,
complete receipt tampering, reauthorization after file reading, competing readers,
EOF revision CAS, rolling gaps, raw versus control accounting, quota rollback and
unmodified business/execution/reservation ledgers.

Coordinator tests use real Unix IPC and a full 32 KiB raw page. The cancellation
test holds a real update lock, disconnects after the reading fence is durable,
joins the request/watchers, checks that no pending page was persisted, then
recovers the same request. It does not prove that the Reader had entered the
flock retry loop at the exact time of disconnect.

CLI tests run real public commands in child processes. The registered original
Host fixture is a controlled Go helper whose original Wait returns zero before
the read. Diagnostics are preseeded by the real Writer. Each CLI child is joined;
the ACK-loss case commits the ACK and disconnects without reading its reply,
then replays through the CLI. Task handle success and task/run/socket mismatch
checks are included. This is not a real Provider or complete Source Host capture
lifecycle. No model/account is called, and no installed copy is replaced.

The database-mutex regression holds the same Store mutex and cancels before
begin, completion and ACK attempt to acquire it. The former blocking Lock fails
all three cases; context-aware acquisition passes. The test does not claim a
specific instruction schedule during mutex polling.

## Failures retained

- Behavioral REDs precede the bounded Reader, Store public API, IPC and CLI
  implementation. Compilation-only failures are not acceptance evidence.
- The first Linux Store run exposed a fixture alias: the request cursor pointed
  into a page subsequently overwritten by JSON decoding. Copying the acknowledged
  cursor fixes the fixture; the production replay logic was unchanged.
- The first combined full race run completed the diagnostics package, then the
  original Store test process was deliberately interrupted after checking its
  identity. Its Store result is not a pass. Final full Store ordinary tests and
  affected Store race tests replace that interrupted Store run.
- Final audit exposed uncancellable database mutex acquisition at three public
  phases. The real failing regression and final passing result are retained.
- Independent review narrowed the socket cancellation coverage claim; the test
  also now asserts reading state and an empty pending envelope after joining.

## Remaining work

Reservation activation, retained-byte verification, all-writer intake and
deletion, full 2 GiB managed-artifact enforcement, native flood/RSS and physical
fault acceptance remain open. Linux verification here covers the diagnostics
core and Store, without Linux race, Provider, sandbox, public native CLI or
installation claims. Intel Mac and Windows native acceptance is deferred for
lack of environments. This milestone does not complete F02 or F01–F13.
