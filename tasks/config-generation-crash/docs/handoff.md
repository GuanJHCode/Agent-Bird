# Configuration generation crash WIP handoff

ROOT transferred the user instruction to stop after the currently active small
Owner/Host completed-exit repair. F12 implementation stopped immediately.
Base d0c2a1c; branch test/config-generation-crash-20261008. Not integrated.

Completed here: scoped plan plus the initial test-only draft in
internal/install/config_generation_crash_test.go. It uses the existing private
updateConfigGeneration checkpoint, actual retained Cmd/sole Wait, intended
SIGKILL verification, four durable phases, pending/Doctor/uninstall fences,
exact replay, immutable original files and four independent conflict fixtures.
No production source, Store/Owner/RPC/public contract changed.

Not completed: compilation, first focused run, actual SIGKILL matrix, race,
vet, independent review, actual counts and qualified acceptance. No build or
test command was launched before the stop; there are zero active sessions,
zero owned Cmds and zero actors to clean up. No user installation/account/model,
Provider, container or native runtime was touched.

This is unvalidated code, not a passing feature or behavioral RED. Resume by
reading the plan and reviewing helper lifetime/ready-record bounds first, then
compile and run the scoped selector on the pinned Go1.26.8. First PASS should be
reported as new evidence, not manufactured RED. Report any production defect
before expanding ownership. Physical process crash is not power-loss evidence.

Pending review points include timeout cleanup/output synchronization, the
ready record's privacy/size bounds, exact conflict error checks and platform
behavior. Ordinary/race evidence and original Cmd/Wait results remain required.
Only this task's files were created; no temporary binary or process root exists.
