# Store v2 compose capture/replay result

The first real two-producer lifecycle exposed `legacy_decision_binding_ambiguous`
at both ClaimReady and PendingLaunches before the v2 binder ran. The fix makes
only a strict v2 compose action bypass legacy decision-binding reconciliation;
v1 actions continue to use the old guard.

The targeted regression now proves real source result/exited/owner-accept
handoff, declared `[b,a]` capture, PendingLaunches replay, and rejection of a
missing persisted capture without attempt or quota drift.
