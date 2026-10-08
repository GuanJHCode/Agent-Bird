# F12 crash qualification evidence

The sole initial focused run was executed against merged integration baseline
`26e212e` with Go 1.26.8 and `TMPDIR=/private/tmp`:

```sh
TMPDIR=/private/tmp /Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go test -count=1 -v ./internal/install -run '^TestConfigGenerationPublicationProcessCrash(Replay|Conflicts)$'
```

Its first unedited terminal output is `data/focused-actual.log`. It contains two
top-level tests, eight named subtests, seven passes, one failure, and no skips.
The replay matrix passed at `intent`, `manifest`, `active`, and `receipt`.
Each original helper process was retained by the parent and had exactly one
recorded Wait after SIGKILL: 93489/intent, 93491/manifest, 93492/active,
93493/receipt, 93494/changed_request, 93495/foreign_scope_expected,
93496/active_inode_drift, and 93497/malformed_stage.

The `active_inode_drift` case received `config_generation_conflict`, as
required, but the tree was changed after the snapshot. The first minimal
attempt to open an existing owner read-only did not remove the write; its raw
diagnostic re-run is `data/focused-fixed-diagnostic.log`. That record isolated
the changed entry to the `generations` directory: the restarted `intent`
publication wrote its manifest before `commitActive` checked the saved active
identity.

The final repair has two barriers: existing owners are first opened without
creating optional directories, and an unstaged pending publication compares the
current active identity and digest with its immutable intent before replaying a
manifest. `data/focused-fixed.log` is the passing final ordinary run and
`data/focused-race.log` is the passing race run. Both have two top-level tests,
eight named subtests, eight passes, zero failures, zero skips, and eight
original SIGKILL Wait records. `data/focused-vet.log` is the empty-output,
successful `go vet ./internal/install` record.

The three failing test runs retained task-owned temporary prefixes for
diagnosis. After exact-path contents checks (63 entries each) and process
checks for all their helper PIDs (none remained), all three were deleted. The
final ordinary and race runs cleaned their prefixes via `t.Cleanup`; no owned
helper process or prefix remains. No user installation or account data was
touched.
