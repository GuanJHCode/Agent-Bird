# Linux profile admission validation

Implemented on baseline `92e4b49ab724916445816a84d4f28bc576bf671e` in the isolated
`feat/linux-profile-admission-20261008` worktree. Production delta is three lines
in `tools/orchestrator/internal/host/profile.go`: the existing non-Darwin
`profile_sandbox_unsupported` rejection now precedes invocation decoding and
Claude/AGY history preparation. The lower-level platform rejection is retained.
Darwin behavior and Linux execution permissions are unchanged.

## Contract and tests

`docs/support-matrix.md` keeps Linux Provider execution and sandbox closed. The
existing lower-level sandbox wrapper rejects unsupported platforms before any
filesystem work. The new Linux-only `TestLinuxProfileAdmissionRejectsBeforeHistory`
extends that behavior to the Host wrapper. Its two valid initial-invocation
fixtures reach the real Claude/AGY history branches on the baseline.

The test calls the actual wrapper and compares the returned command and caller
slices with a detached original. Real fixture-tree observations compare named
inode identity, permissions, mtime, regular-file bytes and symlink targets, and
reject added or removed entries. The tree includes configuration, private spool,
history and the selected scratch path. AGY uses the existing pure filesystem
fixture. Claimed fixture lock versions/hashes select code branches only; no
Provider is executed or qualified, and no real account or config is accessed.

## Evidence and accurate counts

Pinned toolchain: Go 1.26.8; local `TMPDIR=/private/tmp`, `GOTOOLCHAIN=local`,
`GOPROXY=off`, `GOSUMDB=off`. Both Linux binaries were built with the standard
complete package command `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c -o
<stage>/host.test ./internal/host`; no test source was excluded.

Linux selector, identical in RED and GREEN:

```text
^TestLinuxProfileAdmissionRejectsBeforeHistory$
```

| Run | Top-level tests P/F/S | Named tests P/F/S (parents included) | Package result | Container exit/OOM |
| --- | --- | --- | --- | --- |
| Linux behavior RED, before guard | 0/1/0 | 0/3/0 | FAIL | 1/false |
| Linux GREEN, after guard | 1/0/0 | 3/0/0 | PASS | 0/false |
| Darwin ordinary history/command regression | 9/0/0 | 52/0/0 | PASS | not a container run |

The RED was a behavior failure, not compilation or fixture setup failure: both
subcases already returned the expected unsupported error, but rewrote command
argv/env and created history directories and `provider-history.json`. Raw
failures are retained in `data/red/raw.log`; final raw output in
`data/green/raw.log`. Structured events and independently parsed counts are in
each stage's `tests.jsonl` and `result.json`.

Darwin regression executes the exact nine pure test selectors stored in
`data/local/commands.json`: the existing Claude history preparation test and all
eight tests from `agy_history_test.go`. The AGY wrapper constructs sandbox
commands but never starts sandbox-exec, git, a Provider or another OS actor.
`data/local/regression.jsonl` contains actual test events and
`data/local/counts.json` gives counts. Host `go vet ./internal/host` passed on
Darwin and with Linux arm64 cross-build environment; both exit codes are 0 and
both output logs are empty. `gofmt` and `git diff --check` are clean.

## Container isolation and cleanup

Fixed local image:
`sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99`,
observed Linux/arm64. Both create commands used `--pull=never`, network none,
read-only root, cap-drop ALL, no-new-privileges, UID/GID 65534, init, 256 MiB
private `/tmp` tmpfs, and a read-only stage-specific binary mount. Image entry
point was replaced with the test binary. The container did not run an image
service or require external utilities. Exact command arrays and full container
inspect snapshots are retained per stage.

| Stage | Container name | Saved exact CID |
| --- | --- | --- |
| RED | `root-linux-profile-red-4d90dedced97` | `21de7357c2f1e779a85ac4602c3bf1508ed4a5a243673a7a670667e2ede3218c` |
| GREEN | `root-linux-profile-green-cdba268aaaaf` | `abc0c6a48304809f128ffd443086de6c35c96614c36fae71ad96ba772da3e3d2` |

For each CID, `docker rm <exact CID>` returned 0 and subsequent `docker inspect
<exact CID>` returned 1 with `No such object`. `create.cid`, `remove.log`,
`inspect-absent.log` and structured cleanup results preserve this evidence.
No other container was queried by container ID, stopped or removed.

## SHA256 and retained outputs

| Artifact | SHA256 |
| --- | --- |
| Final `profile.go` | `27170d6b4971ea87ef1fc508cda43702b0a8e94d28531e1f6934f1b801c15dc7` |
| `profile_linux_admission_test.go` | `a5e68808023c68b36a9e8bb6abc6191c5bfd8e80d2908cc6d4d4e0bbb26d7fab` |
| RED Linux binary | `4d90dedced97cbab743983bbc2804a8489ca941d55bbc3cded5163fc9dabc61a` |
| GREEN Linux binary | `cdba268aaaafc41acf54654b15bb351ef78c51eb4cf6f1f65c1a4d26230edae3` |

Raw evidence and the offline runner are committed. The two stage binaries in
`tasks/linux-profile-admission/tmp/{red,green}/host.test` are retained as useful
local verification outputs and are ignored by the repository; they are not
committed. No disposable scratch file outside those owned output directories
was created. Fixture temp trees were removed by Go's normal test cleanup.

## Scope and remaining validation

This closes only the unsupported-platform history-preparation ordering gap.
It does not guarantee that all earlier Host launch operations have zero effects,
provide a Linux sandbox backend, or qualify a real Provider, packaging or TUI.
There is no owner handoff dependency and no Store/Coordinator/contract change.
No race, full-package runtime, real Provider, account, user installation,
physical power-loss or other native actor test was executed. These focused runs
have zero skipped tests; they do not establish a broader UID/platform matrix.
Independent review and integration belong to ROOT and were still pending when
this implementation snapshot was delivered.
