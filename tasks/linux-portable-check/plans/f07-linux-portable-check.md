# F07 Linux portable check

## Frozen scope

Linux may install only a `collect-only-hooks` portable package and run only
`plugin-run ... -- check`. It does not advertise or enable a Linux provider,
sandbox, runtime, or any other portable action. Darwin retains its current
portable action policy. All other platforms are rejected.

## Design and acceptance

1. A pure platform/action policy is evaluated before state resolution or an
   installation call. This makes rejected Linux actions unable to create an
   installation.
2. The Linux branch uses an install-only entry point that requires
   `payload_schema: 1`, validates the closed `collect-only-hooks` payload, and
   completes the existing immutable-version, pin, and Doctor lifecycle.
3. Unit tests cover the policy without treating a runtime `GOOS` override as
   Linux-native evidence. The root phase owns Docker execution.
4. The Docker handoff script uses the repository's fixed offline, read-only,
   `nobody`, capability-dropped container pattern and writes evidence only in
   this task's `data/linux` directory when invoked by the root phase.
