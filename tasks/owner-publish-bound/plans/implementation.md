# Original-owner publication shutdown bound

Base: integration 19f5a338ce6be91ac00cf72c34a6b7c65c7abc6c. ROOT is the only writer and macOS/Docker test-token holder; reviewers are read-only. No installation, model/account use, push or release.

Contract: authenticated terminal seals admission; TERM at most 10s, original worker Wait/group proof by 11s, final flush at most 2s, original owner process must return and original Cmd.Wait finish within 13s. Preserve UNKNOWN/incomplete on missing proof. Reserve internal cleanup total 12s for process-return margin; do not relax the external 13s test.

Milestones:
1. Native RED: real enrolled owner, real signed launch and terminal, stubborn worker with persistent same-group descendant; freeze actual publisher after a real Running event RPC has committed and returned via existing private cfg.rpc. The publisher retains its serialization lock forever. Verify original owner Wait without acceptance-time kill/release, exact local unique Exited and group absence.
2. Replace uninterruptible publication lock admission with a context-aware bounded token, no goroutine per lock waiter. Native external13s GREEN plus focused cancelled-waiter and serialization regressions.
3. Native durable-return case: nil-default private post-Spool.Append seam blocks only after actual Exited append succeeds, before sequence notification. Require bounded original owner Wait and exact persisted exit proof. This is not physical fsync injection. Reuse existing blocked-admission regression.
4. Independent stable-source/test review; focused normal/race/build/vet, record original session statuses/counts/scopes. Keep failed native evidence, audit recorded actors, remove only owned unnecessary artifacts.

Open beyond this increment: physical disk failure matrix, escaping/uncontained descendants, finite signed seal/data-only dead-owner replay, all other F01-F13 requirements.

Status: this scoped publication shutdown increment is validated (normal55554, race71375, build/vet, Linux pure gate/cleanup) and independently reviewed. Actual product RED66394/84392 was repaired without relaxing the 13s external bound. Durable fixture initial failure46191 was a wrong SQL column, corrected and rerun; not a product bug. See docs/validation.md and data/root-receipt.json for scopes and original handles. F10 as a whole and F01-F13 remain OPEN.
