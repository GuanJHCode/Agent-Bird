# Owner succession schema25 validation

Scope: immutable authorization/history and migration/accounting boundaries.
This does not implement public Resume, process death observation, successor
Prepare, recovery-scope transition or the intended pre-Bind crash window.
ROOT was the sole writer and test/container executor; independent review PASS.

The migration retains execution_owners and all existing child foreign keys.
It adds one immutable authorization ledger and a nullable unique owner reference,
freezes original owner identity/history and control permits, and preserves the
existing single-write Applied acknowledgement. A composite FK retains the exact
predecessor control revision. Current DB epoch may exceed the dead predecessor's
permit epoch; the predecessor and its permit must agree. Authorization requires
the exact queued task/revision/ready sequence. This DDL is defense in depth, not
an alternative to authenticated Store APIs.

Accounting is 64 fixed bytes plus the UTF-8 bytes of 20 ledger fields. A non-NULL
owner reference adds 8 plus its UTF-8 bytes; historical NULL references add zero.
Tests compute persisted byte lengths independently. Unsupported historical
retirement is rejected without synthesizing authorization. Migration fault hooks
cover validated/objects/triggers/version/commit and retain schema24 on rollback.

Behavioral RED evidence includes unbacked retirement/intended release, four
review boundaries (15 named failures), and mutable control history (five child
failures). The unused-import compile error is separate from behavioral RED. The early race's only failure was the old fixture's hardcoded
expected schema24; the assertion now follows currentSchemaVersion and still
checks history/accounting. All earlier logs are retained.

Final ordinary focused test: 20 top-level / 69 named PASS, no fail/skip (8.299s).
Final full Store: 472 top-level / 1340 named PASS, no fail/skip (196.886s).
Go1.26.8 vet and whole-module build exited zero. Final focused race: 20 top-level / 69 named PASS, no fail/skip (324.527s),
recorded in root-schema25-final-race.jsonl.
Linux arm64 on the pinned offline Docker image: the same 20 / 69 PASS,
no fail/skip; original CID e1568fa235fe2bd54133aead7068a49d0b03bb46b7daf9a74669d68453a67f27
exited0, not OOM, exact removal and post-removal absence recorded.

No real Provider/model/account was used. Linux Store acceptance does not establish
Linux Provider sandbox or Intel/Windows native qualification. The broader F10
and F01–F13 obligations remain open.
