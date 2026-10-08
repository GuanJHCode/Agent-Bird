# Attributable memory diagnostic — completed, not resource acceptance

2026-10-08 macOS arm64, session93635, 17:35:30–17:55:34. Frozen source bf52539 / runner0c9b7f1; test-only candidate baf8c00f, production base bbaea96/a88f45b (schema29). No real model/provider/account or installation change.

1200 consecutive 1s RSS samples, workers1140s + same pending60s tail; each worker114 durable reports/completed. Original Coordinator Wait0; no identity signals, remaining/unregistered actor or worker-stop cleanup. All five source hashes and private copy pins match. 252 observer commands (120vm_stat+120sysctl+12vmmap) each original wait0, no force, maximum3.55468s. Per-role MemStats: Coordinator242 / Host230 / Owner230 samples with exact enrolled PID/role.

ROOT independently recomputed raw in data/diagnostic-20261008/root-analysis.json; independent f02_state_review independently re-read and confirmed these findings. The f12_resource_harness preliminary claim that swap had no direct evidence was incorrect and is not adopted. vmmap index1 is Owner24586; index2 is Host24585.

| Steady interval (seconds) | Host HeapAlloc floor MiB | Owner floor MiB | Coordinator floor MiB |
|---|---:|---:|---:|
|0–300|2.5678|5.1290|2.8280|
|300–600|2.5691|5.1326|2.9122|
|600–900|2.5697|5.1285|2.9433|
|900–1140|2.5655|5.1256|2.9161|

The observed Go heap low-water marks remain stable over this diagnostic. Owner's final10.996MiB sample belongs to completion/exit activity, not steady execution. Charged RSS peak122.40625MiB includes final helper allowance46,219,264bytes; it is an instrumented candidate measurement, not a resource PASS.

System swap was already in use:13760.00→13925.38MiB; vm_stat Swapouts+86624 and Swapins+71745 pages (16KiB page size). All four vmmap boundaries show nonzero actor swapped pages; first→last Coordinator12.5→17.5MiB, Owner3808KiB→13.2MiB, Host8992KiB→11.5MiB. This supports the presence of system and actor paging during this run.

It does not attribute the previous Formal60 RSS-range failure: that run did not collect these simultaneous metrics. Whole-system page counts are not actor-specific; vmmap has only four snapshots, and this binary adds5s ReadMemStats+Sync with shorter phases and observer perturbation. No forced GC, memory-pressure allocation, threshold change, or production memory change. Formal60 remains FAILED against its frozen128MiB/8MiB contract. No blind hour rerun; another qualification requires a final candidate and a suitable low-interference measurement window.
