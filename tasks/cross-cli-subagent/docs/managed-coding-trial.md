# Real-model trial driver

Use the receipt-backed installed wrapper and a confirmed provider lock. The main
session must have the user's explicit model/budget authorization. This driver
never logs gateway credentials or changes account/global CLI configuration.

From `tasks/cross-cli-subagent`, run all phases with the same absolute `--root` and original `--wrapper`:

```sh
python3 scripts/managed-coding-trial.py prepare --root /private/trial-new --wrapper /installed/version/scripts/invoke.sh --provider claude --provider-lock /private/confirmed-lock.json --model USER_SPECIFIED_MODEL --max-active-ms 120000 --max-attempts 3
python3 scripts/managed-coding-trial.py dispatch --root /private/trial-new --wrapper /installed/version/scripts/invoke.sh
python3 scripts/managed-coding-trial.py review --root /private/trial-new --wrapper /installed/version/scripts/invoke.sh
```

`prepare` defaults to **2 total attempts**. Pass `--max-attempts 3` only with explicit authorization for up to three total attempts (initial implementation, owner rework, and room for one technical failure). The flag accepts 1–3 and is frozen in dispatch; the driver never increases the budget on replay.

`dispatch` only submits and performs a bounded wait. `review` only collects the
bound artifact and saves `candidate-N.diff`, `candidate-N.py`, `candidate-N.json`,
`review-N.json`, and immutable `collect-N.json` with the candidate/source/artifact/diff hashes. Neither phase
executes candidate code. If the bounded wait has not produced a reviewable result,
inspect/wait using the retained handle and run `review` when ready.

Read the actual diff and source before validation. Then explicitly provide the
OID and source hash printed by review:

```sh
python3 scripts/managed-coding-trial.py validate --root /private/trial-new --wrapper /installed/version/scripts/invoke.sh --candidate-oid REVIEWED_OID --source-sha256 REVIEWED_SOURCE_SHA256
python3 scripts/managed-coding-trial.py request-changes --root /private/trial-new --wrapper /installed/version/scripts/invoke.sh
```

The first revision requests plain Python `a+b` with no input validation. Owner
feedback adds a new requirement: only exact `int`/`float`, with explicit
`TypeError` for bool/string/list/None and custom int/float subclasses. This is a new boundary requirement, not a
claim that the model made an error. Before sending feedback, the driver saves
`review-1-ack.json` from the exact revision-1 collection receipt and acknowledges
that complete reviewed page. This ACK means “read”, not business acceptance.
The collection's `payload_hash` is mapped explicitly to the ACK's `event_hash`;
extra actionable events cause a failure and require owner review. Feedback is
saved before either RPC; an uncertain
reply must be retried with the same phase/root, which reuses the exact saved
command ID and source binding even if current inspect has no reviewable event.
It first replays the saved revision-1 ACK, including when its earlier reply was lost.
Do not delete feedback or create another request to bypass uncertainty.

Wait/inspect the original handle, run `review` for revision 2, read its new diff,
then explicitly `validate` its printed OID/source hash. Finally run `accept`.
Acceptance requires successful hash-bound revision-2 validation, changed source,
correct candidate lineage, and the full observed event tuple; delivery ACK is a
separate request. `accept.json` and `ack.json` are immutable checkpoints, published
atomically with file/directory fsync before sending their respective commands.
After an uncertain accept reply, collection failure, or lost ACK reply, rerun the
same `accept` phase/root: the driver replays the exact saved acceptance, then the
saved ACK (or obtains and saves `accept-collect.json` if no final receipt exists).
It never reconstructs a decision from a completed summary, which no longer
projects a reviewable result. Failure in any RPC or final status check prevents
the completion report; retain all checkpoints and the original handle.
Original repository HEAD is checked against the prepared base before acceptance and again after accept/collect/ACK/final inspect; a changed HEAD prevents the success report.

Validation runs only on macOS with Seatbelt. A trusted Python bootstrap starts
with `-I -S -B`, a minimal credential-free environment and private HOME/TMPDIR;
it loads trusted runtime modules, installs the sandbox, and only then opens and
executes candidate validation material. Candidate-time file reads are restricted
to system/Python runtime and validation scratch, writes to scratch, with network,
Mach lookup, process metadata/sysctl, fork and exec denied. CPU/time/output limits
apply. Scratch is removed afterward. Unsupported sandbox platforms fail explicitly;
there is no unrestricted validation fallback. The trial report contains bounded
stdout/stderr, candidate OID and exact Git-blob source SHA256.
