# Linux original-process start increment

## Scope

Enable the already-supported original `Start` and `StartDuplex` lifecycle on
Linux only. Keep the existing `Handle`, `Wait`, pipe, identity and Stop
contracts unchanged. Other platforms remain unsupported.

## Verification path

1. Make the existing real-OS Duplex fixture eligible on Linux and compile it
   for the constrained Linux container. Its current failure at
   `unsupported_platform` is the RED evidence.
2. Admit `runtime.GOOS == "linux"` at the launcher gate. Preserve original
   Handle/Wait, birth, executable, pipe and Stop authority contracts.
3. Re-run the focused native lifecycle matrix and record exact named counts,
   raw-log hash, owned CID, and post-exit inspection.

## Review-driven scope correction

ROOT owns the final source and both test tokens. Review found that group
absence depended on a /proc enumeration that could be disturbed by unrelated
PID churn. Use the kernel negative-PGID signal-0 observation instead; never
skip an unreadable PID to infer absence. Preserve cancellation and UNKNOWN
semantics and the separate conservative quiescence scan. Add controlled
kernel-probe RED/GREEN and real persistent-descendant Stop evidence. Final
macOS full race and Docker Linux full process tests replace the initial
nine-case acceptance scope. This is only the F07 launcher increment.
