# Private signed recovery handoff acceptance

Implemented signed `recovery_scope`, existing-only held ledger validation before Applied/Activate, and same-process successor renewal when Bind and handoff span Coordinator epochs. State/Attach RPCs have individual two-second deadlines. The scope handoff preserves all other recovery fields, and cannot dispatch work itself.

Stable source is pinned by `data/final-source-sha256.json` (15 Go files plus the Linux runner) and `data/root-final-receipt.json`. Production and final test deltas received independent PASS reviews. ROOT checked raw streams, exact selectors, terminal results, owned original Wait observations and container receipts.

| Completed check | Top / named | Result |
| --- | --- | --- |
| macOS Host RPC/deadline race | 10 / 37 | 37 PASS, 0 FAIL/SKIP |
| Shared contract race | 1 / 1 | PASS |
| macOS wider Host runtime/execution race | 38 / 107 | 107 PASS, including three real blocked-runtime cleanup tests |
| macOS Coordinator handoff/startup race | 9 / 31 | 30 PASS, 1 child-helper SKIP, 0 FAIL; 99.424s |
| macOS admission, transport, ACK-loss, five-operation and stop race | 28 / 56 | 56 PASS, 0 FAIL/SKIP; 587.522s |
| Ordinary server and five transport scenarios | 7 / 34 | 34 PASS, 0 FAIL/SKIP |
| Docker Linux Host | 10 / 37 | 37 PASS, 0 FAIL/SKIP |
| Docker Linux Coordinator selected cases | 32 / 68 | 67 PASS, 1 child-helper SKIP, 0 FAIL |

The first combined macOS Coordinator race batch hit its five-minute aggregate timeout after 64 named PASS/one helper SKIP; that package is FAIL and its raw log remains. It is superseded for acceptance by the two completed uncached batches, not relabeled. A filtered process observation found no remaining coordinator test actors after the timeout; interrupted children do not receive invented parent Wait assertions.

The Linux raw regex counts include nested child stdout. The missing-session negative test intentionally produces child exit1/FAIL and asserts it in a passing parent. Go `test2json` plus the original selector yields the selected 67 PASS/one helper SKIP; three separately emitted RuntimeChild PASS are excluded. Original raw counts and the derived explanation remain in the receipt.

Both offline Linux containers exited zero, were not OOM-killed, and were removed by exact CID with subsequent absent inspection. Both temporary test binaries were removed only after matching their receipt SHA256. Final contract/Coordinator/Host vet and whole-module build exited zero. Native real successor startup uses the original Cmd Wait, confirms epoch2/revision2/Applied2/HostReady, unchanged Owner and FirstHost tuples and zero new recovery slots; the missing-session child is expected to exit1 without forced cleanup.

This closes the private live-successor handoff/runtime milestone. The predecessor in that native fixture is trusted Store history, so this does not claim actual public CLI deadBoth Resume. Bound-never-Applied abandonment, initial Bind before any ledger, preBind lost-handle recovery, pending-authorization origin rebind, real Providers, installation and unavailable Intel/Windows qualification remain separate work.

Integration a692dba matches all 16 manifest hashes; whole-module integrated build exits zero. See data/root-integration-receipt.json.
