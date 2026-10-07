# ACP completion receipt validation

ROOT repaired the three blockers found in cef0cca/ef5358a: premature turn
ownership release, a later done-channel error revoking an accepted response,
and a cancellation/error publication gap. The pre-fix regression run failed
3 top-level / 7 named tests. Its JSONL is retained as actual RED evidence.

Final Go1.26.8 darwin/arm64 `go test -race -count=1 ./internal/grokacp` with
`GROKACP_NATIVE=1 TMPDIR=/private/tmp` passed 27 top-level / 36 named tests,
0 failures and 0 skips (2.833 seconds). `go vet ./internal/grokacp` exited 0.
The owned same-test-binary actor retained original PID/PGID 14964 and session
fixture-session for RPCs 3 and 4, checked exact prompt/response digests, and
finished through the original Handle Wait with exit 0 and tree-exited proof.
The child is also the race-instrumented test binary.

The preceding pure race run passed 26 top-level / 35 named tests, with only
the native opt-in case skipped. The final native run also includes the later
exact-receipt assertions. Raw logs are compressed beside `root-receipt.json`,
which binds 49 source/module hashes to these results. Earlier worker
`test-results.json` is marked superseded, not counted as final evidence.

Independent code review passed the final source snapshot and verified all
49 hashes, with no unresolved production blockers. This proves the bounded
library/owned-process receipt milestone only. Public Store/controller delivery
and real Grok/account/model acceptance remain outside these results.
