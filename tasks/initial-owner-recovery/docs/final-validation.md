# F10 initial Bind-before-ledger recovery

Source850ba41, production checkpointce4140b. Schema29 records an immutable initial recovery proof with bounded full-run queue/budget snapshot, original root/database identity, fixed publication facts/auth template and real run/global control-byte escrow. Public Resume recovers only exact current inc1 Bound/never-Applied/zero-attempt after typed Owner/Host death, preserves ready/queued FIFO, publishes the zero ledger without overwrite, and issues initial_ready. The marker survives never-Applied successor recovery; first signed Applied activates the proof and releases the run fence atomically.

Independent production and final Mac increment reviews PASS. ROOT inspected original raw counts; source hashes/commands/results are in data/final-receipt.json and data/mac or data/linux.

- macOS ordinary new Store/API/migration6 top18 named PASS;7 adjacent/migration top-level cases from store-first remain valid (the5 new API cases initially failed solely on an unprivate fixture root). Host3 top19 named PASS, plus1 subprocess-only helper SKIP.
- macOS race Store+Host37 named PASS,1 subprocess-only helper SKIP,0 failures.
- Public initial Bind-only→production inc2 Resume: macOS ordinary/race and Docker Linux each1 PASS,0 failures/skips. Initial submit intentionally returns queued_host_unavailable/exit2 after source_host_start_timeout but preserves the control receipt. Both Owner/Host incarnations use original Wait0; actual worker Exited0/reaped, original FIFO/30000ms grant/one attempt, activated proof, immutable replay, collect/accept and Coordinator original Wait0 are asserted.
- Linux arm64 ordinary Store18 named PASS; Host19 PASS/1 helper-only SKIP including two real publisher SIGKILL/original Wait/reopen windows. All3 exact owned containers exit0/non-OOM, removed and confirmed absent. Binaries removed only after recorded SHA matching.
- Go1.26.8 whole go build ./... and go vet ./... exit0; modified Go files already gofmt-clean.

Earlier root-permission and missing controlled-submit environment fixture failures remain preserved; they are not production failures or successful runs. SIGKILL proves process crash replay, not storage power-loss. Store pinning detects persistent namespace replacement; it is not a custom VFS or same-UID malicious ABA defence. No Linux race, Intel Mac/Windows native or real Provider/model was run. This closes initial Bind-before-ledger only; pre-Bind lost original handle, pending authorization origin rebind and broader F10/F01–F13 gaps remain.
