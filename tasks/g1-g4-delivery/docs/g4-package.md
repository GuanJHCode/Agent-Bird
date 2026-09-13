# G4 project-local package

This delivery is a project-local Codex plugin and versioned package layout. It
does not install a marketplace entry, modify a global Codex directory, register
launchd, or perform login/authentication. No public license or publication is
asserted.

## Plugin and hooks

`plugins/codex-orchestrator/.codex-plugin/plugin.json` is the plugin manifest
validated with the bundled `plugin-creator` validator. The default
`hooks/hooks.json` registers two quiet command hooks:

- `SessionStart` appends fixed control metadata and reports whether a local
  `pending.json` marker exists. It never resumes a task or selects a thread.
- `SessionEnd` appends fixed control metadata. It is auxiliary and cannot
  prove that an arbitrary crash or transport disconnect was observed.

Both hooks use `${PLUGIN_ROOT}` and `${PLUGIN_DATA}` supplied by the host. They
do not read or persist inherited environment values, credentials, prompts, or
raw provider events.

## Package and installer contract

`scripts/package-plugin.sh` builds a directory package from a final G1 binary:

```text
<out>/
  bin/codex-orchestrator
  plugins/codex-orchestrator/...
  package-manifest.json
```

The manifest records the package version, binary SHA-256, plugin path, and the
explicit fact that Windows is unsupported. The script requires absolute paths;
the Go installer additionally enforces the strict `MAJOR.MINOR.PATCH` version
form (with an optional prerelease suffix).

The Go API in `tools/orchestrator/internal/install` is the integration point
for G1:

- `InstallPackage` copies the plugin and binary into
  `destination/versions/<version>` and writes a receipt whose hash covers the
  install manifest. Versions coexist, and a duplicate version is rejected.
- `PinVersion` and `UnpinVersion` maintain task pins. Install, pin, unpin, and
  uninstall share the exclusive `.install.lock` transaction lock.
- `UninstallPackage` refuses a pinned, malformed, changed, or unknown version.
  A changed file, malformed pin, symlinked parent, or unknown nested file is
  retained and reported; it is never swept by a recursive `RemoveAll`.
  Only manifest-listed regular files with the expected mode and digest are
  eligible for removal, followed by empty directories. The final receipt is
  removed only after that owned tree is gone.
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
changed manifests, malformed pins, the shared lock, unknown nested manifests,
doctor reporting, and preservation of a native-data sentinel. The package
script and both hook scripts were exercised against temporary directories.
The resource protocol's long measurement run is intentionally deferred to the
root-owned final binary validation.
