# Public ACP controller acceptance

The explicit macOS public controller now reuses its original OwnerBind grant,
launches one pinned readonly ACP origin, drains its one Updates stream, and
delivers complete canonical batches into that original session. It persists a
private immutable completion before Receive. Protocol receipt never creates a
business decision. No real Provider, model, account, installation or publication
was used or changed.

## Final evidence

Go1.26.8 on macOS arm64. `root-public-final-native-race.jsonl` ran with
`ACP_PUBLIC_NATIVE=1 ORIGIN_NATIVE=1`:

- Public ACP/immutable completion: 20 top-level / 130 named PASS, 0 failures or
  skips; package PASS49.446s. This includes the sparse-reply regression and all
  earlier public actors after the final production change.
- Origin launcher/request/policy: 31 top-level / 137 named PASS, 0 failures;
  one parent `TestOriginSandboxPolicyActor` helper skip. Its actual subprocess
  was exercised by `TestOriginSandboxPolicyProbe`; package PASS16.396s.
- Existing Hook shared-submit regressions in
  `root-public-shared-launch-race.jsonl`: 4 top-level / 6 named PASS, 0 failures
  or skips, package11.797s. The shared-submit production code did not change
  afterward. That earlier command omitted `ORIGIN_NATIVE`, so its origin skips
  are not native acceptance; the final command above supplies that evidence.
- Final `go vet ./cmd/orchestrator ./internal/grokorigin` and `go build ./...`
  both exited0. Required source and evidence hashes are recorded in
  `../data/root-public-receipt.json`.

Normal public acceptance used an unmodified compiled CLI and owned Go actors.
Final controller PID80264/native PID80363 retained one owner grant and session,
performed initial RPC3 plus delivery RPC4, then original native Wait0/tree exit.
Local and Store receipts matched; task remained `result_ready` with zero
business decisions. The native actor trace records requests; raw response-byte
hash correctness also relies on the separately accepted grokacp Client tests.

The six boundary cases cover before Claim, after Claim, after committed Begin,
full write/no response, partial response, and durable local completion before
Receive. They assert the original ordered RPC trace, no extra Prompt, exact
attempt state, zero RECEIVED or business decisions, and the appropriate local
record count. The source-boundary variants use Go build overlays, explicitly
not physical OS-failure injection. No product fault switches were added.
The lost/partial-response cases use a real owned actor that withholds/truncates
its response and is stopped by original process cleanup (final Wait=-15).

Receive ACK-loss discards the first real committed reply. Two byte-identical
Receive requests match the one durable local record; only one original Prompt,
one attempt and one RECEIVED row result. No Begin/Prompt retry occurs.

Cleanup-pending replaces the first two cleanup results with injected UNKNOWN,
then proves both controller and native remain alive before the owned actor
exits naturally. The controller keeps the original Prepared and passively
observes until original Wait0/tree evidence, then joins Updates and cleans up.
This validates the lifecycle decision, not the operating system's ability to
produce a physical UNKNOWN. The wait can exceed the active-work timeout; an
external forced kill cannot preserve ownership guarantees.

Actual closed stdout verifies FD1 EPIPE enters cleanup and reaches original
Wait0/tree exit. An independent test-binary helper verifies stopping ACP's
SIGPIPE subscription preserves an already-registered subscriber.

## Discovered regressions and failed probes retained

- `root-finish-behavior-red`: two real failures for cleanup error priority and
  metadata-write failure falsely reported as persisted uncertainty; fixed.
- `root-public-closed-output-red`: ignored status-write error continued work
  until the test canceled it; fixed by checking both active/receipt outputs.
- `root-public-sparse-activate-red`: a sparse Activate reply inherited old Bind
  fields and activated delivery; fixed with new values and all31 required
  fields/non-null checks across four response types. Missing/null tests check
  that failure cannot partially mutate an existing value. Invalid replies are
  not treated as transport ACK loss.
- The first native fixture failed with an overlong temporary Unix socket path;
  the next failed on a nonexistent SQL column. These were fixture failures,
  not product behavioral regressions. Their raw logs remain.
- The first SIGPIPE subscriber test used `kill(SIGPIPE)`. Darwin Go ignores
  asynchronous SIGPIPE in its runtime; this was an invalid trigger, replaced
  by real FD1 EPIPE. Its failed log remains and is not acceptance evidence.
- Missing-API compilation RED logs are preserved but are not behavioral RED.

Independent read-only reviews passed common submit, immutable completion,
lifecycle finish, owned-actor tests, and the final response-decoder fix. ROOT
read fresh JSON outcomes, checked counts, original wait/tree evidence and all
command completions. Fixture cleanup retains private state whenever ownership
cannot be proved; no such retention was reported by the final passing run.

## Remaining scope

Real Grok version, model and tool compatibility is not qualified. The current
profile denies forks and workspace writes; only the explicit API-key mode can
copy XAI_API_KEY. Intel Mac/Windows native acceptance lacks machines. Linux
Provider/sandbox support is separate. Completion-record quota/retention/GC,
physical storage faults, F10 original-owner succession and the other F01–F13
requirements remain open. This milestone does not claim full product closure.
