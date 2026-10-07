# Linux profile admission

Baseline: `92e4b49ab724916445816a84d4f28bc576bf671e`.

## Contract and ownership

Linux Provider execution and profile sandbox remain unsupported. The existing
`profile_sandbox_unsupported` platform rejection must precede Claude/AGY history
preparation in `Host.sandboxProfileCommand`. Rejected calls return the original
command and do not create history, metadata, scratch, or alter fixture files.
This is a local wrapper contract, not a claim that all Host launch admission is
free of side effects or that Linux Provider execution is qualified.

Only `tools/orchestrator/internal/host/profile.go`, the new
`profile_linux_admission_test.go`, and this task directory are owned here. No
Store, Coordinator, contract, process, or history algorithm changes; no owner
handoff dependency. Other worktrees and containers belong to their owners.

## Steps

1. Add Linux-only real-wrapper tests using private Claude/AGY filesystem
   fixtures; no Provider, subprocess, installation, or account access.
2. Cross-compile the complete standard host package with pinned Go 1.26.8.
   Run the new selector in the fixed offline Linux arm64 image and retain actual
   behavior RED, including exact test counts and container state.
3. Add the minimal existing platform guard before history preparation.
4. Repeat the same Linux tests GREEN; run focused ordinary Darwin command/history
   regressions and host vet, avoiding native actor tests.
5. Record source/binary hashes, raw output, P/F/S, precise container IDs,
   ExitCode/OOMKilled, exact removal and inspect-absent evidence. Self-review,
   scoped commit, and return ownership to ROOT for independent review.

## Container boundary

Fixed image: `sha256:6ab0b6e7381779332f97b8ca76193e45b0756f38d4c0dcda72dbb3c32061ab99`.
Every stage uses a unique `root-linux-profile-<stage>` name, `--pull=never`,
`--network none`, `--read-only`, `--cap-drop ALL`, `no-new-privileges`, nonroot
UID/GID, `--init`, tmpfs `/tmp`, and a read-only stage-specific binary mount.
Only this task's saved exact CID may be removed. Binary build output is local
and ignored; hashes and logs are retained as task evidence. No physical power
loss, full native Provider, race, or full-package runtime qualification claimed.
