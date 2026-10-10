# F13 target-review package evidence closure

This report closes only the retained package evidence at
`data/target-review-current-20261010/`. It records ROOT's completed
root-verification and cleanup results without rerunning a build, package,
installation, test, or cleanup operation.

## Exact package subject

The receipt identifies source
`64acf86ece72c6ecfb3e4d94ef2ebd73a31aea76`, repository tree
`87ba4b619a587d116f6f0946043491de1b30ebc2`, and orchestrator tree
`8c27eecacc629b6db43f0233f35530e5b74c7646`. It packages version
`0.1.0-closure.64acf86ece72` for macOS arm64 using the fixed Go 1.26.8 and
Python 3.14.3 paths recorded in `receipt.json`. The receipt hash is
`bd564da6be6c7de5b2e42133e203e07ffad249a7a3b0ca70cc2aeff68ce25da4`.

Both independent builds produced binary SHA-256
`39179ac48bb5cece4b9247e927586577b19e1831248cd11005df440220f85f7e`.
The retained package manifest SHA-256 is
`f0bacd12c96b5281440c52c0797aa263127bbd2e7d53058c6b40e01488072827`.
`root-verification.json` records 12 raw commands, all exit 0 with empty
stderr, 28 package files, 24 source mappings, 27 installed payload files,
and a drained original runner session `53854`.

## Root verification and exact cleanup

ROOT independently verified the receipt, builds, package byte/mode equality,
source closure, task-private install/replay, Doctor results, owned uninstall,
and the runtime-data sentinel. The cleanup intent then nofollow-rechecked the
listed identities and hashes, retained `package-a` and runtime-data, and
removed only `orchestrator-a`, `orchestrator-b`, and `package-b`.
`cleanup-result.json` records 30 files, 13 directories, and 67,324,236 bytes
removed; every removed path is absent, package-a was reverified, and the
sentinel remains preserved. No package artifact, temporary tree, or binary is
added by this evidence commit.

## Native evidence and limits

The package receipt does not itself qualify later source changes or native
behavior. Separately, ROOT reports the Host source `64acf86` native oracle as
one passing test in 858.32 seconds. The pair oracle used runner source
`b8308b9c9eb0407f17e30ed4b8aac84d8b5e7d7a` and passed once in 433.73
seconds; its outer exec session identifier was not retained, although its
runner wrote a normal terminal result. These native records do not alter the
package source identity above.

This scope does not establish real-model or provider behavior, user
installation, hook registration, publishing, pushing, release, cross-platform
support, or resource/Formal60 eligibility.
