# Filter target host-native test

This worktree adds a Darwin-native acceptance test that reuses the accepted
producer → validator → readonly review chain and adds the process-free
`filter_target_preflight` observation as its fourth task. The test records the
target's rendered baseline, verifies the strict target delivery and lifecycle,
and checks that the target remains unchanged.

Validation is intentionally deferred to the integrating owner: this task was
explicitly limited to source changes, `gofmt`, and a commit; no Go test or
build command was run in this worktree.
