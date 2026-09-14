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
