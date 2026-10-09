# F10 pre-Bind original owner launch supervision

Status: investigation/design, base integration `a011561`; no production change.
ROOT owns this worktree. An independent architect reads the frozen integration
source; controlled cross-provider composition tests run in another worktree.

## Required boundary

`PrepareOwnerSuccession` persists intended before Start. If FirstHost then dies,
absence of Bind, a deadline, or an exited PID cannot prove that no child started
or that its whole tree exited. Preserve UNKNOWN for all legacy intended rows
without new original-owner proof. Do not reuse bound-never-Applied, initial
Bind-before-ledger, pending authorization rebind or publication lease as proof.

A new launch owner must retain the original child Handle/Wait, authenticate its
identity and exact immutable request before Start, persist bounded phase/receipt
records, fence duplicate launchers, and distinguish confirmed never-started,
confirmed original tree exit and uncertain ownership. Surviving supervisor
coverage is acceptable only with explicit limits; loss of that final owner
cannot be hidden behind another layer of PID probing.

## Existing guarantees to preserve

- Original identity, permit, signed Applied and epoch/FirstHost scope checks.
- FIFO, revision, budgets, storage quota and at-most-one live original owner.
- No worker launch from unconsumed or uncertain authorization.
- Owner-only runtime directories/files, root identity and atomic transaction
  failure cleanup. No FS or RPC work while holding a Store write transaction.
- Existing installed/native entry points; no daemon installation, user setup
  replacement, real Provider/model/account call, push or release.

## Design questions under investigation

Prefer retaining supervision in an already-running trusted actor over adding
one permanent Go process per Host: the existing four-Host evidence already uses
119.30 MiB of its 128 MiB envelope. Compare Coordinator ownership with current
FirstHost/permit/peer-parent checks, including what happens if Coordinator itself
crashes. Define migration and recovery only after the proof chain is explicit.

Acceptance will target a small number of actual process-failure windows with
original Wait/tree receipts and untouched budget/FIFO; it will not infer safety
from elapsed time or repeat already-passing F10/F12 suites.
