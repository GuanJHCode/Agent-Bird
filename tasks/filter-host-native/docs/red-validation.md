# Filter Host native fixture — RED receipt

- Source: `e8bdeb7119a557862fe3aca1ebc69bb41d2b4daf`; recorder captured an empty dirty state.
- Command: the absolute Go 1.26.8 binary ran `go test -count=1 -json ./internal/host -run '^TestFilterPipelineNativeHostAcceptedValidator$'` with `TMPDIR=/private/tmp`.
- Result: one top-level test failed, zero passed and zero skipped, exit status 1. The failure occurred at `SubmitPlan` with the existing `git_filter_pipeline_unsupported` gate, so this is the expected production-gate RED rather than a fixture or build failure.
- Raw evidence: `data/native-red.command.json`, `data/native-red.stdout`, `data/native-red.stderr`, and `data/native-red.exit`. The stdout SHA-256 is `8ae7eb7771c554cffec688e80ef5ec970533e2bc9c44cc08bcb8fea1f54407bf`; stderr is empty.
- The fixture's controlled actor builder, `xcrun`, and CLT Git paths require original `Wait` plus `ConfirmTreeExited` before continuing. The raw test output records only cleanup state (`pending=0`); it does not contain per-Git process logs. This receipt does not manufacture such logs.
- Failure root retained without cleanup: `tasks/filter-host-native/tmp/native-4147453358`.
