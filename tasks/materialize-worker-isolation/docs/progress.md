# F04 materialize worker route

ROOT owns this isolated worktree; base integration d42c4fd. Source investigation found that normal Host materialization already uses ManagedMaterialize, while the gitops-worker materialize route called raw Materialize. The worker route therefore admitted unsupported Git execution extensions and omitted per-command original-process journal facts.

The bounded change delegates this route to existing ManagedMaterialize with its candidate journal. It preserves fixed /usr/bin/git, existing restricted candidate environment, extension refusal, post-materialization identity/content checks and original Handle/Wait/tree handling. It does not implement isolated support for executing LFS/filter/hooks/signing; such extensions remain rejected on candidate materialization. Final integration's existing hook/signing contract is unchanged.

Actual old-code RED: 2 top-level /5 named failures. Three extension configurations (filter, hooksPath, signing) incorrectly materialized; the ordinary successful route had zero complete Git-process journal chains. After the one-line production change, the same two tests pass (2 top-level /5 named, no failures/skips). Tests exercise BuildInvocation/Run with real Git children, not a newly launched worker CLI process. Raw outputs, actual exit codes and parsed counts are under data/checks.

Final race, build/vet and fixed-snapshot independent review pending. Linux Git native is not available in the already-inspected cached images; no image pulls/install changes are made. No real Provider/model/account, user installation replacement, push/release. Overall F04 remains open.
