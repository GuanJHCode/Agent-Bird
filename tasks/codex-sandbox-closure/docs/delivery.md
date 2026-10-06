# Codex sandbox closure — first kernel reproduction

## Result

The actual Host outer sandbox refuses reapplication even when the inner profile is only `(version 1)\n(allow default)`. This changes the initial hypothesis: the fixed upstream readonly profile's non-noop restrictions are not required to reproduce `sandbox_apply: Operation not permitted`.

This identifies an actual outer/bootstrap boundary, not the exact denied syscall or individual SBPL rule. No product fix, permission expansion, model retry or public Codex enablement is included.

## Changes and provenance

- Tests-only source: `tools/orchestrator/internal/host/codex_nested_sandbox_test.go`. The fixture uses the production state constructor, `codexManagedCommand`, original `process.StartDuplex` Handle and its original Wait/tree proof. Only the worker argv tail is substituted; the actual outer SBPL, environment, cwd and provider pin remain bound. No Session or seal is manufactured.
- Base: `3ecc6dd3f129f25194d1255d3be0862f3d7be872`. No existing product file changed.
- Fixed upstream: `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`. Fourteen narrowly selected official source blobs match the retained complete Git tree. Local receipt: `data/upstream-6b9826/manifest.json`, SHA-256 `a80a36ee4571ac561e6eacc2b22a645d07abd40126aae6504ebe6e05e2bcf7ab`. These investigation assets remain local; they are not account data.
- The 4350-byte inner composition is base policy + unrestricted file reads + empty write/network policy + preferences + empty unreadable-glob policy. SHA-256 `9efcfd9acb165fc744bafe95fcbb99a1b21b784a2d7d2e1203d9c981f85a4318`; no dynamic `-D` parameters, writable roots or platform scratch defaults. This represents the fixed upstream legacy readonly composition, not an observation of the historical tool's complete inner profile.
- Native binary is present as a regular arm64 Mach-O, 222655232 bytes, owner 501, nlink 1, mode 0755. SHA-256 `4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc` matches the product's `codex-cli 0.154.0` pin. It was hashed but not executed.
- `/usr/bin/sandbox-exec` SHA-256: `f3162ae11789a5b296bb3850d493c33ddd52053a03f984b2c4bc34004f4fee99`. `/bin/cat` SHA-256: `61bfb39f273e8290ff7c75866e4b47950577ebe08aa18c8baf4de777996306e1`. Both rounds checked all executable identities before and after every case; the second-round report also persists the exact hash map.

## Verification and exact execution

Go 1.26.8 darwin/arm64; task-private GOCACHE; GOMAXPROCS=2. Tests run from `tools/orchestrator`.

1. Deliberate tests-only acceptance-validator RED: `go test -count=1 -json -run '^TestCodexNestedAcceptanceRejectsUnprovedRead$' ./internal/host`. Original Go PID 31266, Wait 1; 0 PASS / 14 FAIL / 0 SKIP. All thirteen negative cases failed for accepting incomplete observations. This is a validator RED, not a product failure. Preserved under `data/pure-red/`.
2. Final focused pure validation: `go test -count=1 -json -run '^TestCodexNested(AcceptanceRejectsUnprovedRead|TailPreservesActualBuilderScope)$' ./internal/host`. Original Go PID 33717, Wait 0; 15 PASS / 0 FAIL / 0 SKIP, including two top-level tests. Preserved under `data/pure-green/`.
3. Frozen build: `go test -c -o <task>/tmp/host.test ./internal/host`. Original Go PID 33815, Wait 0. All 607 recorded source/fixture inputs were identical before and after build and actor execution. `data/build/freeze.json` binds source hashes and binary SHA-256 `1b614fc76e94fa028365a3f424710a029ab2ce06932a8df0d8bdb41875c5ca43`.
4. One actor invocation: `<task>/tmp/host.test -test.run=^TestCodexNestedSandboxMatrixActor$ -test.v -test.timeout=65s`. Original parent PID 33956, Wait 1, group absent, no timeout, elapsed 1.772 seconds. Actor result: 0 PASS / 1 FAIL / 0 SKIP. This original failure is preserved; no retry was performed.

| Case | Original command shape | Original PID | Exit | Output / ownership |
| --- | --- | --- | --- | --- |
| A | `/usr/bin/sandbox-exec -p <fixed readonly SBPL> -- /bin/cat <owned input>` | 33975 | 0 | Exact 47-byte synthetic token, stderr 0; original Wait/tree/pipe joined |
| B | `/usr/bin/sandbox-exec -p <actual Host SBPL> /usr/bin/sandbox-exec -p '(version 1)\n(allow default)' -- /bin/cat <owned input>` | 33979 | 71 | stdout 0; exact 53-byte public `sandbox_apply` EPERM template; original Wait/tree/pipe joined |
| C | Actual Host outer + fixed readonly inner | Not started | — | Halted after B failed |

The commands in this table abbreviate only the synthetic paths and full profiles. Exact original argv, cwd and environment digest are retained in each `data/matrix-once/matrix/<case>/command.json` and observation. No raw environment was dumped.

All observed cases finished within their original ten-second case deadline and the single sixty-second matrix parent. They retained positive original Handle/Wait/tree proof without timeout or Stop. Source config/auth, their owned references, workspace token, original protected origin, state roots and control sentinel kept their metadata and hashes. Only synthetic stdout and the fixed public error template were exported. No real account content was read or referenced.

Original report: `data/matrix-once/matrix/report.json`, SHA-256 `312dd3691b4b5fc8e583e43eb0c113c3a3707338b3db3aaa8924db142a894cae`. Parent receipt and raw test output are in `data/matrix-once/`. The report's initial classifier remains `inconclusive` because B failed before the predeclared non-noop C comparison; the A/B evidence above is the new causal fact.

## Remaining scope

- The denied bootstrap operation and exact responsible outer rule are unverified. Candidates such as write/link denial or protected-spool read restrictions require a new single-variable, ROOT-scheduled probe; they are not accepted root causes.
- C and the optional fixed Codex sandbox CLI comparison were not executed. The CLI comparison's prerequisite, all A/B/C succeeding, was not satisfied.
- Native model Read, full three-segment continuation, write support and the public Codex entry remain unaccepted. Approval handling stays decline-only and the public guard remains closed.
- No broad Host suite or race actor was repeated: this batch changed only a diagnostic test and ran the focused pure gates plus one bounded actor. ROOT is responsible for independent evidence review before any product fix.

The physical actor token was returned immediately after the original failure and ownership checks. Necessary binary, source pins, receipts and failed runtime remain preserved for ROOT review. No generic runner or unused temporary script was created.

## Second round: single-rule isolation

ROOT reviewed the first report and explicitly authorized N0–N3 tests-only variants on the same synthetic fixture. Production rules were not edited. The fixture retains the exact builder output, then saves the original/variant SBPL and unique rule difference before each original Start.

| Case | Tests-only outer change; inner always noop | Original PID | Exit | Exact token | Exact apply EPERM |
| --- | --- | --- | --- | --- | --- |
| N0 | Replace outer with `(version 1)\n(allow default)` positive control | 34905 | 0 | yes | no |
| N1 | Remove only `(deny file-link)` | 34907 | 71 | no | yes |
| N2 | Remove only global `(deny file-write*)`; retain per-root guards | 34910 | 71 | no | yes |
| N3 | Remove only the exact spool-subpath `deny file-read*` | 34913 | 71 | no | yes |

N0 proves the same process path, cwd and synthetic fixture can reapply a noop profile. Each independent deletion variant still fails. These are negative causal observations; they do not identify a single rule whose removal is sufficient, and do not justify deleting any production guard. Multiple independently relevant restrictions or another restrictive-profile boundary remain possible.

Focused pure validation was rerun after adding the variants: original Go PID 34784, Wait 0; 15 PASS / 0 FAIL / 0 SKIP. The ordinary build was original PID 34808, Wait 0. `data/rule-preparation/freeze.json` binds all 607 stable recorded inputs and `tmp/host-rules.test` SHA-256 `97c58482859b73a8074bf187f4edb1a2d092fdead12d65719f83eb78c0e86d89`.

The one second-round invocation was `tmp/host-rules.test -test.run=^TestCodexNestedRuleIsolationMatrixActor$ -test.v -test.timeout=65s`: original parent PID 34898, Wait 0, group absent, no timeout, 2.245 seconds; 1 PASS / 0 FAIL / 0 SKIP. The PASS means four predeclared known observations were collected and owned, including three permission refusals; it is not a successful native Read gate. All four original Handle/Wait/tree/pipe/deadline assertions and protected/pin invariants passed. No Stop was needed, model/account/turn calls remain zero.

`data/rule-isolation-once/matrix/report.json` SHA-256 `d228e739996dacbe0e59e7a6d8402bf97b484a7e2b08b396c4a0a5e0b947f440` retains the original observations and hash map. Each case retains `outer-original.sbpl`, `outer-variant.sbpl`, `rule-difference.json`, exact command and original output evidence. A separate readback confirmed N1–N3 each remove exactly one unique line with no other byte difference; N0 equals the declared noop control.

The actor token was returned after this round. No further actor, unchanged retry, native Codex bootstrap or product permission fix was started. First-round failure and both binaries/evidence sets remain intact for ROOT review.

## Third and final round: one restriction added to noop

ROOT authorized this final discriminating round because none of the three individual deletions restored nested application. Each P case starts from exactly the previously successful noop outer and adds one independent deny. Inner remains noop and the only final command is the same fixed cat against an owned synthetic token. N0 was not rerun; its exact report hash and scope are referenced in the third report. The explicit synthetic environment shape and original launcher are the same, with fresh owned fixture paths.

| Case | Sole added outer restriction | Original PID | Exit / output |
| --- | --- | --- | --- |
| P1 | `deny file-link` | 35721 | 71, stdout 0, exact apply EPERM |
| P2 | Global `deny file-write*` | 35723 | 71, stdout 0, exact apply EPERM |
| P3 | `deny file-read*` only for the literal owned control sentinel | 35725 | 71, stdout 0, exact apply EPERM |
| P4 | `deny network*` | 35729 | 71, stdout 0, exact apply EPERM |
| P5 | `deny file-write*` only for a fixed nonexistent path under the owned workspace | 35732 | 71, stdout 0, exact apply EPERM |

All five independent restrictions reproduce rejection before cat executes. P5 additionally requires ENOENT before original Start and after original completion; the latter is included in the protection predicate and independently reconfirmed during report readback. Thus a write denial on an untouched, nonexistent target is enough to trigger the observed boundary. This supports a structural restriction on reapplying Seatbelt from an already restricted process on this machine, rather than failure caused by cat touching one protected file. It does not claim every conceivable macOS profile or kernel version behaves identically, and does not identify a private kernel check.

Final focused pure gate: original Go PID 35632, Wait 0; 15 PASS / 0 FAIL / 0 SKIP. Final ordinary build: original PID 35652, Wait 0. `data/from-noop-preparation/freeze.json` binds all 607 stable recorded inputs and `tmp/host-from-noop.test` SHA-256 `9f0c686f26739b466a7a2fa5613f103d5f2041505da74196e9ddbe99ffd23447`.

The sole third-round command was `tmp/host-from-noop.test -test.run=^TestCodexNestedFromNoopMatrixActor$ -test.v -test.timeout=65s`. Original parent PID 35714, Wait 0, group absent, no timeout, 2.403 seconds; 1 PASS / 0 FAIL / 0 SKIP for collecting five declared, owned refusal observations. Every original Handle/Wait/tree/pipe/deadline and source/protected/workspace/pin predicate passed. Model, account and turn calls remain zero.

Original report: `data/from-noop-once/matrix/report.json`, SHA-256 `0930aaf8bac01711f8db0e46400825466adcd1a5371c5541be19c804c6b2f517`. Exact SBPL, single added-rule deltas, commands, outputs and metadata remain beside it. `variant-readback-audit.json` independently confirms each profile is noop plus precisely its sole added line and that P5 remains absent.

The actor token was returned immediately after these observations. Per the ROOT stopping condition, rule trial-and-error ends here. No product guard was removed, no broader permission profile was proposed as a fix, and no native model run was started. The next decision belongs at the execution architecture boundary: a safe tool execution domain must avoid reapplying Seatbelt within this restricted worker inheritance chain while preserving all existing file and network protections. Its design, implementation and native Read acceptance are unfinished.
