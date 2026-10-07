# Durable adjacent recovery-scope handoff

Scope: Store-derived current successor and immediate retired predecessor only. No public path, predecessor or authorization assertions. Preserve the entire canonical recovery record except ScopeSHA256; an immutable intent and irreversible precharged applied slot bridge DB → filesystem CAS → DB applied. Hashes include the canonical trailing newline. Existing FS lease remains held throughout.

ROOT owns this worktree, schema27, Store APIs, shared recovery hash, tests and evidence. The separate FS worker owns only its handoff files. No providers/accounts/install/push/release.

1. Move the unchanged v2 recovery hash into contract, retaining Host wrapper; freeze compatibility.
2. Add schema27 intent table with a precharged applied slot, exact relationship constraints, immutable history and UTF8 quota; test migration rollback and historical preservation. Do not fabricate receipts for historical successors.
3. Add authenticated Store context/prepare/applied APIs with owned transactions; gate successor Applied/Activate on durable recovery receipt. Immutable replay precedes mutable gates where compatible with current authentication.
4. Independently review, run focused/full Store, race, Linux and integrate stable snapshot.
5. Later private signed RPC and native runtime tests will hold the FS lease and connect the pieces. Bound-but-not-Applied death, preBind lost original child handle, and post-authorization origin rebind remain separate open contracts.

Review decision: Prepare pays all TEXT and four integer fields once. Apply only changes applied 0→1, avoiding a quota failure after FS CAS. OwnerID is the unique durable handoff key; authorization and predecessor are separately UNIQUE. No redundant caller-selected handoff/request ID. Both original binding hashes and exact control revisions are stored and constrained.
