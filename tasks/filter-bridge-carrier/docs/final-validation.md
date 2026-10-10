# Original Git gate — final validation

Qualified source: 4438e55cb1781d9fac835573fdd3ac2f5229c63a.
Go tree: b84e7bdc47c6b50a711095ebab0fc741f64047f3.
Production: 5480591 (gate), 69abd7b (session original-owner guard).
Independent review: raw_workspace_review PASS on 4438e55, after fixed-snapshot
BLOCKED reviews of 43fb171 and 3d1f1f1. ROOT implemented and verified the final
lifecycle fixes; the worker's earlier three tests and intermediate summaries
were not qualification.

## Delivered boundary

The private Go gate borrows the original ExactTransformSession, owns transferred
connected endpoints, admits one activation and one stream, and permanently
rejects restart. It validates the original live process Handle, binds the peer
lifetime, verifies the peer before first protocol IO and before/after Exchange,
and commits publication under both gate and session state locks. Close cancels
admission before transport closure and waits for original serving/endpoint
completion. Rejected endpoints are included in drain. Close failure, timeout,
invalid close context or peer uncertainty retain sticky Unknown.

Calls counts Exchange attempts, including known rejection; ResponsesCommitted
counts publication ownership, not completed writes. Drained proves serving and
endpoint closes completed without close error, but never clears prior Unknown.
Gate closure does not close the borrowed session, Broker or original Git Handle.
Both session and gate reject shallow copies. Invalid gate receivers perform no
IO and leave the connection with the caller; valid receivers own rejected
connections too.

The caller is trusted Go code and must bound admission. This is not a listener
and does not authorize arbitrary repeated network accepts or establish the
source of peerPID. A future listener must obtain PID from that exact connection
and cap pending/serving connections before calling this API.

## Verification and evidence

All command/source/Go-tree/clean-state/stdout/stderr/exit sidecars are retained
in data; ROOT recomputed their hashes, parsed actual test events, and verified
the source trees. See data/root-verification.json.

- aa1124f gate behavior RED: 1 top failed; 10 named failures, 3 passes, 0 skips.
  SHA256 5c524aa11e92216ac918ea45e9f0a6a94a6dfc0f3ccea07ead4ce0625c609f88.
- 38c4546 isolated session copy RED (a7d243e production): 1 top / 3 named
  failures. Both live and original-closed copies actually reached Broker load
  and returned a result. SHA256
  6c8a9d3082088bd911fe85752f17b7ddcbe152095464bc1fe53f19921522b9db.
  The separate owned worktree/ref is retained for exact reproduction.
- cb6b158 nil-Close RED: 1 top / 3 named failures, zero skips. SHA256
  32257e96cac1f5b9fa1dbac16502c37fd98af09358d56160de02d43c2ef79bb0.
- 5480591 final complete focused race: 10 top / 64 named passes, 0 failed or
  skipped. SHA256
  3f220f5697661f3fbcfbb0f7ed63f5a8af1b27bf19812c79a61c080745e1d8f4.
  Includes new gate lifecycle, constructor/current-owner and session-copy tests,
  existing pure exact-session tests and a short actual original Handle fixture.
- 4438e55 corrected reflection type expressions: affected 4 top / 54 named
  passes under race, 0 failed/skipped. SHA256
  7007166f74a8e2b71c51ff37b83c12f86d77d25a9e0854da8f432f2dddcd43e0.
  This final commit changes only test type inspection; production, native
  fixture and the other six passing tops are unchanged and their evidence reused.
- Whole-module Darwin/arm64 build and Linux/arm64 CGO=0 build at 5480591:
  exit 0, empty stderr. Final gitfilter vet at 4438e55: exit 0, empty stderr.

Pinned toolchain is existing Go 1.26.8; runner forces local/offline toolchain,
TMPDIR=/private/tmp and isolated evidence labels. Every original ROOT exec
session was drained: 36087, 53699, 26072, 35246, 82191, 87615, 83116, 89112.
Both vet commands returned synchronously. The first direct runner invocation
failed permission checks before running Go; the explicit pinned Python invocation
then produced the actual recorded RED. No background test remains.

The short native fixture used /bin/cat PID 48447 with the original StartDuplex,
Wait exit 0 and tree proof, verified live original activation and rejection after
Wait, and proved gate cleanup did not stop the original process. This is a
process-adapter fixture, not actual Git, kernel socket authentication or a Broker
driver roundtrip. Reuse the already-qualified process-package live Handle-copy/
Stop proofs and existing exact Broker native fixture; neither long fixture was
rerun.

## Retained failures and limitations

The 3d1f1f1 race result is retained: 7 top passes / 2 top failures; 58 named
passes / 3 failures. One assertion incorrectly required exactly one successful
concurrent activation, although duplicate poison may reject both. Another test
copied a live Handle without its private mutex, racing original Wait. Both were
test defects, corrected without weakening production. The existing process
package supplies the synchronized live-copy proof.

The 5480591 vet result is retained as failed: reflect.TypeOf(*pointer) still
copied synchronization state. The final TypeOf(pointer).Elem() version passes vet
and the affected race batch.

No listener, socket-path lifecycle, helper executable, routing digest, durable
carrier journal/quota, target transition, Host/CLI/Store wiring or final merge was
implemented here. Public filtered integrate/compose/retry remain closed. Current
F13 package source 4e1229c predates this gate and session-copy fix. Linux here is
whole build only, not native gate qualification. Formal60 resource acceptance
remains failed; Intel Mac/Windows native work remains deferred.

No model/account call, user installation replacement, real hook registration,
push or release occurred. Historical UNKNOWN roots were untouched. Retain raw
evidence and the small reproduction worktree; no disposable new binary or
runtime root was created by this milestone.

