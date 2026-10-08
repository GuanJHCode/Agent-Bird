# F10 bound-never-Applied recovery acceptance

Schema28 Store and public Resume now recover a current successor that completed authenticated Bind but never Applied, only after exact Owner/Host typed exit observations. Immutable abandonment proof and adjacent scope handoff preserve the original queue reservation and budget. Ordinary fully-Applied admission remains intact.

Production/Store independent review at bf9d23a and final native/public/epoch increment review at 193359f both PASS. Source hashes and evidence pointers are in `data/final-receipt.json`.

- Store affected ordinary: 68 top / 415 named PASS; new API race: 5 top / 26 named PASS. Epoch replay: 1 top / 3 named PASS.
- Authenticated Host→Owner four file windows: macOS ordinary/race and Linux each 1 top / 5 named PASS; four Owner and four Host original Wait=0 per run. Historical inc1 fixture is synthetic.
- Public CLI actual inc1→test-only signed Bind inc2→production inc3: macOS ordinary/race and Linux each 1 PASS, no failures/skips; same attempt, queue sequence, budget, immutable replay, collect/accept and all original Wait records. Old public deadBoth branch ROOT ordinary: 1 PASS.
- Existing Store/Coordinator vet and whole build passed after final production changes. Integration verification is recorded separately.

All listed final selectors have zero failures/skips. Earlier schema/command-collision RED and setup/fixture failures remain in evidence and are not counted as successful runs. Linux owned containers are terminal, non-OOM and removed; generated binaries were verified by hash then removed.

This closes only the bound-never-Applied successor substage. Initial Bind before a ledger, pre-Bind loss of the original child handle, and pending authorization origin rebind remain open. Intel/Windows native and Linux race were not executed. No real model/account or user installation was used.
