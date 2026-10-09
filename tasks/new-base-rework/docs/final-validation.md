# F03 authenticated fresh-base generations

Implementation source: `5cbd657` on `feat/new-base-rework-20261009`; incorporates ROOT integration `039c0d4`. The final evidence commit changes only task documentation/scripts/data.

## Behavior and public use

An accepted, settled integrated candidate DAG can authorize one constrained successor with a new run, task IDs, launch/control identities, Host generation, budget groups and fresh execution budgets. Its source integration TargetHEAD becomes the producer and downstream target base. Old histories remain unchanged. New outputs bind the immutable lineage digest through producer receipts, ordered composition and downstream delivery. The whole successor owns a durable integration target reservation until proved terminal release; earlier ordinary queued/resume_queued integrations cannot be overtaken.

```sh
orchestrator submit \
  --state-dir <private-state-dir> \
  --request <fresh-request.json> \
  --source-control-file <source-control.json>
```

Both request and source control files must pass the existing owner/private `0600` checks. The fresh request uses the normal submit fields and a `new_base_rework` object:

```json
{
  "version": 1,
  "source_integration_task_id": "old-integrate",
  "source_event": {
    "event_id": "old-integrated-result",
    "event_revision": 1,
    "event_hash": "<exact accepted SHA256>"
  },
  "task_map": {
    "old-producer": "new-producer",
    "old-validate": "new-validate",
    "old-review": "new-review",
    "old-integrate": "new-integrate"
  },
  "feedback": "<owner findings>",
  "acceptance": "<new acceptance criteria>",
  "command_id": "<globally fresh command>"
}
```

The displayed object is the value of `new_base_rework`, not a standalone submit request. Provide every ancestor task in the exact map, mechanically remap edges/ordered sources/groups and preserve the admitted source pins, profiles, paths, policies and Directory fields. Change producer/target BaseOID to the preceding integrated TargetHEAD. For derivation from an already-derived generation, omit its injected producer `new_base_rework` context; the Store authenticates and injects the successor context itself. Preserve the admitted prompt while appending new feedback through the explicit spec. Do not supply launch/control tokens or internal authorization in request JSON. The source control token is used only for authentication and is excluded from canonical identity/lineage/logs.

Exact CLI retries reuse a durable preparation marker and the same private control/bootstrap identities. Changed requests conflict. Source ownership is reauthenticated. Each exact source integration event permits one successor; a third generation uses the newly accepted second-generation integration event and control capability.

Supported source shape is deliberately bounded: a complete single integration ancestor DAG with producer roots, exactly one v1 validation/review/integration closure and optionally one v2 ordered composition. Producers and validate/review must already use AutoDirectory=true; Directory stays exact and integrate retains its target Directory/ref. Fallback, sessions, args, config-generation, indexed review, pre-integration Rework and revised budget lineage remain unsupported. Ordinary submit cannot inject a new-base context. Existing ordinary submit and old rework semantics are preserved.

## Applicable evidence

All raw files and failures are retained under `data/`; each final command has its exact command, cwd, Go version, source HEAD and environment metadata. `actual-counts.json` counts actual named terminal events and excludes package passes.

| Evidence | Top-level / named | Result and scope |
| --- | --- | --- |
| `store-final-race-03.*` | 4 / 13 | PASS: exact replay/mode conflict, byte oracle/quota, real COMMIT rollback, competing source target holds/release, earlier queued/resume_queued admission, missing/corrupt ticket failclose |
| `store-final-race.*` migration subset | 3 / 3 | Individual PASS: schema33 preserves source/refuses real-history erasure, owner-launch historical migration, pending-auth actual COMMIT rollback. Whole batch remains FAIL and is not reported passing |
| `public-final-runtime-race.*` | 4 / 8 | PASS: three real CLI/Coordinator/Host/Git generations with fake Claude/AGY protocol fixtures, dirty checkout/history preservation, exact replay, prepared barriers, stale HEAD refusal and old ordered chain compatibility |
| `host-final-race.*` | 2 / 5 | PASS: exact source order and composition provenance/receipt gates |
| `build-final.*` | — | Module `go build ./...` exit 0 |
| `vet-final.*` | — | Related Store/adapter/Git/Host/Coordinator/CLI vet exit 0 |

`NEW_BASE_NATIVE_RACE=1` is recorded for the public final run. Its raw log shows the nested actual orchestrator `go build -race`; both the parent/provider fixture and actual Coordinator/Host binary are instrumented. The source snapshot is clean `5cbd657`. No package-wide Store suite was run.

The earlier red/diagnostic/public/race failures remain distinct and visible, including the queued admission RED, replay self-scope failure and corruption-fixture trigger rejection. They are not overwritten or counted as final passes. Historical migration source and its execution paths were unaffected by the subsequent F03 scope/ticket fixes, so those individual migration passes are reused.

ROOT independently reviewed Store `127e399` PASS, public/Host `9bbbc31` scope and prepared barrier `5cbd657` increment PASS, and independently parsed final raw commands/counts. ROOT owns final integration and product/support documentation.

## Limits and cleanup

The macOS public chain uses real Git, sandbox and fake local protocol providers; no real model/account or provider network was used. Cached Linux environments have no Git, so Linux public/native generations were not run; Linux Store execution was not performed by this child. Prepared publication uses real file/directory Sync and per-call barrier failure injection; no physical power-loss acceptance is claimed. Unsupported source shapes fail explicitly. No installation replacement, image pull, push or release occurred.

Original Coordinator Wait=0 is retained for all four failed native scenes. Only their 16 owned copied binaries were removed after exact-path ps/lsof actor absence, file hashes, available provider pins and expected module checks; 337,920,352 bytes were removed. DB/request/control/bootstrap/log/Git scenes remain. The earliest pre-admission failure had no durable provider pin, which is explicitly recorded in cleanup evidence. No actor was signalled. Empty mistaken nested artifact directories were removed. Successful final native runtime cleaned its own scene after original Wait.
