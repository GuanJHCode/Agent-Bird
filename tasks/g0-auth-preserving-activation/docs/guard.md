# Auth isolation guard

`scripts/auth_isolation.py` is a launcher-side guard. It does not start Codex, call `launchctl`, call Keychain, or make network requests.

The caller creates a fresh short root under `/private/tmp` (for example `g0-auth-XXXXXX`) and passes `home`, `codex_home`, `workspace`, `public_socket`, and `backend_socket` below that root. `IsolationSpec.validate()` rejects non-absolute paths, protected-path overlap, and any public/private/default control socket whose UTF-8 path length is `>= 104` bytes. The long worktree path is suitable for code and reports, but not for a real macOS UDS.

`prepare_isolated_home()` creates owner-only directories and a minimal custom provider config with `cli_auth_credentials_store = "file"`, `model = "gpt-5.6-luna"`, `model_reasoning_effort = "medium"`, and `requires_openai_auth = false`. It refuses to overwrite an existing config. `write_synthetic_api_key_fixture()` is optional and accepts only a newline-free marker beginning with `synthetic-`; it writes a new `auth.json` with no OAuth token fields. No real credential is copied.

`build_clean_environment()` uses an allowlist for locale/terminal values, then sets explicit `HOME`, `CODEX_HOME`, `CODEX_SQLITE_HOME`, `PWD`, `TMPDIR`, a fixed system `PATH`, and an opaque profile lease marker. Auth-like names and every unlisted variable are removed. Peer PID/UID/start-time/executable-digest and profile lease admission remain the frontend's responsibility; an environment variable is not an identity proof.

The supervisor-facing interface is:

```python
context = load_isolation_context(manifest_path, opaque_profile_id)
plan = build_backend_launch(context, (expected_executable, "--synthetic"), private_socket)
```

`plan` contains `argv`, clean `env`, isolated `cwd`, private socket, expected executable, and rendered SBPL. It never spawns the command. The supervisor must compare its frozen executable digest and exact peer lease before invoking the returned plan; the profile marker is not an identity proof.

The manifest is accepted only when it carries a non-empty `protected_paths` write boundary, a non-empty `protected_read_paths` exact-file boundary, a 64-hex `expected_executable_sha256`, owner-only stable metadata, and non-overlapping per-profile roots. `private_socket` must remain in the profile's original `backend_socket.parent`; a sibling profile directory or public/default alias is rejected.

`snapshot_auth_paths()` reads bytes only to hash them. It does not parse, print, or return file content. `SnapshotReport.compare()` returns only `unchanged` and opaque `path_id` values. `write_private_manifest()` creates a new `0600` metadata manifest and refuses to overwrite an existing file.

`render_sandbox_profile()` emits a deny-default SBPL profile. It permits reads needed by a native runtime, writes only below the profile-owned home/codex/workspace/tmp/backend directories, denies reads for exact credential files and writes for entire supplied credential roots, denies bootstrap lookup of the installed `com.apple.SecurityServer` and `com.apple.securityd.systemkeychain` services, and denies IP networking. macOS SBPL's direct `(allow network-outbound (literal "..."))`, `(allow network-bind (literal "..."))`, and `(allow network-inbound (literal "..."))` rules are used for the public/private Unix paths. The tests prove backend bind/listen/accept/send, private Unix connect, foreign Unix rejection, PTY write, real IPv4 TCP/UDP rejection, optional IPv6 rejection, and a non-credential bootstrap lookup where both unsandboxed known services return success while both sandboxed lookups are denied. The launcher must still use the fixed executable digest and peer lease before passing either socket to a child; literal UDS rules constrain the tested endpoints, while peer/profile identity remains a launcher responsibility.

`tests/test_auth_isolation.py` is Python `unittest` and is always invoked with `python3 -B`. It uses only synthetic files under `/private/tmp`, verifies clean environment removal, private fixture modes, opaque snapshots, macOS socket-length rejection, protected write/rename/symlink/read denial, AF_UNIX allowance, and TCP denial. It does not launch Codex/native, login, Keychain, or launchd.
