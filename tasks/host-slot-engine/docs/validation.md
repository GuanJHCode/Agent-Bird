# Validation

Executed in `tools/orchestrator` with Go 1.26.8 and `TMPDIR=/private/tmp`:

```sh
go test -count=1 ./internal/artifacts
go test -race -count=1 ./internal/artifacts
```

Both commands passed. The tests cover shared terminal/prelaunch exclusion,
progress bounds and binding isolation, completed-stage recovery, partial-stage
rejection, physical replacement rejection, verification without namespace
creation, lock cancellation, private-root mode drift, and every manifest/data
publication boundary. `data/red-partial-stage.json` records the behavioral RED:
the prior stage-recovery path rejected a retry carrying the original bytes after
an interrupted partial write. The repaired path accepts only that same
descriptor/body pair. `data/post-fix-artifacts.json` and
`data/post-fix-artifacts-race.json` contain the final ordinary and race JSON
evidence: 17 top-level tests, 59 passing test events, zero failures/skips.
`go vet ./internal/artifacts` also passed; its captured stderr is empty.
