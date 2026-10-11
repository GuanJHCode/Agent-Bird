# F02 diagnostics opt-in automatic GC

- Scope: public run-control enable/disable/status; persisted narrow policy, bounded scheduler, exact saved plan replay; diagnostics namespace only.
- Default disabled. Fixed 7-day TTL from max(valid terminal received time, successful business-result consumption time). Historical task-only receipt timestamps cannot bind attempt/revision and never authorize automatic deletion.
- Record successful result consumption in AckDecisions transaction with current attempt/work_revision and matched handled result event. No raw control token persisted.
- Policy binds controller, control-token hash, original run binding fingerprint (including owner rebind history), revision. Each store authorization transaction checks current binding. Disable and fresh deletion admission share DB mutex/transaction. Existing prepared deletion intents may finish after disable, never after owner drift.
- Reuse PlanDiagnosticsRetention/ApplyDiagnosticsRetention and DeleteDiagnostics terminal, namespace, revision, read fence and held filesystem identity guards. Private context authorization is created only by policy-validated runner and cannot be supplied through public IPC.
- One coordinator loop, cancel+wait on Close, bounded one policy/64 scan/8 deletes per tick; persisted round-robin policy cursor and task cursor. Save exact plan before apply. Replay saved exact plan on restart; no replanning unfinished work.
- Schema37 adds policy, exact plan state and consumption evidence, all charged in control-plane capacity ledger with admission checks.
- Validation: focused RED then 3-5 behavior tests for TTL/unconsumed/unknown, permissions+disable, exact replay and public IPC; preserve targeted existing retention/deletion tests. Fixtures only, no user runtime touched.

## Implemented milestones

- Store/schema37: private narrow policy authorization, exact bounded plans/cursor/replay, fresh consumption checks at both snapshot and intent admission; saved intent can finish after disable.
- Consumption: exact handled result receipt is stored by ACK; the first transaction with both completed and handled conditions supplies success consumption time. ACK-first completion is recorded by the same completion transaction's SQLite trigger. Both paths use current attempt/revision/latest result segment. Fixed timestamp capacity is prepaid.
- Public route: diagnostics-gc-enable/disable/status require original run control and private request `{version:1,run_id,expected_revision}`. Status starts disabled/revision0; changes compare revisions. Coordinator owns one loop, one bounded run per minute, cancellation and wait on Close.
- Focused verification is in data/verification.md and raw JSON reports. Public docs/support matrix and independent review are assigned to main session.
