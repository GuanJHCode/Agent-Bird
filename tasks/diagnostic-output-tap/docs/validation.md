# Output observer validation

Process output tap is ready for Host integration; detailed diagnostics are not yet connected.

- macOS arm64, Go1.26.8, full process race: session21973 exit0,67 top/135 named,0 fail/skip. Includes original Start and StartDuplex children, 2MiB stderr beyond the 1MiB summary, downstream preservation, original Wait/tree/birth, and duplex stdout read only by its original consumer after Wait.
- Docker Linux arm64: crosscompile c648a7 exit0; session45060 exit0,81 top/156 named,0 fail/skip. Cached offline read-only unprivileged container, original process lifecycle tests included. Exact CID and terminal non-OOM/removal proof in data/linux/v1-receipt.json. Linux race was not run.
- Focused build482928 and vet6490d8 exit0. Production/pure and new native fixture independently reviewed PASS. All package source hashes in data/root-final-receipt.json identify the tested snapshot.
- Observer remains a synchronous, borrowed-byte callback. Host must use only bounded non-blocking Capture.Offer; no filesystem work or Wait in the callback. No new duplex reader or goroutine was introduced.
- Native stderr is repeated e bytes: full length/content is checked, but this alone does not prove arbitrary chunk reorder detection. Native fixtures do not exercise Providers, detailed storage, public read/ACK, failure projection or RSS. These remain F02/M2/M3 work.
- Test children completed original Wait and tree checks. The Docker container was precisely removed and inspect confirmed absence. Logs and test binary are retained as evidence. No model account, installation replacement, push or release.
