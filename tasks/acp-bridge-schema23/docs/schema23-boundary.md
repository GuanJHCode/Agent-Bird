# Schema23 implementation boundary

Scope: migrate schema22 to schema23 in Store only. The migration rebuilds only
`bridge_bindings` with foreign keys disabled before the transaction, adds ACP
receipt columns and the partial delivery-ordinal index, recreates the affected
quota triggers, and restores `foreign_keys=ON` after both commit and rollback.

The actual modernc error when the parent was rebuilt with child triggers still
installed was `error in trigger quota_bridge_ingress_INSERT: no such table:
main.bridge_bindings`. Schema23 therefore snapshots, drops, and restores the
exact DDL of only those nine child quota triggers; it does not regenerate their
unrelated accounting through the shared helper. Only the parent and attempt
trigger sets are rebuilt.

Out of scope: ACP RPCs, controller/public APIs, native/provider tests, and
changes to schema22 accounting behavior.

Acceptance evidence: modernc-backed Store tests cover the full schema22 shape,
data and accounting preservation, re-open, trigger behavior, failure rollback,
and the `defer_foreign_keys` counterexample.

Validation: `go test -json -timeout 10m ./internal/store -run
'^TestACPBridgeSchema23' -count=1` reported 10 passed tests, 0 failed, and 0
skipped. The broader Store suite still has pre-existing local artifact-path
failures (`artifact_intake_unsafe` / `unsafe_report_artifact`); schema-focused
migration tests had no schema23 duplicate-column failures after fixture
downgrade was corrected.
