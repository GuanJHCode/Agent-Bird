# Filtered target approval — final validation

Qualified production source: `64acf86ece72c6ecfb3e4d94ef2ebd73a31aea76`, orchestrator tree `8c27eecacc629b6db43f0233f35530e5b74c7646`. Combined delivery source `0218aacabbb1ed3f8696fd095a64c04310e83101` adds only the independently qualified pair native test to that Go tree; production is identical. ROOT verified the raw command metadata, original return files, JSON event counts, source trees and owned-root absence in [root-verification.json](../data/root-verification.json).

## Delivered behavior

Darwin `filter_target_review` consumes one accepted `filter_target_preflight` with the same manifest and target tuple. It uses a fresh AutoDirectory and pinned Claude/AGY readonly native reviewer. Caller prompts, continuation/retry/compose and arbitrary execution shapes are rejected. Host authenticates the complete original artifact, limits the worst-case serialized result before materialization, and constructs the bounded target context itself.

The result contains compact typed provenance references and a new target ReviewBinding. It preserves the original target-free content review and complete preflight artifact. A later integration implementation must authenticate both this result and its referenced original preflight; the new binding alone is insufficient. Filtered integrate remains closed.

The original source and live Broker materialize R, inspect the target and bind a private pair. Full target/candidate verification is followed by a joint pure descriptor/inventory check after all Git and journal callbacks. The candidate's named index and Git directory must still be the held originals. Close releases only the pair's added descriptors. Original Provider Wait/tree and Broker cleanup remain required before terminal success and Store owner acceptance. The Store predecessor bound is five only for the exact admitted new stage; a nil Provider exit settles failed and cannot be accepted.

## Evidence and review

| Check | Fixed source | Actual result |
|---|---|---|
| Host five-stage native race | `64acf86` | 1 top/1 named PASS, 858.32 s, exit 0, empty stderr |
| Pair callback native race | `b8308b9` | 1 top/1 named PASS, 433.73 s, exit 0, empty stderr |
| Source/strict DTO/budget race | `87ff4a0` | 2 top/27 named PASS |
| Admission/schema/Store/prompt/inventory race | `b1a863b` | 5 top/40 named PASS |
| Final pair pure race | `64acf86` | 4 top/4 named PASS |
| Whole Darwin and CGO0 Linux arm64 builds; affected vet | `64acf86` | All exit 0; empty streams |

Counts include parent test names; these passing batches have zero failures/skips. Earlier related-package vet at `89d14cc` remains applicable. No older long native chain was rerun. Full raw argv/environment/output/exit, genuine REDs and the corrected Store test oracle remain in [data](../data) and [historical progress](validation-progress.md). The unchanged production and separately qualified added test justify evidence reuse without repeating these tests on the documentation-only delivery.

Host stdout SHA256: `e68e865d9dd00aaa1be9fd625f3d22950480e0d5cf2574d953825cbe5b8a5ecd`. Pair stdout SHA256: `fa64e4d8b2a05d8730c4bfc0405402aeb461a427ec7e5b6e90caacf0b3e113fc`. Independent review passed the revised plan, adapter/contract/Store/DTO/prompt, Host lifecycle/oracle, production pair `0bd7a644`, and pair native oracle `eb4d4dc7`. Earlier review findings and failed runs were preserved and repaired, not reclassified as success.

The Host actor consumed exact Host context, read actual candidate R, and received permission denial for candidate and target writes. Its native structured approval differed from plain commentary. The full original preflight and separate result DTO/bindings were compared, followed by real cleanup/release/accept. Preflight retained the complete original bounded target inventory (active 72704 ms, cleanup 7676159 bytes). Target review permits only the new owned candidate admin subtree identified through nofollow forward/reverse links; its pre-existing `worktrees` parent may change size/mtime and nlink by zero or one. All other original entries remain exact. This is not a claim that the entire shared `.git` directory remains unchanged during materialization.

The pair oracle proves that a candidate-phase journal callback after target verification can invalidate the target while sequential verification returns, and that the joint tail/public pair rejects it. The callback is bound by command digest, not claimed to be a unique last ordinal. Named index replacement is also rejected. Both original Brokers reported `unknown=false`. ROOT checked both pair fixture roots and the Host fixture root absent.

## Limits and next boundary

The ROOT Host outer session `89040` was drained normally. The pair worker lost its outer exec session ID and cannot claim that outer session was drained. Its original runner `subprocess.run` returned and wrote exit 0, and inner original-process Wait/tree, Broker terminal and cleanup records were independently verified; no process scan, retry or speculative cleanup supplied that proof.

This is offline Claude-shaped actor evidence, not real model quality or a public CLI/TUI full-chain trial. AGY has admission/schema evidence; Linux is compile-only for this feature. The new native stage's 900000 ms fixture window is not performance qualification. Intel Mac/Windows native remain deferred; F12 Formal60 remains FAILED. F13 separately verifies the local package from the same production source; it does not enlarge these native claims. No real account/model call, user installation replacement, hook registration, push or release occurred.

Normal filtered integration still needs a Host-owned transition verifier and authenticated bridge from Git's filter helpers to the original live Broker. The readonly target/index guards cannot be reused unchanged across a legitimate merge. Preserve normal `git merge --ff-only --no-autostash --no-overwrite-ignore`, hooks/signing and original process ownership; do not substitute file overwrites, read-tree or update-ref.
