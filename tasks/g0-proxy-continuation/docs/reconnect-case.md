# Bounded reconnect case

This case is based on fixed commit `6e48675` and owns only
`tasks/g0-proxy-continuation/scripts/reconnect_case.py` plus its test and this
document. It reuses `ResumeCaseSpec`, the existing preflight/hash contract, and
the existing `NativeRuntime`; it does not modify `resume_case.py` or the native
runtime.

The prepared argv is the ordinary resume entry:

```text
<cli> resume <P>
```

No prompt, model override, remote flag, or global configuration change is
added. The preflight records the new runner source automatically through
`--implementation` handling, in addition to the existing runtime, observer,
transport, and resume-case sources.

The case sequence is fixed:

1. Verify the reviewed preflight, source/config hashes, endpoint vacancy, and
   fixed native binary hash.
2. Start the existing runtime and wait for an initial candidate whose thread
   ID is exactly `P`. Both the TUI-side and private-backend peer identities must
   be complete and free of identity errors.
3. Call `runtime.disconnect(connection_id, leg="frontend")` exactly once for
   that same connection.
4. Use one local reconnect window (`local_window_seconds`, normally 10s). The
   old epoch must become invalid before the new epoch is accepted. The new
   epoch must show bidirectional upgrade, `initialize` request/response,
   `initialized`, `thread/resume(P)` response, and fresh `skills/list`
   request/response. The candidate must be from the new epoch and have ID `P`.
5. Run `/status` only after the new candidate exists. Its result must contain
   exactly `P`; status is corroboration and never candidate evidence.
6. Request normal `/quit`, wait for exit, capture the complete trace, and run
   owned cleanup in `finally` even when qualification fails.

The native implementation may retry reconnect for a longer internal budget;
this case does not extend its local 10 second probe. The cleanup wait is a
separate bounded local allowance after the probe decision.

The result is classified as follows:

- `old_epoch_only`: the old epoch was invalidated, but no new epoch appeared
  before the local deadline.
- `transport_reconnected`: a new authenticated epoch and/or handshake appeared,
  but the new `P` attachment contract was incomplete.
- `P_reattached`: the new epoch completed handshake, `thread/resume(P)`, fresh
  skills evidence, exact status corroboration, and owned peer checks.
- `unknown`: the old barrier, trace, identity, or turn evidence was incomplete
  or contradictory.

Every case writes `trace.json` and `result.json` under its exclusive case
directory when the case directory was created. The result retains the
classification, old/new epoch metadata, handshake booleans, identity status,
status observation, complete trace flag, `model_turns`, client/server turn-zero
evidence, termination, cleanup, and failure records. An old epoch candidate is
never accepted after the disconnect; a new epoch must build its own proof.

The CLI mirrors the existing explicit boundary:

```text
python tasks/g0-proxy-continuation/scripts/reconnect_case.py --prepare ...
python tasks/g0-proxy-continuation/scripts/reconnect_case.py --run ...
```

`--prepare` creates only the exclusive preflight record. `--run` requires that
record and is the only path that can start native processes. No native/model
execution was performed while adding this case.
