# Controlled ACP launcher validation

Scope: F06 launcher foundation only. Public controller submission, durable completion records and five uncertain windows remain open. No real Grok, account, model, user installation or release was used.

## Implementation

- Strict pinned executable and version probe under the same private ExactEnv/readonly macOS sandbox. Only explicit API-key mode reads XAI_API_KEY; none does not inspect it. Linux origin launch remains closed.
- Workspace capability sources are checked before probing and before Start; the complete ancestor source set plus system managed sources is denied by the OS policy. Normal instruction text remains allowed.
- Fresh0700 launch/home/grok/tmp directories retain original root and child identities. The actual provider must replace sandbox-exec at the original PID/birth and match the pin before its sole ACP client is exposed.
- Prepared owns both original Handles, including failed Start/Prepare. Start is one-shot. Close half-closes the stream, joins the reader, checks original Wait/tree, then cleans only the anchored private directory. UNKNOWN retains ownership and forbids another Start.
- Directory deletion reads32 entries per batch, checks cancellation between filesystem operations and supports resuming partial cleanup. Root replacement cannot redirect recursive deletion into foreign content.

## Evidence

Commands used Go1.26.8 with TMPDIR=/private/tmp in tools/orchestrator:

```sh
ORIGIN_NATIVE=1 go test -race -json -count=1 -timeout 120s ./internal/grokorigin
go vet ./internal/grokorigin
go build ./...
```

The final race log is `data/root-launch-cleanup-race.jsonl`: **31 top-level /137 named PASS,0FAIL,1 helper SKIP**, package PASS16.482s. `TestOriginSandboxPolicyActor` skips only in the unconfigured parent; the actual child helper ran through its explicit actor path. Vet and whole-module build exited0 after the final behavior change. Only the Close comment was clarified afterward.

The natural actor path proves version PID60678 Wait0, original ACP PID60684 Wait0/tree exited and the same session for RPC3/4/5. The production sandbox probe PID60736 proves ordinary read/private write allowed and state read/workspace write/fork/network/late regular and symlink capability reads rejected, then original Wait0/tree exited. Timeout/EOF-ignoring actors are deliberately stopped and their original tree proof is asserted.

Real regression evidence is preserved: `root-layout-review-red.jsonl` has8 failures for child replacement and post-check root deletion; `root-launch-full-race.jsonl` has the pre-canceled cleanup false-success failure; `root-grace-cancel-red.jsonl` has the wait-during-cancel reader-close failure. Files named `missing-api-red` and the earlier workspace/directory/policy new-API compile failures are setup evidence, not behavioral RED.

`data/root-launch-receipt.json` records source SHA256s, raw-log SHA256s and per-log counts. Earlier snapshots are not substituted for final cleanup acceptance. The earlier cancellation repeat is one case run10 times and predates the final grace/directory changes.

## Limits

- Controller must continuously consume Client.Updates and apply the total request deadline. Launcher completion does not establish public controller or real Grok tool compatibility.
- V1 denies process-fork and does not qualify real subprocess tools. Authnone acceptance uses only owned actors; explicit-key authentication was tested as a pure environment contract.
- Cleanup gives process operations a5-second context; process.Stop retains its existing1-second escalation grace. Filesystem syscalls are synchronous and are not hard-realtime preemptible. Recursive work checks cancellation between operations and has no background deletion.
- mkdir success followed by failure to open the brand-new leaf may leave an empty random directory; no provider has started at that point. Final empty-directory unlink is not an atomic inode-conditioned operation against arbitrary same-UID replacement, but never recursively traverses replacement content.
- Depth above256 fails closed and retains the directory. Any Close error, including cancellation during file deletion after process exit, means cleanup is incomplete and the owner must retain the Prepared object.
- Intel Mac/Windows native checks remain deferred for lack of machines. Linux platform gating is implemented; this milestone does not add Linux Provider sandbox support.

Independent reliability review of the final production/tests/log snapshot: PASS. The reviewer confirmed both cancellation fixes and anchored partial-cleanup recovery; did not run tests independently. ROOT ran and counted the tests above.
