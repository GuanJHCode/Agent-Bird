# F03 ordered-compose Linux isolation validation

- Frozen source commit: `206e1584c79e4db5f9e0563e6ca1c51130ee1539`.
- Cross-compile: `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` with `/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go`.
- Test binary SHA-256: `636cb8b291eb0e5ee896508bc7907b9b3f3cd3a91e810d53ac0d4fcec6365dfe`.

## Executed Store scope

The offline Redis image `sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99` ran exactly:

`^(TestOrderedComposeStoreCaptureAndPendingReplay|TestOrderedComposePreservesDecisionAuthority)$`

Top-level named tests: 2; named results including subtests: 4 pass, 0 fail, 0 skip. Container CID was `91fd15261c5a7050158365ebeeab03f918434906b0ed5c650b73cf32cb5531f2`. The receipt records original attach completion (`start_exit=0`), exited state (`ExitCode=0`, `OOMKilled=false`), `docker rm` success, and exact post-removal `docker inspect` absence.

Raw execution and lifecycle evidence are retained in `data/linux/store-focused.log`, `data/linux/store-focused-receipt.json`, and `data/linux/store-run.stdout`.

## Git-dependent scope blocked by sandbox image capability

The required GitOps, gitopsworker, and controlled fake-provider host-chain cases were not started. The initial required image has no Git. An exhaustive read-only local-image enumeration found only that Redis image, Freqtrade `sha256:06e4a2020436c7159246a18960e51cb799681ab176d88fbb57c1a3849dda4f9f`, and MySQL `sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b`. Under the same isolation constraints, both candidate images reported `usrbin-git=no` and `path-git=no`.

Each candidate probe retained fixed image SHA, CID, attach output, after-exit inspect, removal output, and exact post-removal absence evidence in `data/linux/`. No image was pulled, installed, or networked. The Git-dependent cases remain unverified because no cached compliant image provides Git.

## Cleanup

The owned cross-compiled binary is removed through the local trash after its SHA-256 was recorded. Raw logs, receipts, image probes, and build output remain retained.
