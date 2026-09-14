# Native tripwire dyld bootstrap hypothesis

## Scope and status

This change tests one hypothesis about the bounded native tripwire failure: the
deny-default profile omitted the Apple rules needed to bootstrap a Mach-O
process through dyld. It adds the current system `dyld-support.sb` import and
the same narrow system-library executable mappings used by Apple's hardened
BlastDoor profile.

This is not evidence that the native binary now starts. No native process or
`sandbox-exec` command was run for this change. The earlier failure did not
produce a sandbox denial naming the blocked operation or path, so runtime
verification remains blocked until the separately authorized bounded
fake-only experiment.

## Local Apple profile evidence

The installed Apple profiles were read without executing a sandboxed process.
Their non-secret filesystem identity is:

| Path | SHA-256 | Birth time | Size |
| --- | --- | --- | ---: |
| `/System/Library/Sandbox/Profiles/dyld-support.sb` | `06215a5d32689aefe395c29710e182eb54ba22162f50df8b4842290f8a19bf1c` | `2026-02-25T11:41:32+0800` (`1771990892`) | 2,655 bytes |
| `/System/Library/Sandbox/Profiles/blastdoor.sb` | `54b014c2e3166014b22876b125bb78fabc49ef5fde2f6b52e18d99c58c4c5bd3` | `2026-02-25T11:41:32+0800` (`1771990892`) | 20,702 bytes |

`dyld-support.sb` labels itself as the rules required to bootstrap a process
with dyld. It permits the current Cryptex executable mappings,
`SYS_map_with_linking_np`, signature-related fcntl operations, and the root,
`openat`, `fstatat`, and `dup` operations documented for libignition.

The version-1, deny-default `blastdoor.sb` imports that profile and separately
allows `file-map-executable` only below `/System/Library` and `/usr/lib`. This
separation is material: `file-read*` does not provide the executable-mapping
operation under a deny-default policy.

Apple marks these profiles as private system interfaces that may change. The
hash and birth-time pin above therefore applies only to the inspected local
system version.

## Why `process-exec*` is not part of this change

The working native-product profile also uses unrestricted `process-exec*`, but
that profile differs in several dimensions and cannot isolate the cause. The
tripwire already permits `process-exec` for the exact admitted native binary.
Apple XNU's `load_dylinker()` loads `/usr/lib/dyld` as a vnode and parses it
inside the original Mach-O exec; it does not perform a second exec of dyld.
Consequently there is no first-party evidence that broadening this tripwire to
`process-exec*` would address the observed pre-libSystem stop.

The relevant first-party sources are:

- Apple XNU `load_dylinker()`:
  <https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/mach_loader.c#L2975-L3071>
- Apple dyld4 startup design:
  <https://github.com/apple-oss-distributions/dyld/blob/main/doc/dyld4.md>

## Static verification

The dedicated test renders a profile from inert path values and checks that:

- `dyld-support.sb` is imported;
- executable mapping is limited to `/System/Library` and `/usr/lib`;
- `process-exec` remains limited to the admitted native binary;
- no `process-exec*`, `mach-bootstrap`, or shared-region syscall allowance is
  introduced;
- the existing network default deny and securityd Mach deny remain present.

Run only the static test with:

```sh
python3 -m pytest -q tools/real-user-trial/test_native_auth_tripwire.py \
  -k strict_profile_adds_only_apple_dyld_bootstrap_permissions
```

This check does not compile SBPL and cannot establish launch behavior or
process cleanup. It does not read credentials or create fixture homes.

## Bounded app-server diagnostic

The bounded fake-only follow-up established that `--version` now completes and
that app-server exits before creating its Unix socket. That moves the failure
past dyld bootstrap, but does not identify a missing policy operation.

The fixture now retains at most the first 4,096 stderr bytes in memory while it
continues to hash and count the drained stream. On a pre-socket exit it erases
that prefix and exposes only a fixed JSON report containing the phase, exit
code, stderr byte count and digest, truncation status, a fixed category, and a
fixed errno name when one is recognized. Raw stderr, environment values, and
matched substrings are never included in the exception or written to disk.
The report also includes fixed booleans for `panicked_at`, `scproxy`,
`system_configuration`, `failed_to_create`, and `proxy`. Fatal/panic detection
takes precedence over the generic configuration category.

The fixed categories distinguish CLI usage, configuration, socket setup,
network setup, explicit sandbox diagnostics, native fatal errors, empty
stderr, and unclassified output. They are routing hints rather than proof of a
root cause. The next bounded experiment should publish only the JSON exception
message.

For the separately approved diagnostic run,
`CODEX_RUN_NATIVE_AUTH_DIAGNOSTIC_CONTEXT=1` adds at most 512 characters of
`stderr_context_sanitized`. Before every app-server launch, the fixture
rechecks that both `/etc/codex/config.toml` and
`/etc/codex/requirements.toml` are absent, all fixture paths are the expected
owned `0700` directories under `/private/tmp/g0-auth-*`, auth bytes still
exactly equal the generated synthetic fixture, the native SHA is fixed, and
the replacement child environment contains only the fixed minimal keys plus
one validated synthetic refresh override.

The in-memory sanitizer removes known synthetic values, URLs, absolute paths,
email addresses, JWT-shaped values, long token-shaped values, and sensitive
key-value values. It removes control characters, collapses whitespace, and
fails closed to `context_redaction_failed` if the bounded result still looks
like a path, URL, email, or long token. This retains the error-chain wording
needed to identify the startup phase without retaining or printing raw stderr.

Importing the version-3 `dyld-support.sb` does not supply a general application
profile. Apple's version-1 `blastdoor.sb` imports it and then separately adds
file-existence and metadata access, selected Mach lookups, self-process
operations, sysctl access, and a broad explicit syscall allowlist. This is
evidence that an app-server may require more than the dyld block; it does not
identify which permission this fixture needs. No BlastDoor rule should be
copied without a matching bounded diagnostic.

## CODEX_HOME ancestor traversal

The bounded sanitized diagnostic identified the next failure as
`canonicalize(CODEX_HOME)` returning `Operation not permitted`. Rust's Unix
implementation calls `realpath(3)`, which traverses and inspects every path
component. The fixture already grants the synthetic root as a subpath, but the
native binary's `/Users` ancestors do not cover the separate `/private/tmp`
fixture chain.

The profile therefore grants only `file-read-metadata` and
`file-test-existence` on the literal directories `/private` and
`/private/tmp`. Apple's current `system.sb` uses those same operations for
path-component and symlink resolution. Neither directory is granted as a
subpath, and network, process-exec, Mach, and real-home boundaries remain
unchanged.

## Optional system requirements probe

The next sanitized failure identified the fixed Unix system requirements
probe. Codex 0.154 sets `system_requirements_path` to
`/etc/codex/requirements.toml`, calls `read_file_text`, and continues only when
the result is `NotFound`. On this host `/etc` is a symlink to `/private/etc`,
and both logical and resolved `codex/requirements.toml` paths are absent.

The strict profile now grants `file-read-metadata` and
`file-test-existence` only on the four literal parent directories in those two
fixed paths. It grants `file-read*` and `file-test-existence` only on the two
literal requirements-file spellings, allowing the expected absent-file probe
to reach `NotFound`. It adds no `/etc` or `/private` subpath rule and does not
grant global metadata access.

Sanitized diagnostics map either fixed spelling to the public constant label
`SYSTEM_REQUIREMENTS`; arbitrary paths remain redacted.
