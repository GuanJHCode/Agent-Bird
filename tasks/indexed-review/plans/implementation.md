# F05 indexed review implementation

- Goal: add opt-in `review_index` metadata to candidate review actions and deliver an immutable, bounded Git text index through Grok prompt files.
- Boundaries: this worktree owns the F05 action, Git index, Host delivery, Store gates, and capability checks; no owner-runtime or schema migration changes.
- Milestones: define the index and its receipt binding; use it in Host Grok preparation; gate indexed review/integration through Store and coordinator; add regression tests and verify with Go 1.26.8.
- Frozen limits: 512 paths / 64KiB path bytes / 8MiB logical text / 32KiB chunks / 2048 chunks / 64 entries and 64KiB per page / 1..63 shards / 384KiB provider input / 1MiB manifests-artifacts / 8MiB Git blob reads.

## Implementation and verification (2026-10-07)

- ROOT owns this worktree. The independent worker now performs read-only reviews; owner-runtime remains a separate worktree and acceptance scope.
- `503129c` / `831b7a1`: metadata-only streaming index, valid plan admission and three actual controlled Grok consumers followed by Git integration.
- `8394613`: common manifest/coverage contract and every shard's Host guard before the mutation journal; no index resampling at integration. `ef4e249`: Store accept-time durable source, launch, configuration, dependency vector and coverage validation, with idempotent acceptance after database reopen and removed receipts.
- The current batch adds a public CLI DAG through real Coordinator/Store/Source Host, 65+ context files and UTF-8 chunks, addition/deletion/rename, three actual prompt-file consumers and Git mutation. It kills only the idle test-owned Source Host after validation exits, recovers twice without changing its artifact or execution identity, then reopens Coordinator after all acceptance and repeats every decision with the receipt files removed. Both original Coordinator handles return Wait=0. This does not prove live-worker takeover after Host death.
- A real temporary installation and published generation exposed Grok configuration admission rejecting explicit/CLI-default model selection. The minimal fix permits those choices while preserving generation proof; missing/ambiguous choices and unsupported providers retain their gates. No new saved-default field or schema was added.
- Actual Git index boundaries cover unchanged binary/invalid UTF-8/LFS/symlink/submodule refusal, 512 vs 513 paths, 64KiB vs 64KiB+1 path bytes, 8MiB vs 8MiB+1 logical text, and hydration at 384KiB vs 384KiB+1 before blob reads. Contract tests cover 2048 vs 2049 chunks, 64 vs 65 page entries and escaped manifest size. Real prompt files cover 384KiB including envelope vs +1, JSON expansion and task prompt 64KiB vs +1.

## Evidence

Go 1.26.8, macOS arm64, TMPDIR=/private/tmp. All following completed with zero failed or skipped tests; counts include named subtests. Raw logs remain in this worktree's `tasks/indexed-review/data/`.

| Log | Scope | Passed | SHA256 |
| --- | --- | --- | --- |
| `root-e-config-final-race.jsonl` | Install / Store / CLI configuration race | 53 top / 168 named | `e3106446f06cfddfceb4a52fa8ae49879073caf6dc8ced93308ed38d86a183f5` |
| `root-f-boundaries-race.jsonl` | Contract / Gitops / Host boundaries race | 20 top / 72 named | `a1aea1dba939606da04eb91cf15dc2b7e30c15788e4202aad654b8ecae5f901b` |
| `root-e-cli-final-race-v2.jsonl` | New CLI chain, legacy CLI chain, native Host success/rejection/config cases | 3 top / 33 named | `4401b9f7ed0f492a6c08ce79d4b7f01f5647bc01c2bf5e3129182a4211c39905` |
| `root-f-admission-race.jsonl` | Store indexed plan shape and CLI primary/fallback capability admission | 2 top / 10 named | `2288ee78d031a4e52d2b5cf11be531dc37bdb0f0ada5babb3a3cf0a0d5aa4789` |

`go build ./...`, `go vet ./...`, and `git diff --check` passed on the final code. Independent reviews passed for the configuration product change, boundary tests and rename chain. The CLI test's separately built Coordinator executable is a normal binary; `-race` instruments the Go test and copied Provider fixtures, not that child binary.

The first expanded native regression exposed old fixture grants missing LaunchIntentHash; correcting the fixture retained the product guard. The failed `root-e-cli-final-race.jsonl` is preserved and is not a passing result. Earlier full-suite attempts without final tool output are UNVERIFIED; they ended before this batch and their historical PIDs are not ownership handles. Do not infer their outcome or act on those PIDs.

## Limits and remaining scope

Every Grok shard carries the full manifest. The 384KiB actual encoded input ceiling, including task prompt and instructions, can reject a manifest even when it fits the independent 1MiB artifact ceiling. More shards cannot shrink full metadata. A valid page cannot approach 64KiB while limited to 64 ordinals in 0..2047; the meaningful page boundary is the entry count. The contract permits 2048 smaller chunks; the producer normally emits 32KiB chunks and may hit another limit first.

Existing transactional quota, attempt and active-budget gates remain on admission/dispatch; accepting already completed work does not acquire another lease or require new execution budget. Indexed receipt guards do not bypass those paths. No new post-execution budget rejection was introduced merely to satisfy a test matrix.

Evidence uses controlled protocol consumers and test-owned installations. Real-model quality, Intel Mac/Windows native runs, original installed TUI consumption, live-worker ownership migration, final combined packaging/install and publication remain separate work. No user installation was replaced and nothing was pushed or published.
