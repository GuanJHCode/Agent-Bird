# F10 Coordinator-owned launch attempts

Status: candidate only; not integrated. The complete F01–F13 programme remains open.

## Implemented scope

Public opt-in `submit --owner-launch-protocol 2` retains the original Owner `exec.Cmd` and sole `Wait` in the existing Coordinator. FirstHost passes two empty child descriptors, waits for a signed descriptor-close acknowledgement, then sends bootstrap and source environment directly to Owner. The Coordinator never reads that channel.

The immutable v2 attempt/receipt domain permits one pre-Bind replacement FirstHost under the same OwnerID, incarnation, scope, origin, generation and executable. `recover-host` preserves the protocol through its existing publication lease. It does not consume another business attempt, ready-queue slot or active-time grant. Existing v1 records do not acquire synthetic launch proofs; an unproven launch after Coordinator loss remains UNKNOWN.

## Evidence currently available

- Empty FD handoff ordinary and race: one top-level test, seven named tests, no failures/skips; fixed-snapshot independent review passed. Raw evidence in `data/ipc/`.
- macOS public pre-Bind crash/recovery on source `a6a135c`: one top-level/named test passed, actual exit 0, 7.03 seconds, stderr empty. Raw `data/native/prebind-bound.jsonl`, counts and exit retained. FirstHost SIGKILL happened after signed ACK and before any payload; first Owner original Wait exit 2. Public recovery bound attempt2, executed the worker, collected/accepted/ACKed, and both recovered Owner/Host original Wait returned 0. Base intent bytes, queue sequence and 30000ms grant were unchanged.
- The successful native case checks a source-only environment sentinel: absent before Owner bootstrap, present in the worker, absent in Coordinator environment and durable state. Coordinator ran inside the test process for this case; it is not Coordinator SIGKILL evidence.
- Six earlier native failures are retained. Corrected causes include v2 Prepare validation, explicit v2 state loading, lease protocol preservation and a test helper with an unrelated fixed run ID. One pre-Start receipt rejection retained only a prepared row and remains unexplained; it is not reclassified as a pass.
- Twelve copied binaries from the six failed fixtures were removed only after their recorded actor PIDs were absent and paired image hashes matched. Databases, requests, logs and Wait reports were retained. `data/native/owned-failure-binary-cleanup.json` records exact paths/hashes.

## Pending acceptance

Store quota/receipt matrix and final schema32 review, standalone Coordinator hard-loss UNKNOWN, relevant final race/legacy regression, Linux evidence where supported, whole build/vet and source-consistent integration are pending. Initial partial Store matrices and prior static PASS do not cover subsequently found defects.

No real Provider/model/account was invoked; no user installation was replaced; nothing was pushed or released. No Intel Mac/Windows native evidence or physical power-loss proof is claimed. Prior resource measurements do not qualify this candidate; Formal60 remains FAILED.
