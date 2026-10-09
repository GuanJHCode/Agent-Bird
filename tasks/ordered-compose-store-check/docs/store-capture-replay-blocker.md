# Store v2 compose capture/replay blocker

The real two-producer lifecycle reaches `ClaimReady` after both source tasks
publish result and exited events and receive owner acceptance. Claim admission
then fails with `legacy_decision_binding_ambiguous` in the legacy decision
binding reconciliation before `bindCandidateInputContent` or
`bindOrderedDependencyInputs` runs.

The required scheduling/decision-binding change is outside this worktree's
allowed Store file ownership. No source change was retained because it would
not exercise the requested v2 capture/replay path.
