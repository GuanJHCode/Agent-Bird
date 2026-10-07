# ACP origin contract milestone

The separate ACP owner DTOs reject duplicate/unknown/missing/null fields and
enforce v1, local owner, bounded identities and positive signed 64-bit numbers.
Canonical domain-separated request IDs bind the complete batch/attempt and
protocol receipt, excluding separately authenticated paths/secrets. Golden
keys were independently computed. The typed ACP renderer retains all task
receipts and caps final UTF-8 prompt bytes at 8192; Hook rendering rejects ACP.
The distinct adapter manifest identifies the protocol adapter, not Provider
or tool-profile qualification.

Go1.26.8 final contract+codec race: 56 top-level / 301 named PASS, 0 fail/skip;
both packages finished PASS. Vet exited 0. Initial feature RED logs are missing
API compile failures, not behavioral regressions. The earlier contract race
was 44/289, codec 11/11; the final run also includes the explicit manifest pin.
Raw compressed logs and source hashes are in root-contract-codec-receipt.json.

Independent read-only review passed the contract and codec snapshots. No Store
authorization/state machine, Coordinator routing, launch profile, public
controller, native or real Provider acceptance is claimed by this milestone.
