# Collection receipt accounting, schema24

Receipt metadata previously entered neither durable global nor run usage. Standalone collection had no admission, and Hook/ACP forecasted existing receipts repeatedly. Schema24 transactionally rebuilds the receipt table with task FK, rejects invalid history, backfills four UTF-8 text byte lengths exactly once and installs INSERT/DELETE accounting plus immutable UPDATE protection. Standalone and Hook claim aggregate only missing receipt cost in their Store-locked transaction. ACP integration is a separate follow-up.

ROOT captured behavioral RED for missing charge/admission/history upgrade. Final full Store ordinary suite passed; then added schema22→23→24 history and missing-receipt exact-quota tests, covered by final targeted race and Linux native modernc run. Production source was unchanged between full Store and final focused checks. Counts, source hashes, package outcomes and container identity are in `../data/root-receipt.json`. One intermediate test helper compile failure is retained and is not product regression evidence.

Independent production and test reviews passed. Tests cover UTF-8 bytes, no-growth replay over reduced limits, concurrency, immutable/collision rejection, delete/refund/transaction rollback, failure at every migration stage and successful reopen, invalid orphan/NULL-id history, exact Hook quota, and chained migration with FK restoration. Linux container exited 0 without OOM and was removed by exact CID. `go vet ./internal/store` exited 0.

This completes this accounting defect only. F02 retention/delete/tombstone and load/physical-fault acceptance remain open.
