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
- `status`, `collect`, `ack`
- `resume`, `stop`

Every task operation requires the exact `controller_thread`; resume and stop
also require the latest `work_revision`. A missing binding is `blocked` rather
than guessed. Session startup is silent and never resumes interrupted work.
The skill does not use `${PLUGIN_DATA}` for production state, does not replace
the `codex` entry point, and does not define MCP or app configuration. Git
materialization/integration remains blocked until the source Host exposes the
G3 package; the plugin never executes those operations in the coordinator.

## Package and installer contract

`scripts/package-plugin.sh` builds a directory package from a final G1 binary:

```text
<out>/
  plugin/
    .codex-plugin/plugin.json
    skills/orchestrate/SKILL.md
    scripts/invoke.sh
    bin/codex-orchestrator
  package-manifest.json
```

The manifest records every packaged regular file's path, mode and SHA-256,
plus the explicit fact that Windows is unsupported. The script requires an
absolute binary and a new or empty absolute output directory;
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

The caller supplies `DataRoot` as the native/session data location. The
installer checks that it does not overlap the package destination and never
removes or rewrites it. Changed files are retained for inspection. These
interfaces operate on a temporary project-local destination in this delivery;
no real install or uninstall was performed.

## Version matrix and removal

Each installed version has its own immutable-by-convention directory and
receipt. An active task records a pin to the exact version it uses. Upgrade is
therefore additive: install the new version, switch the caller's pin, then
remove the old version only after it is unpinned and its files still match the
receipt. `Doctor` is the preflight check for a retained or drifted version.

There is no claim of a complete native/provider integration here. The package
is only a versioned local distribution boundary; native session identity,
resume qualification, and provider adapter behavior remain owned by their
existing G1/G2 contracts.

## Validation

The plugin manifest was checked with the bundled `validate_plugin.py`. The
installer tests cover two coexisting versions, active pins, changed files,
changed manifests, malformed pins, the shared OS lock, crash recovery after
the version-local manifest is gone, unknown nested objects, symlink rejection,
existing-directory mode preservation, doctor reporting, and preservation of a
native-data sentinel. The package script and installed wrapper were exercised
against temporary directories.
`scripts/measure-resources.sh` samples an explicit PID list once per second and
accepts only the original budget/scenario pairs: no-task or offline-callback/5
minutes, workers/10 minutes, and burst/20 minutes. It produces a private JSONL artifact. The long
measurement run remains deferred to the root-owned final binary validation; a
shortened run is not treated as acceptance evidence.
