# F12 idle30 — macOS controlled acceptance

Frozen source `1cf8e92`, runner SHA `b771c29740af8961db7f223a5171fc8eed00c83726ed9f531f8137029d7c5a3b`; candidate SHA `41663f7ce224494bc529f0695540cca9a5591bd8d92df5c7557ee29143d49819` (product source `51b8e3a`, same bytes as offline300 and the failed formal60). No product change.

Run session17917 exited0. `data/formal-acked-20261008` contains 236 raw samples: all durable nonidle counts0, Coordinator original child alive through29.90428175s and naturally exited/reaped Wait0 at30.05453333s after first idle observation. ROOT independently recomputed these values from JSONL, checked the public ACK receipt/Wait0, pins and cleanup. Owner/Host exact enrolled identities were absent within13s of the completed pending-result proof; the worker's durable Exited0 matched its original running tuple and was reaped. Controller stayed alive and exited via EOF with original Wait0. Cleanup signals/errors/remaining are all empty.

Public flow: owner-bind/submit gated fake worker → exact lifecycle pending proof → collect → accept → ACK same collection/event tuple → no further product IPC during idle measurement. Request files0600, state dirs0700, private copied executable0500. Thresholds remain29–32s and Owner/Host13s. Independent ACK increment review PASS; final evidence review recorded in receipt. The existing deadline-crossing guard PASS from6080a77 remains applicable; unchanged code was not retested.

The first formal run `data/formal-20261008` FAILED: omitted delivery ACK left all213 samples nonidle; Coordinator needed a cleanup TERM. It is retained as fixture-failure evidence, not reclassified.

This establishes one controlled macOS arm64 idle retirement. It does not establish four-Host capacity, overall F12, real Provider/model, Intel Mac, Windows, or the60-minute RSS criterion (which failed separately). Owner/Host original-Wait evidence is reused from prior lifecycle tests; this harness adds exact identity retirement evidence, not new original handles for those actors. No user installation changed; no push or release.
