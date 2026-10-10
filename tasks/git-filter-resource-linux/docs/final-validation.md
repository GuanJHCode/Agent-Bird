# Linux Store resource directed validation

Source `431cdb9b83a422081e73fe7f8a943a704b8c64c4` was compiled with the pinned Go 1.26.8 binary for Linux/arm64 with `CGO_ENABLED=0` and offline Go resolution. The compiled `tools/orchestrator` tree is identical at source and runner heads.

The exact expected closure is the union of the 20 Store named PASS results from `resource-final-race.stdout.jsonl` and the 8 named PASS results from `migration-fixture-green.stdout.jsonl`: 8 top tests and 28 named tests. Source raw SHA-256 values are retained in `data/source-inputs.json` and `data/summary.json`.

The cached digest-pinned image was run once with pull disabled, network none, read-only root, non-root UID, all capabilities dropped, 96 PIDs, 768 MiB, two CPUs and isolated tmpfs mounts. The terminal result was 28 PASS, 0 FAIL, 0 SKIP, attach exit 0, container exit 0 and no OOM. The runner removed the exact CID and a separate inspect confirmed it remained absent.

The task-owned cross-compiled binary was fsynced and removed only after identity, UID, link-count and hash checks. The initial cleanup attempt detected a locally incorrect `0555` mode receipt against the actual `0755` binary and preserved both the binary and raw failure. The receipt was corrected to the observed mode; the retry reused the successful exact-CID absence evidence and completed file and directory fsync-backed removal. No product source, native user flow, model/provider, installation, network pull, push or release was performed.
