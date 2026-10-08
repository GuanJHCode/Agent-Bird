# Integrated configuration crash qualification

Integrated from b19804e through 591b79f. The three modified Go source files
match worker 0a2157c byte for byte (see data/integrated-receipt.json).
Independent final review passed after fixing the required-directory fallback
and adding valid-stage identity-drift qualification.

Mac final ordinary/race each cover 2 top-level and 10 subcases, with 10 original
SIGKILL Cmd.Wait receipts and zero failures/skips. Linux arm64 offline non-root
container covers the same 10 subcases; its exact CID exited 0, was non-OOM and
was removed. ROOT checked raw reports/receipt; it reuses unaffected results of
the earlier full install suite (111 top/355 named PASS, two helper skips).
Integrated smoke covers the two review regressions (1 top/3 named PASS, no
failures/skips); the whole integrated Go module builds successfully.

This closes only the local physical-process configuration publication crash
substage. It is not power-loss, Intel/Windows native, supervised public config
entry, or the same-candidate 60-minute two-worker resource qualification.
No account/model/provider invocation, user installation replacement, push or
release was performed.
