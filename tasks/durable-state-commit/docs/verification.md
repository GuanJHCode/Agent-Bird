# Durable state COMMIT verification

Environment: Go 1.26.8 at `tasks/g0-macos/data/go/bin/go`; `TMPDIR=/private/tmp`.

Before production changes, four deferred-FK cases failed as intended: claim,
stop, budget revision, and initial original-owner preparation each left an
uncommitted write visible through the pool while an external connection stayed
clean (`data/red.jsonl`, 28 JSON records, exit 1).

The focused four-case regression passed after the change
(`data/green-targeted.jsonl`, 24 JSON records, exit 0). The ordinary selected
state regressions also passed (`data/ordinary.jsonl`, 56 JSON records, exit 0):
durable state commit, runtime DAG, question answer, budget CAS and interrupted
resume, original-owner initial and attach, provider control, and rework.

The source SHA-256 values were recorded before the race run in the commit
history and can be regenerated with:

```sh
shasum -a 256 internal/store/{budget_revision.go,decisions.go,lifecycle.go,scheduling.go,rework.go,dag_rework.go,resume_continuation.go,owner.go,original_owner.go,original_owner_runtime_state.go,provider_controls.go,store.go,durable_state_commit_test.go}
```
