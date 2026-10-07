# F07 Linux original-process start verification

Scope: this increment enables the existing original-process launcher on Linux.
It does not enable a Linux Provider, sandbox, or installation flow.

`startCommand` now accepts Darwin and Linux only. It retains the existing
original `Handle`/`Wait`, birth identity, process-group Stop, TERM-to-KILL,
pipe, and post-Start-error cleanup code paths. Other platforms still return
`unsupported_platform`.

The RED container session used the pre-change binary and ran the nine named
lifecycle tests. All nine failed at the original `unsupported_platform` gate:
short-lived original Start, owned Start, TERM-to-KILL, graceful TERM cleanup,
post-Start birth-unknown handle retention, Duplex echo, Duplex pipe drain,
live-descendant EOF/UNKNOWN, and Duplex post-Start birth-unknown cleanup.
Raw log SHA-256: `8f346de0783ffd45a46d1d7ff9e709252bfabd63108cbc8353f1bf3f74a08c11`.
The terminal CID was `f481718eddbed751704999ea28d67277ec11e9cd4d96c4cde68274c39119ff04`;
post-terminal `docker inspect` returned `No such object`.

The GREEN session ran the same nine names from Linux arm64 test binary
`f22d003358fa9a506687bf44800007f0263a7e9dd9c2ea0a42f64e83bfef0c6b`.
It recorded 9 PASS, 0 FAIL, 0 SKIP, exit 0. Raw log SHA-256:
`0ef52372e39e98aa3f3b851d3cd844ad2200fab1bd46701f620c5ef2a26d187a`.
The terminal CID was `9dc9cd3339d10b5a17afdb0d308d688c611fe9307fef5345966ee0f232d6844e`;
post-terminal `docker inspect` returned `No such object`.

Both sessions use the pre-cached image
`sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99`,
with no network, read-only root, uid/gid 65534, no capabilities, no-new-
privileges, and bounded PID/memory/CPU/tmpfs. The exact commands and receipts
are in `data/*-receipt.json`; raw output is in `data/*.log`.

Additional checks: Linux arm64 `go vet ./internal/process` exited 0, and
Darwin arm64 `go test -c ./internal/process` exited 0. The latter is compile
coverage only; no macOS process actor was run.

## ROOT final verification (2026-10-07)

The earlier nine-case worker matrix is superseded for acceptance by the
stable nine-source-file snapshot in `data/root-final-receipt.json`.
ROOT retained the worker RED/green files as provenance, not as substitute
for the following final evidence.

- Linux arm64 full process package: original session 80832, container exit 0,
  **77 top-level / 152 named PASS, 0 FAIL, 0 SKIP**. CID
  `6b9c5e1ba35213df4f756e19d33106ad187045ed434e1729102514e027f4fbf0`
  was inspected terminal/not OOM, removed by exact ID and confirmed absent.
  The read-only, network-disabled cached image and all resource restrictions
  are preserved in `data/root-linux-group/full-v2-receipt.json`.
- macOS arm64 full process package with `go test -race -count=1 -json`:
  original session 80231 exit 0, **63 top-level / 131 named PASS,
  0 FAIL, 0 SKIP**, 40.715 seconds. Go 1.26.8.
- `go build ./...` session 36042 exit 0; `go vet ./...` chunk 2e00f9
  exit 0; final `git diff --check` passed. Independent production and
  fixture review returned PASS for the final nine Go files.

The final increment also replaces Linux group-alive enumeration with the
kernel `kill(-pgid, 0)` proof. Only ESRCH proves absence; errors such as
EPERM remain UNKNOWN, and cancellation wins before/after observation.
Original Handle.Wait remains independently mandatory. The conservative
/proc scan for quiescence is unchanged. A controlled probe unit RED ran in
Linux (session 16267 exit 1, five subcase failures); it does not claim a
real system-churn reproduction. Real Linux zombie/original-Wait and
leader-gone/member-live tests pass in the final full suite.

ROOT added a real persistent same-group descendant test: the product
Handle.Stop sends TERM then KILL, original leader Wait reports -SIGKILL,
and both leader/group and the previously identified descendant are proved
absent. This is distinct from the existing duplex negative test, which
correctly retains UNKNOWN after leader Wait/EOF until a private descendant
release. No fixture release is used in the new successful tree-stop path.

The first ROOT full Linux run (session 33656) had 149 named passes and three
`command_path_untrusted` fixture failures from hardcoded /bin/sh or /bin/sleep.
The final fixtures use the same canonical test binary and preserve their
TERM, original Wait, passive-drain and exact-environment assertions. The
product executable trust rule was not weakened. All ROOT Docker sessions
are terminal and their exact containers removed. Necessary failed logs and
binaries are retained; no temporary actor or container remains from them.

No Linux race, Linux Provider/sandbox/install flow, Intel Mac/Windows native
acceptance, or real model/account call is claimed. Physical /proc churn and
escaping-descendant containment remain outside this increment.
