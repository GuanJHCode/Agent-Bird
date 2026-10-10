# Filtered target Host preflight qualification

Qualified source: `2ec598fc08e8b3a245d24e4d0985c0f95aed5eb6`, orchestrator tree `b89f0eae5c2055e431de186591de4ba2bbeb02c5`. Production is `e91b30aee011aaae8fc2afa5be07cfe6d6c11dfa`; later differences are tests, evidence and documentation. Independent production review passed e91b30a, and strengthened native-oracle review passed d43454a. ROOT verified raw command results/counts and the absence of both successfully cleaned owned native roots. Machine-readable evidence is [root-verification.json](../data/root-verification.json).

## Behavior

Darwin admits the separate `filter_target_preflight` action after one owner-accepted approved filtered content review. It retains that review's complete producer/validator/reviewer bindings, original receipt hashes and zero Target. A new Host-only observation envelope binds the exact accepted input event/artifact/decision, target tuple, candidate and original fresh filter resource grant. This provides no target approval, mutation or deletion authority; filtered integrate remains closed.

The action uses no dummy Provider command or invented process/exit code. The original Host callback records starting before entry and measures its complete monotonic interval. Known errors/cancellation settle as Failed/Stopped plus Exited; original process/cleanup uncertainty remains UNKNOWN. Success requires original Broker/tool cleanup before Result. Stop arriving during terminal artifact publication suppresses Result and retains the same artifact on Stopped/Exited. Store authenticates the persisted action and artifact without parsing Host-private content; its nil-exit success exception is restricted to this exact action.

`OpenFilterTargetGit` installs a source-owned bounded index/metadata guard before index-consuming source capture. Every original Git journal callback, including intent and last exited, rechecks held FDs. Child contexts retain the parent guard from initial source opening; private-held calls skip only their current mutex. Metadata/index checking reopens named gitdir/info identities. Observer Close never closes the source guard or grants file cleanup. These are boundary checks, not exclusion of arbitrary same-UID changes during a running Git process.

## Evidence

| Evidence | Fixed source | Result |
|---|---|---|
| Admission and Store grant/settlement race | 7876dc0 | 2 top / 9 named PASS, no failures/skips |
| Host source/invocation, late Stop, root/child index and directory drift race | e91b30a | 5 top / 29 named PASS |
| Store success and stopped-artifact settlement race | 3e2f241 | 1 top / 3 named PASS |
| Nofollow inventory oracle race | d43454a | 1 top / 4 named PASS |
| Final actual four-stage Host chain, race | 2ec598f | 1 top / 1 PASS, 434.89 seconds |
| Whole Darwin build, Linux arm64 CGO=0 build, related vet | 3e2f241 | All exit 0, empty stdout/stderr |
| Final Host vet | 2ec598f | Exit 0, empty stdout/stderr |

All passing commands above exited 0 with empty stderr. The final native stdout SHA-256 is `9e6ff62f6218da4265eb34cc2f5c1f41c7b839c20ce52ece30a8ae05919f2f2b`; other exact hashes/counts and original commands are in the receipt. Go 1.26.8 was used with offline/local toolchain settings.

The final native test actually executes producer -> accepted validator -> accepted readonly content review -> accepted preflight through Host/Store, using local controlled actors. It distinguishes base canonical B from rendered R, preserves the complete accepted review, confirms no Provider events/identity/exit code for preflight, and verifies its original callback active time (73,606 ms), exact cleanup (7,676,159 bytes), reservation release and owner acceptance. Before and after preflight/accept it compares the entire bounded named worktree and `.git` inventory through Lstat/nofollow/Fstat/final Lstat, including bytes, type, UID, nlink, modes, device/inode, size and mtime. Native root `native-2593866002` was removed only by the original fixture after known terminal proofs; ROOT independently confirmed absence.

The earlier native run at 3e2f241 also passed (451.98 seconds, SHA `56204c283ba04e85096b29bd86467478ac5ee70418982aa3bc1537cce94aa666`, root `native-2083495129` absent), but its five-path following-Stat oracle was insufficient for complete inventory preservation. It is retained with that limitation; the strengthened final run supersedes that claim. Both original sessions were drained. Test-only differences do not invalidate the unchanged production builds or the explicitly source-bound package.

## Preserved failures

Genuine pre-fix failures remain recorded: admission 8d79b24; live Store grant binder 305ea4c; late Stop after a real terminal slot 60e58de (1 top / 3 FAIL, SHA `7aeb3487462b39e73bb64a580c2210c36aed9b7cb3feced38307612d20cf06d3`); child Start and gitdir/info replacement 8e58aac (2 top / 6 FAIL / 2 PASS, SHA `3a821e9e97e32450db1dd4137b3c0b0861479a6004d332281a3a0ba2e295af1a`). Late-Stop failure roots with no processes are retained: `native-3233521085`, `native-1213850175` under this worktree's `tasks/filter-host-native/tmp`.

The separate index worker's raw files are preserved under `tasks/filter-target-index-guard/data`. All its commands ran against dirty f3ed8aa: API compile failure is not a business RED; two focused passes lack fixed-source qualification; old native attempts failed first for missing fixture parent, then a mutex deadlock, then a 180-second timeout after the deadlock fix. Their original tool sessions were not recoverable, so no cold/ps inference supplies original Wait/tree proof. Its roots `raw-3944552256` and `raw-1448222567` remain in `../filter-target-index-guard/tasks/filter-raw-fixture/tmp`; none was cleaned or relabeled passing. ROOT took over, added genuine fixed-source regressions and ran the qualified checks above.

## Scope and remaining work

This is Darwin arm64 actual Host/Git/Broker qualification with Claude-shaped controlled actors, not real model quality or a full public CLI/TUI run. AGY has admission coverage, no separate filtered native chain. Linux is compilation only; Intel Mac/Windows native remain deferred. A new forced-UNKNOWN process oracle and detailed-diagnostics execution for this process-free action were not added; existing UNKNOWN conservation is reused and independently reviewed. Detailed diagnostics can still be reserved by Store but this action does not run a diagnostics driver.

The [isolated package](../../closure-package-provenance/docs/filter-target-current-20261010.md) is bound to source 3e2f241, whose production files match this qualification. Its 12 commands, reproducible builds/packages, source closure and private install/replay/Doctor/uninstall were independently checked. No user installation was replaced and no hook was registered.

Target-bound approval review, the bounded Git-child filter bridge, normal filtered integration, compose/rework/retry/continuation, LFS and signing are not completed by this milestone. F10 recovery and F12 configuration-crash evidence are reused; F12 Formal60 remains FAILED. No real model/account, push or release occurred.
