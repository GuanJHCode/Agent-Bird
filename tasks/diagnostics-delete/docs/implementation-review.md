# Independent implementation review

Snapshot d71fba8, compared with integration f7db2e8: BLOCKED (read-only review, no duplicate tests).

1. SQL and Go deletion cost omitted the phase TEXT field. The maximum 8 UTF-8 bytes must be prepaid in addition to the fixed64-byte overhead. ROOT added an independent measurement using the actual PRAGMA table columns; the old implementation really failed (3144 charged vs3152 measured).
2. Four historical migration fixture entry points refused schema31 before reaching the safe empty-schema31 removal helper. ROOT extended only their known-version allowlists, preserving every real-history refusal before its downstream downgrade.
3. Retired diagnostics grant and read reauthorization fixtures directly changed reservation states. Schema31 correctly rejected these writes; the read fixture could consequently pass without reaching reauthorization. ROOT made the corruption injection explicit, verified the resulting state, and required the exact authorization error.

The reviewer found no other production blocker above80% confidence. ROOT's targeted RED for items1/2 and GREEN for all three are preserved under data/review-regression-{red,green}.jsonl. GREEN:16 top /68 named PASS,0 fail,0 skip. Incremental independent review of0164436 is PASS: all three blockers resolved, no new blocker or non-blocking issue. Reviewer reused the preserved RED/GREEN evidence without running tests. No production filesystem changes were needed for these items; existing final FS/Mac race/Linux/public evidence remains applicable to those unchanged boundaries.
