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

## Final candidate checks (production db4c267)

- Complete proof-loader/corruption and quota Store race: 9 top-level / 23 named PASS, no failures/skips, raw `data/checks/store-final-race.*` (149.853s). This includes one-byte-short run/global admission, successor accounting, retained escrow, immutable base/effective intent, retry/bind gates, and signed/history corruption checks.
- Legacy schema/actual COMMIT rollback/32 proof preservation: 4 top-level/named PASS, `data/checks/store-legacy-final.*`; contract 3 PASS in `contract-v2.*`. Older migration failure occurred before COMMIT because legacy bound inspection queried absent schema32 tables; the explicit version/absence gate fixes it.
- Full Go build and related six-package vet exit 0; Linux arm64 CGO-disabled Store/CLI test binaries compiled. Exact commands, source and binary hashes in `data/build-final/`.
- Linux Store: 5 top-level / 19 named PASS; native controlled public cases: 3 top-level/named PASS, including new pre-Bind retry, Coordinator loss UNKNOWN, and legacy initial Bind recovery. Container receipts/logs in `data/linux/`; ROOT independently parsed outcomes, inspected each exact CID absent, verified binary hashes then deleted only owned test binaries.
- macOS final native race batch: pre-Bind retry and legacy initial Bind recovery PASS. Coordinator-loss failed before SIGKILL because the fixture awaited submit's five-second Host-ready timeout and Owner had already recorded its real Wait. Raw preserved in `native-final-race.*`.
- First fixture timing correction 4d2fff5 failed differently: still-live submit polling automatically started a replacement Coordinator after SIGKILL; manually started second Coordinator returned busy. Correct UNKNOWN proof did not qualify missing original Wait/cleanup. `coordinator-loss-final-race.*` remains FAIL. ROOT preserved the failure DB, authenticated `stop_task` directly on the existing socket (task was ready with zero attempts), and observed natural idle exit. No PID signal/new Coordinator was used; the missing original Wait is not invented. `data/native/auto-restart-cleanup.json` records this cleanup.
- Fixture 998845d cancels and joins public submit polling after signed ACK, receives its real original CLI Wait/control, and only then kills original Coordinator. Independent static fixture review PASS. Linux delta 1 top-level/named PASS and exact owned container/binary cleanup is retained in `loss-polling*` / `root-loss-polling-cleanup.json`.
- Latest Mac delta still FAIL: `invalid_ipc_message` before Start (one prepared receipt, no child), raw `coordinator-loss-polling-race.*`. This repeats the earlier intermittent FD-handoff symptom; its cause remains unproved and blocks integration. An isolated owner-launch-fd-diagnostic worktree is investigating the exact syscall/validation point, not claiming success from repeated runs.
- Three latest failed Mac fixture copied binaries were removed only after all task-specific executable paths were absent from a process snapshot and hashes matched durable attempt executable pins. Requests, DBs, source logs and original Wait files remain. See `data/native/final-race-failure-cleanup.json`.

## Pending acceptance

Identify/fix the intermittent Mac descriptor-handoff failure with targeted regression, repeat only affected validation and review, then source-consistent local integration. No current integration claim is made. Full proof loader and quota have independent PASS on db4c267; any later IPC change needs its own review and affected checks.

The initial proof RED was observed in tool output but not saved as an artifact. An accidentally broad Store run on the worker's older d380 baseline was interrupted (exit130, 444 top-level /1494 named terminal events, 148 FAIL); it lacks ROOT's schema32 fixture/updated quota-oracle changes and is not acceptance evidence. Raw remains in data/proof; the final focused race and migration evidence is scoped above.

No real Provider/model/account was invoked; no user installation was replaced; nothing was pushed or released. No Intel Mac/Windows native evidence or physical power-loss proof is claimed. Prior resource measurements do not qualify this candidate; Formal60 remains FAILED.
