# RED evidence — public installed-config supervised admission

Command (Go 1.26.8; `TMPDIR=/private/tmp`):

```sh
go test -count=1 ./cmd/orchestrator -run '^TestSupervisedInstalledConfigPublicEntry$' -v
```

Observed result: one top-level test fails, with zero skips. The controlled
installed package publishes generation revision 1, and the public `task submit`
persists the generated reference and `claude-model` in `task_runtime`.
The queried `host_launches.execution_owner_mode` is `legacy`, where acceptance
requires `supervised-v1`.

The cause is a current contract contradiction, not a fixture option omission:

* `cmd/orchestrator/task_entry.go` and `controller_bridge.go` create local-owner
  submit requests.
* Config-generation submission only admits local owners in
  `internal/coordinator/config_generation.go`.
* `internal/coordinator/server.go` selects supervised mode only for non-local
  requests.

No production files were changed. The retained failed temporary directory is
printed by the Go test for inspection; it contains only synthetic package,
provider, and request data.
