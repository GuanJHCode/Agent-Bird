# Final Git process tracking validation

Final `gitops-worker integrate` now uses the original process handle and durable intent/spawn/exit journal while retaining the normal Git environment, repository hooks, and signing configuration. Candidate commands keep their existing restricted environment. The worker locks its journal before any Git command and refuses replay after an integration intent, incomplete/unknown process chain, malformed record, or partial original identity.

Valid regressions cover missing final process records, malformed replay chains, partial identities, and a real post-merge hook configured through `GIT_CONFIG_*`. The last case failed before the final-environment separation and passed after repair. Both repository-configured and environment-configured hooks execute in the real materialize/compose/integrate/cleanup chain.

Final ordinary tests: gitops 48 top-level / 99 named PASS; gitopsworker 4 / 21 PASS. Targeted native race: gitops 3 / 12 PASS; gitopsworker 4 / 21 PASS. All final runs have zero failures/skips. Final package vet passed. Logs and source hashes are recorded in `../data/root-receipt.json`; import grouping was normalized after the full runs, without changing behavior, and vet reran afterward.

Independent review found the environment-loss and partial-identity gaps; both have effective RED/GREEN evidence and the final change was reviewed. This is one F04 milestone. It does not enable candidate LFS/filter/hooks/signing, prove actual signing-helper behavior, or close the remaining F01–F13 work. No real Provider, installed-copy replacement, push, or release occurred.
