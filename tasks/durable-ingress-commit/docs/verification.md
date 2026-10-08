# Durable ingress COMMIT verification

Environment: Go 1.26.8 at `tasks/g0-macos/data/go/bin/go`; `TMPDIR=/private/tmp`.

The red regression ran before production edits. Its three deferred-FK cases
failed as intended: the external connection observed no write while the pool
observed the failed transaction's uncommitted ingress state. Raw output is
`data/red.jsonl` (23 JSON records; command exit 1).

After the change, the focused regression passed (`data/green-targeted.jsonl`,
20 JSON records; exit 0). The required ordinary verification passed
`TestDurableIngressDiscardFailedCommit`, `TestRuntimePersistsDAGSlotsEventsAndExplicitResume`,
`TestReportReconnectRevokedBindingUpdatesOnlyEpochAndRetainsGuards`, and
`TestReportReplayBindsKindAndProgressCannotConsumeCriticalReserve`
(`data/ordinary.jsonl`, 64 JSON records; exit 0). The same selection passed
under `-race` (`data/race.jsonl`, 64 JSON records; exit 0).

Source SHA-256 after verification:

```
d99f0c008978ca9d96592a0d5a0916e821ae06908ab09a93660a447a63d11d0c  internal/store/admission.go
bd5a6de90149dd6bb2ccf892e5704dda455ab5e4351abd46e37f006ac3d94482  internal/store/host_events.go
b41d3717b96ce8d59591b8dea3d564463ca4ae9206bffb629f174f9137726505  internal/store/reports.go
9d01491757a95596f971b43fc0ff962bc13f39064bdbfacd06d46a01be667a55  internal/store/owner_transaction.go
c0bd9489ffa426462ebb74eef58e1dfa575dfb715d64dcb8242406ee5546bd16  internal/store/durable_ingress_commit_test.go
```
