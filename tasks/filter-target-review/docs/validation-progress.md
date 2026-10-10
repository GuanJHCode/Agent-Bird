# Filtered target approval — validation in progress

Host native has passed; overall delivery still awaits the independent pair native result and integration. Integration remains 7142a32. The new stage consumes exactly one accepted preflight with the same manifest/target, runs a fresh pinned Claude/AGY readonly reviewer, and emits bounded typed provenance references plus a new target ReviewBinding. Old content review remains target-free; preflight remains observation. Filtered integrate remains closed. No real model/account, installation replacement, hook registration, push or release.

Plan d6a7cd1 independently passed after replacing recursive artifacts with compact references, explicitly bounding the worst-case JSON summary, and requiring a joint live verifier. Adapter/contract/Store/source/DTO/prompt production through dd22539 independently passed. Host lifecycle and the b1a863b native oracle have no remaining independent findings. Pair 43ed874's final named Git directory recheck was repaired by 0bd7a644 and independently passed. Its four focused race tests passed at ROOT source64acf86. The separate pair native fixture eb4d4dc7 independently passed review and is running in its isolated worktree.

## Current ROOT evidence

All commands below have a clean source, fixed Go 1.26.8, original terminal result and empty stderr. Raw argv/environment/source/stdout/stderr/exit are under `data/`.

| Evidence | Source | Actual outcome | stdout SHA256 |
|---|---|---|---|
| admission RED | c44a9d3 | 1 top; 7 pass, 3 fail; exit 1 | f05db07ce81e512e1dac348d242d79a852b3546b31e824e80d20c103ce6b56e0 |
| source race | 87ff4a0 | 2 top; 27 pass, 0 fail/skip | e356aa7f360f76035a3424a4ffc27e222471adefc878179a5cbb27ff975c9ac0 |
| schema + Store RED | 05a4591 | 2 top; 4 fail; exit 1 | 16141806677d247100d9357d7b87bd5a33c2efaff0fde026d3374db653d9df49 |
| schema + Store race | 9403c4c | schema 3 pass; Store 1 fail; whole exit 1 | 8c76e17cc0e529171f3c13508e1f486f7a591390aa8dd9407092a094f7dd5a00 |
| Store oracle failure | fb413de | 1 top fail; exit 1 | ddd4323005c17b41994955851c49bba2f2255904029f6502ee16c123086c3f4c |
| focused race | b1a863b | 5 top; 40 pass, 0 fail/skip | e56f752a581e01b9168318b628a7581a4991b32f7088e0716dbedac784b2ac68 |
| Mac whole build, Linux CGO0 arm64 whole build, related vet | 89d14cc | all exit 0; empty stdout/stderr | e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 |

Named counts include parents. The schema RED proved the new action lacked the native review schema; 9403c4c repaired it. Store first rejected the fifth task because legacy predecessor traversal was bounded at four; fb413de gives only the strict admitted new stage a bound of five. The next Store failure was an incorrect test oracle: the established contract accepts a nil-exit terminal event but settles `failed`; it need not reject the event transaction. 2da9736 tests that actual contract with independent success/failure branches and proves failed approval cannot be owner-accepted. No production exit-success guard was weakened. b1a863b's final focused batch passes both branches.

## Native qualification

`TestFilterTargetReviewNativeHostAcceptedPreflight` extends the existing controlled chain once, without executing the older native top-level tests. It must independently compare the full result DTO and exact actor context, read R, receive permission denial when writing candidate and target, preserve the complete accepted preflight artifact, obtain the original process/Wait/tree and cleanup events, and succeed through Store owner acceptance. The fixture permits exactly the fresh candidate's `.git/worktrees/<child>` registration identified by nofollow forward/reverse links; every original entry stays exact except that parent directory's size/mtime and zero-or-one link-count increment. This is not a claim that the whole shared Git directory is unchanged.

7226f4d gives only the new approval fixture a 900000ms grant/profile deadline and a 20-minute enclosing context. Use Go test `-timeout 25m`. This is a bounded acceptance window, not a performance or F12 resource qualification. ROOT Host native completed at clean64acf86: 1 top/1 named PASS, 0 fail/skip, 858.32 seconds, exit0 and empty stderr, stdout SHA256 `e68e865d9dd00aaa1be9fd625f3d22950480e0d5cf2574d953825cbe5b8a5ecd`. Original tool session89040 was drained. ROOT independently confirmed owned fixture `native-612680898` absent. Its preflight portion measured active72704ms/cleanup7676159bytes and passed the complete immutable target inventory check; target review permits only the documented fresh admin registration. Final Mac/Linux whole builds and affected vet at the same64acf source all exited0 with empty streams. Pair candidate-callback native qualification is still running in its independent worktree; its outer exec session ID was lost by the worker and cannot be claimed drained. ROOT will directly verify the original runner subprocess return evidence and inner original-process/Broker records, and keep this outer tool-record limitation explicit. Linux is build-only; AGY has admission/schema-only evidence. F12 Formal60 remains FAILED and Intel/Windows native remain deferred. F13's source3e package does not cover this new draft.

Final pair pure race at clean64acf86: 4 top/4 named PASS, stdout SHA256 `29c7d31b6c708a97d1164d6b461a2d9b253f329b99f8640b89eb37e8975ea201`, exit0/stderr0. This covers detached owners, descriptor-only Close, named index replacement and named admin directory replacement.
