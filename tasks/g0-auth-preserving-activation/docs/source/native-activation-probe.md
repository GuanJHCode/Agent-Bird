# Native activation probe

`native_activation_probe.py` is a bounded runner for one already-prepared
isolation profile. It uses the fixed `auth_isolation.py` context, writes a
0600 PID grant before releasing the pre-exec gate, and obtains PID birth and
executable metadata through `proxy_transport._process_metadata`. The gate
executes the selected executable with the caller's target arguments; an
ordinary probe uses an empty argument list, while a separate version smoke
may explicitly pass `--version`.

The probe always builds the child environment through `build_clean_environment`.
Grant files live below a separate owner-only supervisor root, outside every
profile writable root; the native child cannot rewrite its own admission.
It creates and removes only the new fake-home default socket alias pointing to
the approved public socket. Backend admission is checked with
`build_backend_launch` against the profile's private backend directory; this
module does not start a backend or call launchd. A caller may wrap `run()` in
the reviewed activation transaction.

Credential inventory input is an owner-only text file. Comment and blank lines
are ignored, all remaining entries must be absolute paths, and target bytes are
only hashed by `snapshot_auth_paths`. The report contains opaque changed path
IDs, auth unchanged state, bounded PTY byte count/digest, and a redacted
diagnostic excerpt. It contains no credential body or environment values.

The probe has two explicit modes: `version` accepts only `--version` and no
PTY commands; `activation` accepts an empty argv, requires `sandbox-exec`, an
immutable service readiness receipt, matching PTY `/status` thread UUID,
`/quit`, zero exit, and a final service cleanup receipt before transaction
revoke. The fake test uses the version mode and a PTY to prove
grant-before-release, default socket alias cleanup, private backend plan
construction, and unchanged synthetic auth metadata. Real native and
launchctl execution is intentionally left to the top-level transaction owner.
