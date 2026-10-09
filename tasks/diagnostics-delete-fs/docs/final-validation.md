# Diagnostics deletion filesystem validation

Scope: `internal/diagnostics` only.  This evidence covers the private
filesystem lease used after Store has authenticated and durably recorded the
deletion intent; it does not establish Store, CLI, quota, or native product
acceptance.

Implementation commits:

- `1601082 feat: add replayable diagnostics deletion lease`
- `4acf7ac fix: harden diagnostics deletion replay`

Final targeted command, from `tools/orchestrator` with Go 1.26.8 and
`TMPDIR=/private/tmp`:

```sh
go test -race -json -count=1 ./internal/diagnostics -run '^TestDeletion(ClosedNamespaceReplay|PlanMaximumInventoryFitsBound|RejectsLiveUnknownAndReplacement|TornStageRecoveryAndForeignStageRefusal|CrashReplayAfterChildWait)$'
```

Result: exit 0; 5 top-level tests passed, 0 failed, 0 skipped; package elapsed
1.744s.  The preserved JSON event evidence is
`tasks/diagnostics-delete-fs/data/deletion-race-20261009.jsonl`.

The grouped cases cover closed namespace deletion and exact-plan replay;
maximum bounded plan encoding; live writer, unknown entry and replacement
rejection; a valid torn marker-stage prefix versus a foreign stage; and an
actual test-child `os.Exit(91)` after the marker and first chunk unlink.  The
parent observed nonzero child completion, retained `manifest.json`, observed
the missing planned chunk, then reopened and completed replay.

No whole-repository suite, real model, user install, push, or release was run.
