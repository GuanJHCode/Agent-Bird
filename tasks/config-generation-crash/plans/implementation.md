# Configuration generation process crash Implementation Plan

> For agentic workers: use executing-plans inline; no delegation.

Goal: qualify original configuration publication after an actual SIGKILL in a
private temporary installation prefix. Base d0c2a1c; test-only ownership.
Architecture: existing private updateConfigGeneration per-call checkpoint stays
inside the held install lock; a bounded test-only helper emits/fsyncs a ready
record then waits. Parent retains original Cmd/sole Wait, checks live lock
refusal, kills that exact handle, validates SIGKILL Wait, then calls actual
UpdateConfigGeneration again. No reconstructed Wait, Provider or user install.
Tech stack: pinned Go1.26.8; TMPDIR /private/tmp, offline dependencies.
Spec: capability-closure F12 plus c11-owned-generation-decisions Publication/use.

Only new internal/install/config_generation_crash_test.go and this task directory
may change. Production defects are reported to ROOT before any repair. Partial
or malformed records remain failclosed; there is no prefix GC or repair policy.
This is process crash evidence, not power-loss or public supervised CLI coverage.

- [ ] Add owned temporary package fixture, bounded private child and original
      Cmd/Wait lifetime. Default helper invocation explicitly skips.
- [ ] Test intent/manifest/active/receipt actual crash: immutable original
      request, pending consumer/uninstall refusal, Doctor complete tree unchanged,
      exact replay/ref/revision2, preserve existing manifest/history/active inode
      where applicable, second replay immutable, healthy final Doctor.
- [x] Independent crash fixtures for changed request, foreign-scope Expected,
      active inode drift, malformed stage; each rejection preserves actual tree.
- [x] Run first actual focused selector honestly: it exercised all eight
      subcases and recorded every original Cmd/Wait. Replay passed for all
      four phases and three conflict variants; `active_inode_drift` revealed
      a production write after its conflict result. See
      `docs/focused-failure.md` and `data/focused-actual.log`.
- [x] Keep the preserved-tree assertion. The repair opens existing owners
      read-only before creation and checks an unstaged intent's original
      active identity before writing its manifest.
- [x] Format and run the focused ordinary selector once after the final repair,
      then its race and vet checks. Record actual counts, original Wait and
      retained roots/cleanup before independent review.
