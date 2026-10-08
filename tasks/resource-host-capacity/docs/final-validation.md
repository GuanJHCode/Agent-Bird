# F12 four Host capacity — PASS on macOS arm64

Candidate integration a88f45b/schema29 SHA c07a62478f7bede76652a90fcfd37a7ccfe193cc36125053886a0d6d5d140b5d. Final runner/controller fd38dc4; all nine source hashes and128MiB/30s thresholds frozen in data/formal-canonical-freeze-20261008.json. Session17637 exit0; independent final review PASS and ROOT recomputation recorded.

31 raw samples cover30.125678541s. Every sample checks Coordinator+4Owner+4Host and all5 controlled origins, exact PID/birth/argv/nonzombie. Default slots remain2 running and2 ready, with no attempt for queued tasks. Product-only peak111001600bytes plus fifth CLI original wait4 MaxRSS14090240bytes, conservatively applied to all samples, totals119.296875MiB.

The fifth owned child stops before exec; kqueue is registered first. Its only kernel event is EXEC|EXIT (0xa0000000), no FORK. Public submit returns exact admission_deferred with empty stdout and original Wait2, never queued_host_unavailable. DB inventory, capability directory identities and next_ready_sequence5 remain exact.

Both queued workers are observed running before releasing their gates. All4 workers have matching running/result/Exited0 tuples and are reaped, followed by public collect/accept/ACK using unique stable command IDs. All8 Owner/Host identities disappear0.044–0.130s after their completed pending proof (deadline13s). All5 controllers and Coordinator return original Wait0, without cleanup signals/errors/residuals. All9 frozen source/copy pins remain unchanged.

First formal session56965 failed before measurement: sys.executable was a Homebrew symlink; process.startCommand rejects noncanonical paths before child Start. fd38dc4 resolves only the fixture interpreter path. The failed summary/diagnosis remain preserved; product guard was not relaxed. Miscommitted task-created pycache files were removed.

This proves a bounded four-Host window using explicit TEST_FAKE, not real native bridge capability, Provider/model or formal60 flood qualification. The separate60-minute run on51b8e3a still failed its8MiB RSS-window range. Older offline300/idle30 evidence remains tied to its earlier candidate. Owner/Host original Wait evidence is reused from native lifecycle tests; this harness adds exact identity retirement. Intel/Windows native remain deferred.
