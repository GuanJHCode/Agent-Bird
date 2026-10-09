# F13 candidate package provenance

Scoped acceptance PASS on clean source `a68a901ec57e4d96200ee20f48eae6e5969e8a95`, containing reviewed production `5cbd657`. The complete orchestrator tree is `27aa641629f5c07fd0f8726684c16eb7e4040611`. This validates macOS arm64 packaging and isolated public administration, not F01–F13 overall completion.

## Actual checks

- Go **1.26.8 darwin/arm64**, pinned Python **3.14.3**, dependencies verified from the existing cache with network fetch disabled. GOENV/GOWORK disabled; GOFLAGS empty; explicit Darwin/arm64, CGO0, trimpath and buildvcs=false. Exact tool paths, Python hash, flags and Go build metadata are retained.
- Two independent build invocations produced the same binary SHA256: `60a679b730775bcab7a66d978c32c1df16c083a0d202f7d994a22f74c90b5f29`.
- The formal `package-plugin.sh --kind full-native-hooks` ran twice. All **28 package files** matched byte-for-byte and by mode. An independent fixed closure checks **24 source files**, the one intentional plugin version rewrite, the binary and generated runtime/payload manifests. Package manifest SHA256: `0b00dda95d0062c76afd2b6bd6af55db7a0aa74d0c8aeca083cda015e634b880`.
- Public `install --request` used only a fresh task-owned installation directory. The installed set was exactly the **27 payload files plus one installed manifest**, with matching hashes/modes/owners, no extra file or empty directory, symlink or hardlink.
- Exact repeated install returned the same receipt and retained binary device/inode/mtime. Public Doctor reported healthy with the single expected version and no pins/issues.
- Public uninstall removed only that test version. The separate runtime-data sentinel survived. Post-uninstall Doctor was healthy with no installed versions or pins/issues.
- **12 actual commands**, all exit0 and empty stderr. ROOT independently checked the raw admin replies, actual files, hashes, modes and remaining data. These are command-level acceptance checks, not a Go test count.

Raw commands/stdout/stderr/exits, the full receipt and ROOT verification are in `data/candidate-first/`. Static independent review of final runner `3168e07` passed. Prior review defects (toolchain gate, independent source closure and fresh output parents) were fixed before execution; there is no failed package run to overwrite.

## Retained artifact and cleanup

Local package version: `0.1.0-closure.a68a901ec57e`. The retained package path is recorded in `data/candidate-first/receipt.json` and `root-cleanup.json`; it is in this task's isolated `closure-package-provenance` worktree under `tasks/closure-package-provenance/tmp/candidate-first/package-a`.

After all original command waits completed and no process referenced the exact work path, ROOT rechecked hashes and removed only duplicate build binaries and package B (**63,307,932 bytes**). Package A, raw evidence, installation receipts and the preserved data sentinel remain. The test installation itself was already removed by the verified public uninstall.

## Limits

`full-native-hooks` is the package format name, not proof of installed Hook/TUI consumption or real Provider/model qualification. No plugin registration, real model/account, user configuration/install replacement, push or release occurred. The package pins this machine's existing Python path/hash; this is not a self-contained interpreter bundle or another-machine installation claim.

Earlier Linux collect-only installer evidence remains limited to its original source and mode; no latest Linux full-native, Intel Mac or Windows acceptance is claimed here. Resource Formal60 remains FAILED, and old resource measurements do not qualify this binary. F04 isolated extension execution, broader retries/retention and other unresolved programme items remain open.
