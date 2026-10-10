# Exact live filter session — private qualification

Qualified source `9faf0174ba662a5dceb21c602bc85fdfceef4e5a`, orchestrator tree `f20d145263afd53569e0dcdd4948907b5fde3437`. This delivers a private original-Broker transformation session. It does not connect Git helpers, expose a public action, perform merge, or authorize cleanup.

The Host must derive exact permits from authenticated B/R evidence. Construction matches the manifest digest and original live Binding, copies/canonicalizes the permit list, and fixes input/output digests and sizes, path/operation and finite call limits. Monotonic sequence, serialized exchanges and sticky failure prevent retries from resetting those limits. Only slots 0→1 and the original Broker's pinned tools are used. Output remains in one bounded private buffer until independent byte/hash checks, terminal observation and the locked success commit pass. Close before that commit revokes output; later Close only affects future calls. Close never delegates cleanup or closing to the borrowed Broker.

`BrokerObservation` is an atomic, Go-only observation of the original object, including frozen identity and lifecycle. It cannot reconstruct or reactivate a Broker. Execution Binding still becomes zero after Failed/Closed/Removed/Unknown. The session uses observations to distinguish a known rejected tool/producer result from unknown process ownership. Known failures retain rejected terminal observations and permit only the original Host's existing cleanup path. Unexpected lifecycle drift or unproved original Wait/tree is UNKNOWN and yields no output. Future code must combine Exchange return/State with the outer process terminal result; the journal is observation, not execution or cleanup authority.

## Verified evidence

ROOT independently parsed retained command metadata, source/Go trees, raw JSON counts and original exit files in [root-verification.json](../data/root-verification.json). The original sessions were drained normally; no PID scan or inferred termination was used.

| Fixed source | Scope | Result |
|---|---|---|
| `e925c62` | Initial API behavior RED | 1 top/2 named FAIL; compiled successfully |
| `c1642b2` | First focused race | 1 top/23 named PASS |
| `3b81ca2` | Pure + original Broker race | 2 top/30 named PASS; native 7.26 s |
| `7bf26e0` | Known-failure regression RED | 1 top/3 named FAIL |
| `9faf017` | Final affected race | 3 top/35 named PASS, zero fail/skip |
| `9faf017` | Whole Darwin / CGO0 Linux arm64 builds; gitfilter + Host vet | All exit 0; empty streams |

Named counts include parents. The `3b81ca2` green run did not establish qualification: independent review found that Broker's intentionally revoked Binding caused known failures to become UNKNOWN. The improved fake reproduced that actual contract at `7bf26e0`. The final implementation and portable/native oracles independently passed review at `9faf017`. Every earlier failure and intermediate record remains retained; no older long Git/Host tests were rerun.

The final native fixture builds the pinned local actor through original Start/Wait/tree, performs real XOR B→R and R→B exchanges, compares actual output and journal bindings, and proves session Close leaves the original Broker readable. Separate fresh Brokers prove wrong input starts no tool, a tool exiting 7 has original Wait/tree and known rejected terminal, and a partial producer error starts no tool and remains known. All original Broker cleanup results were checked. Both intermediate/final owned roots (`executor-774485786`, `executor-132658574`) were independently confirmed absent.

## Limits

Portable injected unknown/lifecycle drift is synthetic; this leaf adds no forced real UNKNOWN process case. Linux is build-only. A caller-supplied synchronous producer must honor its deadline and writer errors; a later IPC adapter must make reads interruptible. If lifecycle drift occurs inside the terminal journal callback, the earlier record may still describe a known exchange, while the post-callback check returns UNKNOWN. Such a record cannot alone authorize publication or cleanup.

The next layers remain unimplemented here: bounded Git filter.process carrier, original helper/outer Git ownership, and exact target before→after merge verification. Normal merge/hooks/signing must remain intact. No real model/account, user installation replacement, hook registration, push or release occurred. F12 Formal60 remains FAILED; Intel Mac/Windows native remain deferred. The current F13 source64acf package covers the previously qualified target-review stage and does not claim this newer private source.
