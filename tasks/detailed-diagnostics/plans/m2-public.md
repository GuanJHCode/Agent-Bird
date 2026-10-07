# M2 public diagnostics integration

Base229dc15; process observer integrated at12cbaee. ROOT alone writes integration, schema19 and public contracts.
M2a consent/config/admission and durable schema foundation are verified;
Claim/Launch reservation authority is verified in bb70c55 (M2b). Private Host
capture driver and Writer.Abort are verified in041d955; ordinary execution and
failure projection are verified in177769b. Managed Codex original-reader drain
and capture are verified in `tasks/diagnostic-codex-integration/docs/validation.md`.
Public read/ACK and M3 remain unimplemented.
See docs/m2a-validation.md and docs/m2b-validation.md.
The process output observer is independently implemented in isolated
`diagnostic-output-tap` at the same base. ROOT retains macOS/Docker native tokens.
All F01–F13 requirements remain active; this does not waive managed artifacts,
retention, physical-fault or flood/RSS acceptance.

## Admission and identities

- TaskRequest/TaskSpec carry optional immutable DiagnosticsPolicy. Omitted
  fields preserve exact legacy config-plan bytes. Explicit summary and detailed
  remain distinct request identities. Public null, duplicate, alias, unknown
  keys and invalid modes/versions are rejected before admission.
- Propagate the policy through every ConfigGenerationTaskUse conversion and
  both plan digest implementations. Guard-owned snapshots copy the policy;
  a caller mutation cannot change the already-pinned expected request.
- A new task_diagnostics_policies row stores explicit canonical body+hash;
  absent legacy rows authorize summary only. Never infer consent on migration.
- Producer is the stable HostLaunch/runtime Host identity. Claim resume keeps
  the same attempt but creates a new segment. One detailed reservation binds
  run/task/attempt/producer/policy and the exact M1 scope hash, never a mutable
  single segment. Retry gets a new attempt/reservation.
- Managed reservation ledger uses `(attempt,class,scope_key)` uniqueness.
  Diagnostics uses scope_key=`attempt`; terminal/progress will use their exact
  segment. Reservation identity/maximum size are immutable; deletion proof
  changes state, not the original grant. Diagnostics reserves20MiB+128KiB.
- DiagnosticsGrant extends the signed launch with the original reservation,
  policy hash and durable segment ordinal. Host derives a fixed-width ordinal
  plus actual source SegmentID for M1's strictly increasing segment identities.
  PendingLaunches must reconstruct the same grant and never reserve again.

## Read, replay and ACK

- Use run_control/controller/task authentication inside each Store transaction,
  with exact attempt/producer/policy/reservation membership checks. Data reads
  do not require execution-owner or Host liveness/current epoch.
- No existing business collection/decision ACK ledger is repurposed. Maintain
  one row per detailed attempt: monotonic next revision, one in-flight reading
  request or persisted pending envelope, and only the latest ACK receipt.
- Each delivery row starts idle, next revision1, with no request, pending page,
  ACK or cursor history. The first read derives a canonical zero cursor from
  the already bound Scope; it does not accept caller-provided initial history.
  SQL insert/update guards enforce initialization and phase/revision transitions.
- Before filesystem reading, transactionally fence the exact request ID,
  requested cursor and revision. Filesystem work then occurs outside the DB
  transaction/mutex. A second transaction revalidates and persists the full
  page before any response. A different request cannot replace reading/pending.
  An interrupted read may resume the same request; no uncommitted body has been
  exposed. The reading fence also prevents deletion racing file reads.
- Canonical envelope has a receipt hash over its body without its own hash.
  It binds attempt, revision, delivery ID, request ID, cursors and eviction gap.
  Full serialized bytes<=64KiB and raw payload<=32KiB. M1's page bounds alone
  are insufficient when escaped segment names inflate JSON; size must be checked
  before Store persistence and pagination must still make progress.
- Exact pending read replay returns identical bytes. ACK validates the complete
  receipt in one transaction, advances only this diagnostics cursor/revision,
  and saves the latest receipt for exact duplicate idempotency. Older ACK is
  stale; no execution status, decision, grant or report row changes.
- Read failures retain visible incomplete/unavailable state. Fenced failures
  require exact-request recovery/cleanup; absence of pending bytes is never
  proof that an in-flight read may be deleted.

## Capture and ownership

- Command.ObserveOutput observes borrowed stdout/stderr bytes outside process
  capture mutexes. Caller callback performs only bounded Capture.Offer and is
  thread-safe. Normal protocol downstream keeps its exact behavior.
- Duplex stdout is observed by its original Read, with no second reader,
  goroutine or stdin capture. Existing stderr-only Handle.Output is preserved.
- Host creates a capture queue and independent storage driver from the verified
  detailed grant. No raw recording under nil/default summary. Storage failure
  cannot stop pipe draining; it must enter original owned Stop/Wait/tree/drain
  handling and preserve incomplete diagnostics.
- Summary WriteMetadata failures must become visible. Do not replace original
  Wait/tree evidence with EOF, closed Capture or disappeared PID.

## Verification

TDD for public opt-in/digest drift and guard snapshots; real Submit/Claim/replay/
resume/retry reservation identities and schema18 migration rollback; separate
authorized read/ACK replay and no business mutation; concurrent read/deletion
fences; ordinary/structured/duplex output and default privacy. Native controlled
CLI must exercise the public path, including owner death, ACK loss and durable
storage-failure projection. M3 still has to migrate every managed artifact writer
and deletion/intake path before the complete2GiB allowance is claimed.


## Next grant integration hooks (read-only map, not implementation)

- LaunchCommand gets optional DiagnosticsGrant; grant adds positive SegmentOrdinal.
  Nil consent must preserve legacy command/config/candidate-intent bytes.
- Claim binds the durable attempt reservation before bindCandidateInput computes
  candidate intent; PendingLaunches reconstructs the original reservation and
  segments.segment_no before its candidate replay hash comparison. Resume uses
  the same attempt reservation and a strictly greater segment ordinal.
- CandidateIntentHash must commit nonnil grant alongside its current plan/payload/
  input and optional dependency vector, with exact existing nil-grant bytes.
  An omitempty optional extension can preserve nil bytes; verify by literal
  legacy and original replay fixtures rather than assuming a branch shape.
- Owner wire already signs the whole typed LaunchCommand; place grant validation
  before ValidateDependencyInputs' early return so ordinary launches are checked.
  Existing ordinary launch execution-identity hash stays unchanged; signed owner
  body and Store reconstruction bind the new nonnil content.
- Reuse candidate_input_test, dependency_inputs_test exact replay, continuation/
  resume_continuation and owner_runtime_wire tamper fixtures. Bind all reservation
  tuple fields and allowance in the same Store transaction; schema alone is not
  authorization. Canonical M1 segment ID uses fixed-width ordinal plus actual ID.


## M2b reservation implementation contract

ROOT owns integration Store and tests; contract worker owns its separate
`diagnostics-grant-contract` worktree at d8eb382. No shared writer/test resources.
Only new attempts may create a diagnostics reservation (20MiB+128KiB), and only
from explicit canonical detailed task policy. Resume/replay bind the original
attempt reservation, never create missing history or charge twice. Pure binding
at bindCandidateInputForDecision covers historical review/retry/legacy intent
reconstruction as well as dispatch. PendingLaunches binds before legacy grant
reconciliation. Dispatch allows only reserved/active; future released-history
proof policy must be separated from dispatch permission during M3.

Global allowance is2GiB in this state DB. Count every nonreleased reservation,
including other classes/legacy_pending, with overflow-safe sums. Disk admission
counts all nonreleased unmaterialized commitments (reserved-retained), the new
reservation, exact control-row metadata, SQLite write overhead and configured
minimum/critical disk reserves. Retained bytes may only be established by later
verified M3 materialization. No caller assertion releases that commitment.
Control-row accounting remains separate and transactional. Errors are
artifact_spool_full (logical allowance), storage_blocked (disk uncertainty/space)
and diagnostics_grant_invalid (invalid persisted authority/binding).

Before public Submit commits detailed tasks, validate run/task IDs against M1
Scope's exact length/UTF8/NUL restrictions. Summary/omitted preserve legacy
acceptance and bytes. Tests must cover real Submit/Claim/reopen/replay, stopped
resume same reservation/next ordinal, explicit retry new attempt/reservation,
rollback of execution/artifact/control rows, pending disk overcommit, corrupted
persisted tuples, candidate input/vector and historical retry intent paths.
This is still not full artifact-quota coverage until M3 migrates old writers.


## M2c driver boundary (not yet execution integration)

Fixed attempt root is `.diagnostics`, not `diagnostics`; both scanner levels
already skip hidden directories. Only the verified detailed grant and exact
Host producer may open it, through pinned FD chains with exact0700 children.
Writer.Abort discards dirty assembly and releases FDs without overwriting any
manifest/pending transition. Reader uses existing unclean/recovery semantics.
A Close error after its complete manifest was already committed is still an
error to propagate, but does not retroactively prove lost raw data.

The single execution owner chooses driver Finish only after original positive
Wait/tree/drain proof and after resolving summary metadata errors. Every earlier
return uses Abort. The first terminal choice is immutable; later calls join it,
and caller timeout never fabricates consumer termination. Async failure disables
Offer under its shared bounded state lock, notifies the owner once, and aborts
the writer. Ordinary execution uses its local cancelSegment; managed Codex must
use the local lease cancel in executeManagedCodex, which does not register the
ordinary segmentCancels entry. This remains required Host wiring, not completed
by the driver foundation. Full Writer syscall interruption is not claimed.
