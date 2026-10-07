# Host artifact slots: dispatch through retained references

Base `2a167a7`; ROOT is the sole integration writer and native/Docker owner.
This continues M3 in `../../managed-artifact-intake/plans/implementation.md`;
F01–F13, legacy accounting, deletion and full physical/flood acceptance remain
required. Host positive fixtures were delegated to an isolated worktree; ROOT integrates and
verifies. Independent review remains read-only in the integration worktree.

## Frozen decisions

- Each newly dispatched segment reserves one terminal slot of1MiB and21 progress
  slots of64KiB, including resume segments. Prelaunch failure and terminal output
  share the same terminal slot. No release on exit, ACK, retry or UNKNOWN alone.
- `LaunchCommand.host_artifacts` carries version1, distinct terminal/progress
  reservation IDs and a SHA256 of run/task/attempt/segment/producer/command/revision.
  Version1 fixes slot counts and bounds; callers cannot choose larger limits or
  filesystem destinations. Execution epoch is excluded so the same original
  grant survives authenticated reconnect.
- Reuse schema20 `managed_artifact_reservations`, with terminal/progress classes
  and scope_key=segment ID. Existing fields represent this authority fully; no
  new migration or duplicate authority table is necessary for dispatch grants.
- Reserve both rows in ClaimReady's original transaction, before candidate intent
  hashing; later failure rolls back attempts/segments/budget/report/slots together.
  PendingLaunches restores both exact original rows and never reserves again.
  Before commit, validate every outstanding raw promise plus the entire control
  metadata delta of the transaction, including later input/continuation writes.
- Historical candidate proof reconstruction may preserve a legacy nil grant only
  when both rows are absent and its original intent matches. This is data-only;
  live dispatch with missing/partial/released authority fails closed. It does not
  mint slots for historical pending work or claim legacy filesystem ownership.
- Candidate intent extends its existing canonical body only when host_artifacts
  is present. Old nil-grant hashes remain byte-for-byte stable for history.
- Store authorization, grant shape and physical authority are separate. Host
  must reject malformed/missing live grants before its durable grant ledger.
  A syntactically valid descriptor alone cannot mint Store permission.

## Required milestones

- [x] Behavioral RED: new claim emits two durable reservations; failed admission
  leaves no execution/budget/report side effects. Exact pending/reopen/reconnect
  preserves IDs and charge. Resume gets new slots; retry retains old obligations.
- [x] Contract strict decode, scope/limit binding, candidate intent and signed
  owner envelopes; propagate through every claim/replay/history path.
- [ ] Descriptor-bound terminal/prelaunch and progress writers, physical bounds
  before opening files, fixed private stage, immutable content, fsync and recovery.
- [ ] Commit verified physical refs with Host lifecycle/progress, charge shared
  terminal/Exited once, preserve committed replay without rereading mutable paths.
- [ ] All consumers (summary/review/retry/rework/indexed/candidate input) use the
  same retained object authority. Legacy claims remain explicit and accounted.
- [ ] Scoped RED/green/race, full affected suites, controlled native public path,
  offline Docker Linux, build/vet and independent stable-snapshot review.

Dispatch reservation is a milestone, not complete writer enforcement or complete
M3. Explicit deletion/tombstones, full2GiB integration and native flood/RSS remain
in the parent plan after all writers are migrated. Intel/Windows native waits for
user-provided environments; existing Mac arm64 and Docker Linux work continues.

## Dispatch foundation acceptance

Completed the two dispatch milestones with ROOT full Mac Store/contract/Host/Coordinator/IPC, affected race and public CLI validation, plus offline Linux Store/contract. See `../docs/validation.md`. The remaining writer/ref/consumer stages above stay open; their implementation requires new affected validation.
