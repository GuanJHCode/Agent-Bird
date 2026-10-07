# RPC shutdown contract

Scope: isolated duplex/RPC transport work from the frozen detailed-Codex plan.

- `duplexStream.CloseWrite` closes stdin under its own `sync.Once`; `Close`
  reuses that operation and then closes stdout.
- `codexrpc.Client.CloseWrite` freezes outbound admission under `mu`, rejects
  outstanding jobs without mutating them, and waits concurrent callers on the
  same underlying half-close. It reports deidentified transport failures.
- `ReadEOF` is latched only by the original read pump after `ReadSlice`
  returns `io.EOF`, before partial-frame classification. Full close and
  cancellation do not set it.

Evidence is retained in `../data/`: runtime pipe interface RED/GREEN logs,
focused RPC RED/GREEN logs, and uncached normal/race codexrpc runs.
