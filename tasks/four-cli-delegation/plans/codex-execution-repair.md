# Codex execution repair

Authorized outcome: a real isolated Codex coding candidate, without widening the
existing filesystem or tool-network boundary. Root owns writes in this worktree.
Architecture/security agents are read-only. No automatic paid retry.

1. Prove the fixed standalone exec-server can run under a single filesystem and
   network-denying OS sandbox without a model. (Native probe passed.)
2. Add an owner-bound Unix/stdio bridge. Bind the one connector to the live Codex
   child and pinned helper, reject unrelated peers and reconnects. Keep executor
   behind its own OS sandbox and close/confirm its entire tree on every exit.
3. Run parent via app-server under a separate read-only OS sandbox, with only
   Codex and the fixed bridge executable allowed as children. Private environment
   registry has one default and include_local=false. Model transport stays local.
4. Use externalSandbox/restricted for turns; enforce that declaration at the
   executor's OS boundary. Preserve source approval/model/instructions. Reject
   approval requests rather than answering on behalf of the owner. Reject any
   unexpected policy, managed network, HTTP or environment-config request.
5. Translate exact thread/turn completion into the existing Host result protocol;
   retain pin/input checks, timeout/stop accounting, final output and freeze.
6. Native negative tests: protected writes/reads, Git metadata, network, unknown
   peer, disconnected executor, no local fallback, child process cleanup. Review
   stable changes independently before one 120-second real coding acceptance.

The old production admission block remains until these gates pass. No source
home or installed package/configuration is modified by development.
