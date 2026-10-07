# ACP Coordinator RPC acceptance

Five independent ACP IPC kinds now enter shared bridge admission, cancellation, response timeout and error sanitization. Strict decoding precedes authorization. Coordinator verifies local owner/control files, persistent run owner and exact kernel peer PID/birth; binding RPCs additionally match persisted binding owner. Store rechecks the complete tuple in its transaction. Hook roles, tokens and descendant sender authorization are not reused.

Real socket RED showed all five routes returned unsupported_request before implementation. ROOT final Mac race covers original peer Bind→Activate→Claim→Begin→Receive, durable exact replay, real descendant possessing the same capability rejected at every RPC, forged peer JSON rejected, private-file error sanitization, and client disconnect releasing admission while the database remains locked. Business decisions stay unchanged. Original owned children are joined with Wait0. Hook Coordinator regression and full IPC race pass; vet exits 0. Exact counts, source hashes and terminal outcomes are in `../data/root-receipt.json`. Independent review passed.

Linux arm64 Docker runs the same five top-level ACP socket tests with 14 named cases, no failures/skips, no account/model/network access. Exact CID exited 0 without OOM and was removed. This validates RPC/process identity behavior, not a Linux Provider sandbox or public native integration.

TestACPRPCChild is selected explicitly by child invocations; the parent ACP selector excludes it. An unconfigured whole-package invocation would report the helper's intentional skip. No required behavioral tests were skipped in these runs.

Public controller start, binary pin/preflight, sandbox launch, single ACP session and prompt/response five-window acceptance are still pending.
