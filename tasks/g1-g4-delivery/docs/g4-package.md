# G4 project-local package

This delivery is a project-local Codex plugin and versioned package layout. It
does not install a marketplace entry, modify a global Codex directory, register
launchd, or perform login/authentication. No public license or publication is
asserted.

## Plugin command surface

`plugins/codex-orchestrator/.codex-plugin/plugin.json` is the plugin manifest
validated with the bundled `plugin-creator` validator. The
`skills/orchestrate/SKILL.md` skill is the user-facing entry point. It calls
only `${PLUGIN_ROOT}/scripts/invoke.sh`, whose allowlist is:

- `ensure-running`, `submit`
- `status`, `collect`, `wait-events`, `ack`
- `accept`, `answer`, `retry`, `resume`, `stop`
- `native-bridge`, `rebind-owner`
- `install`, `doctor`, `uninstall`, `pin`, `unpin`

Submission binds the exact `controller_thread`; every later task operation
requires the returned owner-only 0600 `control_file`, and resume/stop also
require the latest `work_revision`. A missing binding is `blocked` rather than
guessed. Session startup is silent and never resumes interrupted work.
The skill does not use `${PLUGIN_DATA}` for production state, does not replace
the `codex` entry point, and does not define MCP or app configuration. Git
composition, materialization, integration, and cleanup run through the source
Host's internal `gitops-worker`; the coordinator never executes Git directly.

## Package and installer contract

`scripts/package-plugin.sh` builds a directory package from a final G1 binary:

```text
<out>/
  plugin/
      .codex-plugin/plugin.json
      skills/orchestrate/SKILL.md
      scripts/invoke.sh
      scripts/native-product-bridge.py
      lib/native_bridge/
      runtime/g0/
        runtime-manifest.json
        <11 pinned service/transport modules>
      bin/codex-orchestrator
  package-manifest.json
```

The manifest records every packaged regular file's path, mode and SHA-256,
plus the explicit fact that Windows is unsupported. The script requires an
absolute binary, an absolute pinned Python interpreter, and a new or empty absolute output directory;
the Go installer additionally enforces the strict `MAJOR.MINOR.PATCH` version
form (with an optional prerelease suffix).

The Go API in `tools/orchestrator/internal/install` is the integration point
for G1:

- `InstallPackage` copies the plugin and binary into
  `destination/versions/<version>` and writes a receipt whose hash covers the
  install manifest. Versions coexist, and a duplicate version is rejected.
- `PinVersion` and `UnpinVersion` maintain task pins. Install, pin, unpin, and
  uninstall share a persistent `.install.lock` file guarded by kernel
  `flock`; a crashed process releases the lock without leaving a stale flag.
- `UninstallPackage` refuses a pinned, malformed, changed, or unknown version.
  A changed file, malformed pin, symlinked parent, or unknown nested file is
  retained and reported; it is never swept by a recursive `RemoveAll`.
  Only manifest-listed regular files with the expected mode and digest are
  eligible for removal. Each item is moved through a private 0700 quarantine,
  verified by inode/content through held directory descriptors, then removed
  with `unlinkat`. The uninstall intent embeds the complete verified manifest
  and is synced before removal, so losing the version-local manifest mid-crash
  does not make recovery impossible. The receipt and intent are removed only
  after the owned tree is gone.
- `Doctor` reports versions, pins, manifest/hash drift, and file drift without
  modifying the native data root.

The packaged `runtime/g0/runtime-manifest.json` fixes the complete 11-file G0
service/transport closure by logical ID, relative path, mode and SHA-256. It
also allowlists one existing absolute Python interpreter by SHA-256. Packaging,
install and `Doctor` validate this manifest; they never copy or chmod the
interpreter. A missing or changed interpreter is reported as
`runtime_interpreter`, and the native bridge never falls back to source files
under the development repository. Its request binds this installed runtime
manifest to the same attached owner lease and activation service manifest.

The caller supplies `DataRoot` as the native/session data location. The
installer checks that it does not overlap the package destination and never
removes or rewrites it. Changed files are retained for inspection. rc6 was
installed only into a temporary project-local destination and passed `Doctor`.
After native-product-10, its task pin was released automatically; a fresh
healthy `Doctor` preceded successful removal of temporary rc4, rc5, and rc6.
The installation now retains only rc3 and its `native-product-task` pin for an
older recoverable failure. That pin was not forced. No permanent user
installation, plugin registration, or service activation was performed.

## Version matrix and removal

Each installed version has its own immutable-by-convention directory and
receipt. `PinRunningVersion` derives the package root and version from the
exact installed executable, verifies its manifest/receipt/files, and writes an
idempotent durable task pin before the control plane makes work executable. An
ambiguous database commit retains the pin for restart reconciliation. The
control plane may call `UnpinRunningVersion` only after it proves the task has
no active, unknown, explicitly recoverable, open-question, or pending-delivery
reference. Upgrade is additive: install the new version and remove the old
version only after every such pin is released and its files still match the
receipt. `Doctor` is the preflight check for a retained or drifted version.

The package does not bootstrap launchd, change Codex settings, or create an
owner lease. Native delivery requires an already attached service capability
whose service manifest binds the same installed `runtime/g0`; provider launch,
resume qualification and task state remain governed by the G1/G2 contracts.

## Validation

The rc6 pinned build, package, temporary install, pre/post-native `Doctor`, and
plugin validation are archived in
[`package-rc6-verification.json`](../data/package-rc6-verification.json). The
installed binary SHA-256 is
`f258ab25f5fd8eafa8e08fddc28d630bb342378f7c9fa65cd8c259d6da8b28c4`;
the installed manifest SHA-256 is
`5583bc69b4b761e6f36ca2848029ae7b12860a1e2bc772ebcd17451075bd7992`.
The [`rc6 archive receipt`](../data/release/rc6-archive-receipt.json) binds the
6,810,308-byte `codex-orchestrator-0.1.0-rc6.tar.gz` archive to SHA-256
`cc0043af6900a3c917a72931371be918e37897a5764c020f2f052de2b4cf1e41`;
its package manifest SHA-256 is
`40c190c8937367b83412e1cfa8e6f3e069d3169924b8b02ac6d3b39007a6d8e9`.
The rc6 source package, receipt, and tar remain under `data/release` as release
artifacts after uninstall; their retention is not an installed-version claim.

The complete project-local native product case is
[`native-product-10`](../data/cases/native-product-10/acceptance.json) and is
`pass`: auth14 remained unchanged, the Source Host stopped when the original
owner exited, the same native profile/thread resumed with zero turns under a new owner,
explicit rebind/resume completed, and delivery ended with a typed ACK. The
independent [`root-postflight.json`](../data/cases/native-product-10/root-postflight.json)
confirmed all 15 recorded process identities gone and the public socket absent.
The case used no external model calls and no real launchctl registration.
Its two same-timeline one-Host samples were 120.421875 MiB and 122.9375 MiB,
within the case's 128 MiB check; these frames are not a sustained joint G0/G1
resource benchmark.

rc6 also includes bounded streamed-artifact hashing, a 300-second bootstrap
timeout followed by watching until terminal state or owner detachment, and continuous draining/bounded discard for ordinary raw or
unknown provider diagnostics. Strict structured terminal/result/question
schema failures continue to fail closed.

The installer tests cover two coexisting versions, active pins, changed files,
changed manifests, malformed pins, the shared OS lock, crash recovery after
the version-local manifest is gone, unknown nested objects, symlink rejection,
existing-directory mode preservation, doctor reporting, and preservation of a
native-data sentinel. The package script and installed wrapper were exercised
against temporary directories.
[`resource-acceptance.py`](../scripts/resource-acceptance.py) runs the frozen binary with explicit synthetic
loads, process-tree sampling and durable report checks. It retains the original
5/10/20 minute budgets and writes private scenario evidence. The full-duration
resource suite remains scoped to G1 core: idle, callback, the 600-second hosts
scenario, and both 20-minute burst scenarios have passed. The results and reuse
basis are archived in [`resource-acceptance-summary.json`](../data/resource-acceptance-summary.json). No sustained
full G0/G1 joint resource benchmark was run. A shortened smoke is not treated
as acceptance evidence, and these scoped results cannot be written as the
whole distribution meeting a resource target.

Permanent plugin/service deployment is outside this project-local G4 contract.
It is an optional follow-up that first requires a separately reviewed production
admission contract for the existing native profile/backend and a verified
plugin/service deployment method; no permanent authorization is requested here.
