# Existing vmmap region analysis

This is an offline classification of the twelve raw `vmmap` tables produced
by the completed, test-only 20-minute memory diagnostic. It starts from the
already verified role mapping in `root-analysis.json` and does not run a
workload, query a live process, change production code, or alter the Formal60
threshold or verdict.

## Inputs and reproducibility

The input is the `0000`, `0300`, `0600`, and `1100` second `vmmap` snapshot
for each coordinator, owner, and host in the source diagnostic worktree. The
source mapping is checked against `root-analysis.json`, whose SHA-256 is
`1c2cc5b8f389bdaa7429035cda876ad0c3ca0abe246a50d7ef0496ccc7fdfe4f`.
Every raw source pathname, PID, timestamp, and SHA-256 is retained in
`data/vmmap-region-summary-20261008.json`; all quantities are bytes.

Run only the local parser:

```sh
python3 tasks/resource-region-analysis/scripts/analyze_vmmap.py \
  --input-root /path/to/diagnostic-20261008/system \
  --root-analysis /path/to/diagnostic-20261008/root-analysis.json \
  --output tasks/resource-region-analysis/data/vmmap-region-summary-20261008.json
```

The parser requires the existing role mapping for all 12 files. Its categories
are literal vmmap labels: `MALLOC*`, `VM_ALLOCATE`, exact `mapped file`, labels
containing `sqlite`, `__DATA*`, selected library/code labels, and other. In
particular, `VM_ALLOCATE` is not asserted to be Go runtime memory.

## Observed diagnostic-region changes

Across all three roles, selected library/code-label resident bytes declined
from 625,871,664 at 0 seconds (596.878 MiB) to 368,962,560 at 1100 seconds
(351.870 MiB), a 245.008 MiB decrease. `VM_ALLOCATE` resident bytes likewise
declined from 35.438 MiB to 22.051 MiB. These are vmmap table resident columns,
not the product RSS contract, and they align with the independently recorded
swap activity rather than evidence of monotonically retained resident pages.

The exact `mapped file` label was 0–32 KiB resident in every snapshot. No raw
table had a region label containing `sqlite`, so `sqlite_named` is absent from
the JSON groups. This rules out a large *named vmmap SQLite or mapped-file
region* in this diagnostic only. It does not rule out SQLite allocations
accounted under another label, other file-backed mappings, or behavior in the
prior Formal60 binary.

`MALLOC*` resident bytes were at most 0.875 MiB in any individual snapshot;
their swapped bytes remained nonzero. The result therefore narrows this
diagnostic's visible variation toward library/code residency, VM allocation,
and paging, rather than a large literal mapped-file/SQLite-labelled region.

## Boundary

The diagnostic source was the test-only `0095b3f` line later integrated at
`17b3370`; its own final report already marks it `not_applicable`. Formal60
did not collect simultaneous region tables, and the diagnostic binary adds
ReadMemStats, sync, and observers. This analysis cannot attribute the old
13.015625/21.609375 MiB Formal60 ranges, cannot establish a resource PASS, and
does not replace the frozen 128 MiB / 8 MiB contract or its FAILED verdict.

ROOT independently checked all 12 raw hashes and every table row using a separate decimal column parser. Resident/swapped sums match the retained classification. Source K/M/G values are rounded vmmap displays: byte conversions and derived MiB totals express those printed values, not exact physical byte measurements. Shared library regions may appear in multiple processes; their sum is neither unique physical residency nor the product RSS contract.
