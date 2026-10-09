# F03 new base implementation evidence

## Store milestone (2026-10-09)

- schema33 immutable `new_base_lineage` records one successor per source integration tuple and command/request digest. No raw source/control tokens are retained.
- Read-only source gate verifies managed Host artifact files before any write transaction. Admission repeats source ownership, latest accepted events, payloads, settled budgets, exact captures, launch intents and immutable artifact refs within the owned physical transaction.
- Source support: complete single integration ancestor DAG, one validation/review closure, one optional v2 ordered composition and roots. No fallback, session, indexed review, config-generation, pre-integration rework or revised budget lineage. Explicit unsupported errors preserve these safety boundaries.
- Every `Directory` remains exact. Producers and validation/review must already opt into `AutoDirectory`; composition already uses automatic private worktrees. Integrated target/ref stays exact. Worktree/inode identity is revalidated by existing Host/Git paths.
- New run/tasks/launch/control/groups are independent; DAG/pins/paths/policies remain mechanically equal, integrated TargetHEAD becomes base. Only newly added retained adapter context/brief bytes are charged by byte-exact marshaled growth, plus exact lineage column bytes (two int64 scalars = 16 bytes). The unchanged ordinary-submit baseline accounting is preserved. A SQL column-byte oracle and per-task adapter delta independently check trigger and reservation equality.
- Ordinary submitted contexts remain rejected. F10/schema32 historical downgrade fixtures accept empty schema33 and refuse erasing real lineage.

Raw named Store evidence: `data/store-red.*` (expected failing behavior before implementation) and `data/store-green-03.*` (15 named passes, 0 failures/skips; 4 top-level tests). Diagnostics/failures remain under distinct filenames. No entire Store suite was run.

Public CLI/Host/Git milestone remains in progress. ROOT owns independent review and integration. No real provider/model/account, installation replacement, network pull, push or release.

## Review corrections / durable target reservation

Independent c9d01ba review found and implementation corrected:

1. Producer `CandidateArtifact` hashes its full receipt before omitting `Files`; source proof now compares the authenticated delivery receipt digest to ordered manifest pins, while checking digest format. Real public chain reproductions exposed this mismatch; all failed raw records are retained.
2. Execution owner mode is included explicitly in canonical admission identity despite `HostLaunchSpec`'s `json:"-"` field. Mode-only replay must conflict.
3. A schema33 target reservation lives from admission through the whole new DAG. Active target keys are unique. Ordinary submission/dispatch and old DAG rework admission respect the held target; distinct refs remain independent. Different matching-ref paths with no durable proof of disjoint common repositories defer with a scope reason. The reservation is immutable except a proved `reserved -> released` transition, with exact column-byte accounting and fixed 30-byte timestamps.
4. Source accepted completion releases only with current accepted/exited/settled proof; other terminal release uses existing `releasableTaskPredicate`, retaining UNKNOWN, retry/resume and ancestor holds. Release participates in Host-event, accept and queued-cancel transactions. It can also be rechecked during later admission.
5. Competing legacy integration scopes read artifacts only in the pre-admission read-only gate. The owned write transaction recomputes exact durable reservation facts and rejects missing/changed scope snapshots; it never calls `integrationScope`/file reading in the F03 admission path.

`store-reservation-01`: 2 top-level / 14 named PASS. `store-reservation-02`: concurrent two distinct source-event submissions yield one successor + one target refusal; ordinary matching target admission/dispatch refused, distinct ref allowed, UNKNOWN cannot release and queued cancel releases. 1 top-level / 1 named PASS. Final race/build/vet and public three-generation verification are still pending.
