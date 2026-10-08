# F12 controlled dual-worker resource acceptance

Formal mode requires exact candidate, worker, runner SHA-256 and the complete threshold object in `--freeze-manifest`. Each executable is copied from a single no-follow FD to an exclusive private 0500 file, verified before launch and at the end. No model, account or installed binary is used.

Both fake workers use the actual supervised Owner/Host chain for 3540 seconds within a 3600-second tool measurement. Each emits 1 MiB/s total stdout/stderr for two 20-minute phases: valid Claude-shaped assistant lines, then unknown raw lines. Both phases add one 4 MiB line every 30 seconds. The last 19 worker minutes are quiet; the final 60 seconds of the tool window preserve the exact pending results after worker completion. Every 10 worker seconds (354 reports per worker), each worker invokes the production CLI report capability path with a <=4 KiB schema-valid progress request and verifies the exact durable ACK. Original helper Wait, CPU/RSS and receipt are retained. This proves the controlled fake-adapter drain/report path, not real Provider parsing or model quality.

Sampling is every second. Either worker exiting or changing identity before the frozen shared start + 3540 seconds fails. Completion must close within five seconds with exact Exited0/reaped, completed stats and report count; each remaining tail sample preserves the same attempt/segment/result pending. Owner/Host may naturally retire after completion and must be reaped within 13 seconds of the durable completion; Coordinator remains alive for the full hour. Candidate processes and descendants are counted; only the two load-generator worker PIDs are excluded. Two report-helper peak RSS allowances are conservatively added, including potential double counting. Product RSS must stay <=128 MiB. In each 20-minute phase the first five minutes warm up; the remaining three five-minute medians must differ by <=8 MiB. These criteria are frozen before formal execution. CPU counters and raw samples are retained.

The runner owns the original Coordinator Popen, checks durable worker Exited0/reaping, uses public collect/accept, and observes exact Owner/Host identities naturally disappear. Owner/Host original-Wait behavior is separately covered by native lifecycle acceptance, not invented by this runner. Cleanup honors 13-second worker/Owner windows and the original Coordinator Wait; forced cleanup fails acceptance. State, raw output, partial samples and content manifest remain on failure.

`--smoke` uses 20 seconds and reduced bandwidth. It checks the harness only and does not establish the frozen long-time RSS/no-growth contract. The separate five-minute offline phase is not implemented by this runner; `--offline` explicitly refuses and no offline verdict is claimed.

ROOT smoke 1 failed because the fixture sent a forbidden report payload field; the production schema remained unchanged. Smoke 2 passed but independent review found its RSS summary omitted future helper maxima from early samples (102.765625 reported vs 103.84375 MiB recomputed). The final runner retains online fail-fast checks and recalculates every sample/window with both final helper maxima after all receipts arrive. The late-helper regression covers an early 120 MiB product peak plus a late 12 MiB helper: it must reject 132 MiB; 8 MiB at the exact 128 MiB limit passes.

Final smoke 3 passed all stages and exact report receipts with 103.390625 MiB conservative peak, 20 samples, original Coordinator Wait0 and zero remaining processes. Its runner digest matches the committed source. The three Python guards passed. These are harness evidence only; no formal run has yet occurred.

The first native-observer formal attempt failed at 3599 samples: the old harness
requested 3660 seconds without a policy, which the existing segment contract
clamped to 3600; worker execution began before the shared measurement start.
That attempt remains failed. The new schedule leaves 60 seconds of segment
headroom, preserves both full 20-minute floods and the >=10-minute dual-worker
requirement, and changes neither production budget limits nor RSS thresholds.
