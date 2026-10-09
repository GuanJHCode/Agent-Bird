# Bootstrap publication lease

This scoped milestone passed on macOS arm64 and offline Docker Linux arm64. Production is5089e31; static final review covers22414f8. It orders the same candidate's native/local rebind and recover-host operations without changing schema30 authorization, original Wait/tree, UNKNOWN, budget or immutable receipts.

The canonical state/launch lease covers original bootstrap read, bounded/cancellable Rebind RPC, exact receipt validation, FD-relative staged write/fsync/rename/directory-sync and postcheck. Root/recovery remain0700; a0500 bootstrap directory can still be read but cannot publish. Root/lock/file replacement or edited content fails closed. Recover-host takes this same lease before reading and holds it through final inspect, retaining its exclusive original startup-intent filename.

Evidence in data/final-receipt.json:

- Deterministic two-child CLI/fake RPC ordering: old code RED because G2 reached RPC while G1 was held; corrected fixture retains both original Wait0. New ordinary ordering plus filesystem guards:2top/6named PASS0F/S. The fake RPC proves CLI locking/order, not actual DB authorization.
- Existing actual public pending-auth rebind/0500 failure/exact replay, Host recovery and legacy refusal:3PASS. Actual permission denied occurs after DB commit; replay changes only the bootstrap and keeps the original receipt. Original Owner/Host/Coordinator Wait and worker Exited0/reaped are retained.
- Final Mac race and offline Linux each:3top/7named PASS0F/S, covering ordering, root/lock/content/cancel guards and the actual public chain. No whole Store repeat.
- Native attached-entry regression initially failed on both integration384491d and the lease branch. The test decoded v2 Owner recovery as legacy v1 Host recovery, so LastPID/Birth were always zero. The corrected test checks durable runtime_hosts tuples, ledger version2 and exact equality of all non-scope ledger fields;1PASS. Both initial failures remain saved. No production guard was relaxed.
- Go1.26.8 whole build and final cmd vet exit0. Independent reviewer approved production, bounded stage exact-byte check, source wiring, corrected native assertion and fixture evidence.

Linux used cached image6ab0b6e..., offline/nonroot/readonly/cap-drop/pids+memory+CPU limits. Container4aee2983... exited0/nonOOM with original attach0, then exact rm/independent ROOT inspect absent. An earlier containercf89345b... was only created: the runner output directory was initially made under the wrong cwd, receipt creation failed before docker start; its created state/removal evidence is retained, not counted as a test. ROOT checked all4 production source hashes, binary3145fb23... and raw log SHA, confirmed both full CIDs absent, and removed only the verified binary and empty owned directories. Raw receipts retain full hashes/CIDs.

This is cooperative writer ordering for one frozen candidate. Different versions/direct IPC and malicious same-UID ABA are not covered. DB and filesystem are not one atomic transaction; authenticated exact replay repairs post-commit publication failure. Pre-Bind lost original child handle remains unresolved. Intel/Windows native and F12's failed Formal60 remain unchanged. No real Provider/model/account, user installation replacement, push or release occurred.
