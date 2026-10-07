# Launch credential scope increment

New credential paths bind the complete normalized launch scope, allowing
predecessor and successor origin/generation ciphertexts to coexist. Owner ID
and incarnation alone do not change that normalized scope. Ciphertext version1
and AES-GCM AAD remain unchanged. A matching legacy file can be read or copied
with exclusive publication; it is never overwritten. A foreign legacy scope
must first authenticate under its own declared scope/AAD, then remains intact
while the already-authenticated current Host token creates its own v2 file.
An invalid existing v2 file cannot trigger legacy fallback or silent repair.

Two real RED tests reproduced the old filename collision and absent v2
migration. Final macOS Go1.26.8 race: all `TestOriginalOwner*`20 top/46 named
PASS, zero failures/skips,123.154s, including owned native attachment/recovery.
After test-only additions, final credential/invalid-Prepare race7 top/22 named
PASS, zero failures/skips. The invalid Prepare test now enumerates every
launch credential file, preserving response and Store no-allocation checks;
its prior incomplete-scope filename assertion would have been ineffective.
An intermediate unused-import build failure is preserved separately.

Linux arm64 ordinary tests7 top/22 named PASS in owned offline container
2811968e39a702cda6683755558050b8b6a0ee0c9c9317479891847d50238f86.
Its original exit was0, not OOM; exact CID removal succeeded and subsequent
inspection found it absent. This is not Linux Provider/sandbox acceptance.
Final coordinator vet and whole-module build exited0. Independent read-only
review passed production and the final tests. Hashes and raw evidence are in
`../data/root-credential-receipt.json` and its referenced logs.

No real Provider/model/account, installation, push or release. This increment
does not authorize a new owner or reset a recovery ledger. Schema25, stable
Resume authorization, actual succession and intended-child crash evidence
remain separate open work under the implementation plan.
