# Public owner succession fixture

Baseline `3669cb8`; writer owns the new CLI native test, minimal TestMain
basename dispatch, authorized private Wait observer in `original_owner.go`,
and scoped task artifacts. No Store/contract/schema or other production change.

ROOT authorized `runInitialOriginalOwnerWithWaitObserver(ctx, bootstrap,
observer func(initialOwnerWaitObservation))`, preserving the original wrapper
signature/default nil observer. Private observation contains PID, actual
`*os.ProcessState`, and original Wait error only. No Cmd exposure, public flag,
global hook or product environment backdoor. The one existing Wait goroutine
calls the observer once after actual Cmd.Wait, then publishes its normal done.
Fixture observer output uses a private inherited pipe with a finite deadline.

The fixture CLI is a task-private copy of the test executable. All public
submit/status/stop/resume/collect/accept/serve and original-owner commands call
the actual production `run` entrypoint. A test-only source-host launcher keeps
the original registered FirstHost Cmd; its child consumes the actual bootstrap,
normalizes its real identity, and calls the existing production initial-owner
runtime with the private Wait observer. This launcher is explicitly substituted
for installation-level Source startup; it does not duplicate enrollment,
signatures, Store retirement, handoff, execution or transport lifecycle.

The worker is the existing bounded dual-marker budget fixture. Public Stop must
produce one original nonzero Exited and settled positive charge before old Owner
SIGTERM. Original Owner and FirstHost Wait records plus exact death observations
must exist before public stable-ID Resume. Original origin stays alive. Resume
must create incarnation2, same attempt/new segment, preserve all non-scope ledger
fields and previous budget rows, and grant only remaining active budget. The
second real worker must belong to the new Owner; release -> result_ready/exited
-> actual public collect/accept -> completed. Exact command replay must not Start
again. No hand-written SQL retirement/death or forced Scope repair.

Steps: write tests/compile-only API RED; implement API scaffolding without Wait
observer behavior; obtain ROOT's exclusive native window for actual observer
behavior RED, then minimal wiring and focused native flow; preserve all raw
outputs, exact Cmd/Wait/cleanup/counts and review gaps. ROOT currently owns native
resources; no test actor is started before explicit window transfer.

Checkpoint: actual core flow reached completed, strict successor retirement fails
with actual host_retired + conflict. ROOT has received native resources for
production diagnosis; current source frozen. See docs/report.md and raw data.
