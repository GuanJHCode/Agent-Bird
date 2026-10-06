# F05 indexed review implementation

- Goal: add opt-in `review_index` metadata to candidate review actions and deliver an immutable, bounded Git text index through Grok prompt files.
- Boundaries: this worktree owns the F05 action, Git index, Host delivery, Store gates, and capability checks; no owner-runtime or schema migration changes.
- Milestones: define the index and its receipt binding; use it in Host Grok preparation; gate indexed review/integration through Store and coordinator; add regression tests and verify with Go 1.26.8.
- Frozen limits: 512 paths / 64KiB path bytes / 8MiB logical text / 32KiB chunks / 2048 chunks / 64 entries and 64KiB per page / 1..63 shards / 384KiB provider input / 1MiB manifests-artifacts / 8MiB Git blob reads.
