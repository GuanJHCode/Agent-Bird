# F12 state-directory creation

Goal: create missing suffixes beneath an already-open ancestor without following a symlink introduced after ancestor observation.

Scope: `tools/orchestrator/internal/instance/**` only. Preserve the public `PrepareStateDir(string) error` API and existing rejection of non-private leaves. Existing ancestor aliases remain valid; existing directories are never chmodded.

Design: resolve/open the longest existing ancestor, retain its FD, then create/open each missing component through `openat`/`mkdirat` with `O_NOFOLLOW`. Re-stat the path/opened leaf identity before success. A test-only hook runs after opening the ancestor and before the first create.

TDD: first prove the hook can redirect old `MkdirAll` into an outside directory, then require the hardened implementation to reject without outside state. Cover legitimate aliases/new nested directories, leaf modes/types, symlink leaves, ancestor replacement, and close errors.
