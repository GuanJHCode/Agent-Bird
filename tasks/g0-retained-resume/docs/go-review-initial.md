# Independent retained lifecycle review: ba4d19e

Base: `66bb9f0`; inspected head: `ba4d19e`.
Read-only source worktree; a `git archive` copy under this task directory was used for reproductions.

## Important: healthy publication can be rejected by inspect

`retained.go:148–162,175`: read-side Stat can capture two links, followed by the producer removing its staging link. The remaining file has one link but paired Lstat now fails, causing `untrusted_file` instead of a bounded snapshot retry.

Command (task-local GOCACHE/GOMODCACHE/TMPDIR/GOTMPDIR; existing Go 1.26.8 toolchain):

```text
go test -count=1 -timeout=90s -run 'TestRetained|TestReview' ./...
```

Result:

```text
--- FAIL: TestReviewHealthyPublishedFileAfterStageRemoval (0.00s)
    reviewer_race_test.go:29: healthy final file rejected when stage is removed after reader Stat: old nlink=2, current nlink=1
FAIL
FAIL g0-return-lab 9.571s
```

The injected review test uses real filesystem operations and controls the Stat/Remove interleaving; it does not alter production functions. This is a transient false rejection, not observed effect duplication or data corruption.

## Important: shared full stdout/stderr prevents signal exit

`retained.go:412–435` returns the cancellation error to `main.go`, which writes the error synchronously to stderr. A shared full pipe leaves that write blocked after state becomes interrupted.

Command: `python3 scripts/merged-stdio-repro.py`.

Observed: pipe filled with 65,536 bytes; child received SIGTERM; after one second, `exited_within_1s_after_SIGTERM=false`, `exit_code=null`, persistent state `interrupted`, `completed_steps=0`. The test then killed and reaped only its own child.

Business writes had stopped; the process did not exit. The retained bounded-output promise therefore needs a bounded final error path too. Legacy command behavior should remain unchanged.

## Other checked boundaries

- Owner/revision/expected-segment checks occur under the execution lock before completed replay.
- Immutable step files determine reconciled progress; completed replay does not increment segment.
- State timestamps require canonical UTC RFC3339Nano round-trip; schema, IDs, hashes, numeric fields and enums are bounded before output.
- Native G0 and the later parent guard are outside this intermediate verdict.

Ready to merge: **With fixes**, and only after the later parent guard plus required full validation are reviewed.
