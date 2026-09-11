# Retained Host validation

Scope: bounded macOS Go fixture in `tools/g0-return-lab`; no model-provider call,
installed plugin edit, Codex source/configuration change, or production adapter.

Initial TDD evidence (2026-09-11):

- Before production implementation, three primary lifecycle tests failed because
  `retained-start` returned exit 2 / `invalid_args` (1.961 s).
- The entire new retained suite then failed against the original source
  (4.594 s), again because the new commands were absent.
- After the minimal implementation,
  `go test -count=1 -timeout=90s -run TestRetained ./...` passed (10.151 s).

The suite uses real CLI processes and OS signals/locks. It verifies TERM retention,
KILL/stale-running recovery, contention and owner/revision/segment CAS, immutable
effects, read-only reconciliation, malformed/linked artifacts, and a real blocked
stdout process killed after its last effect but before cached checkpoint commit.

The initial retained suite has 11 top-level tests with additional table-driven
subcases. The native runner separately reproduced an unguarded foreground process
surviving native Codex SIGKILL, so the direct-parent guard is required.

Direct-parent TDD:

- A real shell helper is the fixture process's direct parent. Killing it while the
  child blocks in its initial output, waits between steps, or blocks after its last
  effect left the original foreground process alive in all three cases. The
  improved harness asserts child liveness independently of inherited stdio: RED
  `go test -count=1 -timeout=90s -run TestRetainedParentDeath ./...`, 7.710 s.
- The guard records the initial PPID, refuses PPID 1, and cancels on reparenting.
  Only the execution goroutine writes business records. GREEN for the same
  command: 8.086 s, exit 0. Each test resumes the retained record and verifies that
  the committed prefix survives, and the final-effect case adds no segment.
- At that source stage, full normal/race/vet each exited 0 (normal 29.694 s;
  race 31.540 s). The race command instruments real CLI subprocesses too.

Independent review found two additional regressions and provided reproductions.
Both were rechecked in new permanent tests before changing production code:

- A reader's inode stat can show two links just before a publisher removes its
  staging link. The old validator rejected the now healthy one-link final file.
  The test uses real inode snapshots and filesystem operations, without a mock.
- If stdout and stderr share a full pipe, cancellation stopped business writes
  but the old main error diagnostic could block forever. Both SIGTERM and SIGINT
  reproduce this with a real child process and a filled pipe kept open by the test.

The combined review regression command failed in 3.856 s. The minimal fixes
recheck the same inode's current link count, retaining foreign-link rejection, and
bound only retained-command final stderr diagnostics to 250 ms. Earlier detached
and ACK error output remains synchronous with its original shape and exit codes.

After both fixes, the review tests plus unsafe-artifact regressions passed in
5.812 s. Final production source verification with Go 1.26.8 darwin/arm64:

| Command | Exit | Result |
| --- | --- | --- |
| `go test -count=1 -timeout=90s ./...` | 0 | `ok g0-return-lab 30.607s` |
| `G0_LAB_TEST_RACE=1 go test -race -count=1 -timeout=90s ./...` | 0 | `ok g0-return-lab 32.887s`; CLI children also built with `-race` |
| `go vet ./...` | 0 | no output |
| `go build -o ABS_TASK_BUILD_DIR/g0-return-lab-retained-darwin-arm64 .` | 0 | macOS arm64 executable |

There are now 14 retained top-level tests, in addition to unchanged baseline and
ACK suites. After these full runs, the concurrent-resume test gained one extra
assertion that reuses the winning completed task: its old expected segment must
still fail after the winner has exited. The production source and binary did not
change; that test was rerun with and without the race detector (2.915 s and
4.343 s respectively, both exit 0).

Build SHA-256:
`feb9b9ad969abca5ff35db28c0f0d8d920dc0778d7aac68a90952900e989a285`.

Reviewed production candidates:

- `main.go`: `fd3a7699abdd7b963ea6f70bd5823f9b350980561dc9925245a4469f881b7544`
- `retained.go`: `144c199e3dc863b30517500e57428f689c09a8bbbb13d19c58ee11ff5776650d`
- `ack.go` remains `20767515b37be6ab23c3bb3d5d62f079b99f81ce26d68f671a54c11be1dbf2da`.

Independent re-review of fixed commit `5b96919` passed. The reviewer reran its
original I1 filesystem-barrier and I2 merged-full-pipe reproductions; both turned
green. In a fixed archive copy,
`go test -count=1 -timeout=90s -run 'TestRetained|TestReview' ./...` exited 0
(17.508 s), and independent build / `git diff --check` exited 0. The run includes
the three real direct-parent death boundaries and the completed-task old-CAS
assertion. There are no remaining source findings. This allows the coordinator
to proceed to native experiments; it does not constitute native or full G0 PASS.

Native Codex integration results remain separate from these fixture tests.
TestMain and testing.TempDir
remove their own binaries/files; only the task-owned build binary, cache, and
verification logs remain under the coordinator's task build directory until the
native runner has copied the reviewed binary and the coordinator cleans caches.
