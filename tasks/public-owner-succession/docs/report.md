# Public owner succession checkpoint

Base `3669cb8`, branch `feat/public-owner-succession-20261008`. This is a
**failing diagnostic checkpoint**, not completed public/native acceptance.
ROOT resumes production diagnosis in another worktree; native resources are
returned. This worktree remains frozen pending the repair commit.

## Scope and implementation

- New `cmd/orchestrator/public_owner_succession_native_test.go`: actual public
  submit/stop/status/stable-command Resume/replay/collect/accept through the
  production `run`, real signed owner runtime, real workers and ancestry.
- `candidate_test.go`: two task-private basename dispatches only; existing
  worker fixture and assertions unchanged.
- Authorized `original_owner.go` private observer: original wrapper still passes
  nil; sole original `exec.Cmd.Wait` reports PID/actual ProcessState/error once
  before done. No Cmd exposure, global hook, environment or public flag.
  Test callback output uses a private inherited pipe and one-second deadline.
- Controlled source launcher retains the registered FirstHost's actual Cmd;
  enrollment, Owner Start/Bind/runtime and worker execution remain production.
  This substitutes installation-level Source launching with a test executable,
  and therefore does not qualify an untouched installed CLI or Provider.
- Read-only SQL observations only. No SQL retirement, fake death, reconstructed
  Wait, Scope rewrite or production lifecycle fix.

## TDD and evidence

Pinned Go1.26.8; TMPDIR `/private/tmp`; local/offline Go dependency resolution.
Exact argv/exit codes are adjacent `data/*-command.json`; raw Go JSONL retains
all output. `data/counts.json` counts named tests, excludes package rows.

| Evidence | Named pass/fail/skip | Meaning |
| --- | --- | --- |
| api-compile-red.log / -2.log | no actor | draft setup failures, not behavior RED |
| api-compile-red-3.log | no actor | compile-only missing private observer API |
| observer-red.jsonl | 0/1/0 | actual exit17 original Wait completed without observer |
| observer-green.jsonl | 1/0/0 | original handle, exact ProcessState, eight callers, one callback |
| public-flow-1.jsonl | 1/0/0 | initial full business flow green; insufficient exit2 cleanup assertion; superseded |
| final.jsonl | 1/1/0 | strict successor retirement exposed actual failure |
| cleanup-diagnostic.jsonl | 0/1/0 | same failure with exact returned Owner error, latest source |
| vet.log | exit0 | `go vet ./cmd/orchestrator`, latest source |

Final focused selector:
`^TestPublicOwnerSuccession(WaitObserverUsesOriginalHandleOnce|DeadBothResumesSameAttempt)$`.
Latest diagnostic selector:
`^TestPublicOwnerSuccessionDeadBothResumesSameAttempt$`,
`go test -timeout 120s -count=1 -json -run <selector> ./cmd/orchestrator`.
No race, Linux, Docker, Provider or full-suite test in this task.

## Business progress and blocker

Every public-flow run reached stop settlement, old Owner/Host death, live
original origin, actual incarnation2 with same attempt/new segment, exact new
worker ancestry, old budget-row preservation/remaining grant, scope-only recovery
ledger handoff, immutable exact-command replay, result_ready then public
collect/accept/completed. Actual first worker Exited is signal exit `-15`; this
is positive physical exit proof, not a forged positive exit code. Second worker
Exited is0. Stop and Exited are unique and exact PID/birth; ESRCH proves reaping.

After accept, while Coordinator is still alive, the successor Owner already
returned `host_retired\nconflict` and exited2. FirstHost follows exit2. Production
`retireCompletedHosts` sends the terminal host_retired; the precise conflict
origin remains ROOT's independently assigned investigation. This is treated as
product failure, not normal graceful retirement. No assertion was relaxed to
accept arbitrary nonzero exit and no production repair was attempted here.

Latest actual original handles / receipts (`cleanup-diagnostic.jsonl`):

| Actor | PID | Actual result |
| --- | --- | --- |
| Coordinator owned Cmd | 59313 | Wait nil / exit0 |
| old FirstHost owned Cmd | 59342 | original Wait nil / exit0 |
| old Owner owned Cmd | 59397 | original Wait nil / exit0 |
| original worker | 59629 | exact actual Exited -15, reaped |
| successor FirstHost owned Cmd | 59880 | original Wait `exit status 2` / exit2 |
| successor Owner owned Cmd | 59934 | original Wait `exit status 2` / exit2; run error host_retired + conflict |
| successor worker | 60213 | exact actual Exited0, reaped |

Original records copied unchanged to
`data/cleanup-diagnostic-original-waits/`, including
`original-owner-error-59934.json`. Controlled test/worker binary SHA256:
`016eef4546f62a631bab968ad3affe72c1d7ff445c1c548fea6b0af834dc6cb9`.
All three source hashes are in `data/source-sha256.json`.

## Cleanup and next step

All observed actors reached physical exit, every owned Cmd's original waiter
completed. No timeout or forced cleanup in the executed runs. Passing first
flow removed its private root; failing roots remain intentionally intact:
`/private/tmp/public-owner-succession-2817075760` and
`/private/tmp/public-owner-succession-3266240994`. No unknown files or other
worktrees were removed. Compile output directory contains no binary (compile
RED failed); retained evidence is intentional.

ROOT owns native resources now and will provide an isolated production repair
for replay verification. Current test remains FAIL at the strict retirement
check. Independent review, final green/race/platform qualification and installed
CLI validation are not completed by this checkpoint.
