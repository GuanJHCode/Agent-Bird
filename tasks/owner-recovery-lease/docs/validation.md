# Existing-only recovery lease validation

OpenExistingOwnerRecoveryLease derives the spool path and expected scope from the Coordinator supplied durable binding. It opens only existing state/host-spool/launch directory, lock and ledger authority. It holds private directory, lock and record descriptors and validates current names, inode, bytes, mtime, canonical JSON, scope/identity and authority hash. There is no mkdir, O_CREAT, ledger persistence or recovery-budget consumption. Failed validation and Close permanently invalidate the lease.

Any OwnerPending blocks, including applied/activated/unknown. A coordinator reservation is admitted only in reserved phase; starting is unresolved and remains unchanged. Nil reservation, exhausted and fifth-slot records retain all budget, backoff and Unavailable fields. This permission to inspect and lock is not process death proof or an original child Wait receipt.

The initial missing-API run is compile RED. A later behavioral RED showed an identical-byte replacement ledger could pass by reusing the generic recovery reader. Holding the original record FD and initial FileInfo now rejects that substitution. Tests also reject restored authority after failure, late mutation, hardlinks, symlinks including ancestors, private-mode drift, malformed/canonical/size boundaries, scope/identity mismatch, unsafe launch path components and all missing components without creating anything. Multiple failed contenders do not release the original flock.

Final macOS arm64 ordinary and race each pass 35 top-level / 211 named tests, zero failure or skip (4.461s / 12.479s). Includes shared recovery budgets, publication, retirement and directory regressions. Go1.26.8 host vet and whole-module build exit0. Independent implementation review PASS; final additional boundary tests were self-checked, with production source unchanged after review.

Docker Linux arm64 runs the same 35/211 test set, zero failure or skip, in the pinned offline image. Exact CID 406208d474ac4acda751968d635df9ad14b34a4cd4a4f5d6ed476b2295d8c6f3 exited0, not OOM, removed and confirmed absent. Owned temporary binary removed only after its SHA256 matched the receipt. Sources, raw-log hashes and container evidence are in data/root-receipt.json.

No Provider/model/account, user installation, push or release was used. Coordinator callers, exact OS death classification, signed predecessor-to-successor scope transition and public CLI Resume remain subsequent work. This increment does not close F10/B3 or F01–F13.
