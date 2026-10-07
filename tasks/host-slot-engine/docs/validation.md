# Validation

Executed in `tools/orchestrator` with Go 1.26.8 and `TMPDIR=/private/tmp`:

```sh
go test -count=1 ./internal/artifacts
go test -race -count=1 ./internal/artifacts
```

Both commands passed. The tests cover shared terminal/prelaunch exclusion,
progress bounds and binding isolation, completed-stage recovery, partial-stage
rejection, physical replacement rejection, verification without namespace
creation, and lock cancellation.
