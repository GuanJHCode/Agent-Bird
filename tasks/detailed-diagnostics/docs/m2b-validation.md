# M2b diagnostics launch authority

Explicit detailed consent now creates an attempt-scoped diagnostics reservation
and empty delivery row inside the same Claim transaction as execution/budget
rows. The grant commits policy/hash/reservation and a positive durable segment
ordinal into the launch, candidate intent and signed owner body. Legacy nil
three/four-field candidate bytes and omitted wire remain unchanged.

Resume keeps its original attempt reservation and advances ordinal. Reopen and
Coordinator epoch replay reconstruct the original grant without charging again.
The central candidate-input binding also restores diagnostics on historical
review/retry/legacy proof commands; binding only dispatch would have lost those
intent fields. Missing/corrupt/foreign policy, producer, reservation namespace or
delivery tuples and deleting/released reservations are rejected for dispatch.

All nonreleased managed reservation classes consume the2GiB per-state-DB
allowance. Unmaterialized reserved-retained promises, new bytes, exact control
metadata, SQLite overhead and minimum/critical disk reserves enter overflow-safe
disk admission. The new artifact reservation itself is not charged as16MiB
control data. This is NOT yet complete managed-artifact coverage: M3 still must
migrate old writers/intake, establish retained_bytes by verification, and implement
release/delete. No public capture/read/ACK endpoint exists yet.

## Evidence and limits

- Worker contract commits2b4d4ec and9cf455b added the typed grant. ROOT rejected
  the initial tests' pointer aliasing, replaced dependency assertion and weak
  signature causality. Corrected tests preserve the old dependency tamper and
  use independent values; valid-shaped reservation/ordinal changes recompute
  intent/body hashes before the old signature rejects them. Policy/hash drift
  is separately rejected by grant format validation. ROOT adds literal nil
  three/four-field canonical expectations. Final independent review PASS.
- Actual Store RED48485 exposed absent detailed grant, disk double promise and
  unrepresentable M1 scope admission; it was not a compile-only failure.
- Focused7730 passed the initial tests. v2/full5073 had exactly one fixture
  mismatch: Codex interrupted continuation is intentionally unsupported without
  its separate seal contract. Final tests use the authorized Codex Host-question
  path and retain a detailed-grant negative test of the interrupted restriction.
  AGY/Claude still exercise interrupted resume. No permission gate was widened.
- Final focused12997 passed; full Store65237 exit0,366top1042named,0fail/skip.
  Final contract race17936 exit0,35top249named,0fail/skip. Final affected Store
  race82977 exit0,75top299named,0fail/skip (427.860s). Source and log hashes
  and final affected-race terminal evidence are in data/root-m2b-final-receipt.json.
- Docker Linux61277 exit0,19top61named,0fail/skip. Crosscompile52325 exit0;
  binary moved into its dedicated linux-m2b directory before a read-only mount.
  CID21d089fb199de109b36145ec4f11980db4e9a9a53691ab8f65fc562d4c9ac1bd
  exited non-OOM and was precisely removed, with inspect absence in its receipt.
  This is pure Store/crypto/owned-filesystem evidence, not Linux race/Provider/CLI.
- Foreign producer, missing/corrupt authority, exact logical allowance, control
  failure, arithmetic overflow and two simultaneous disk promises are exercised.
  Signed-frame overflow uses controlled SQL payload injection after a genuine
  supervised owner binding/applied/activation; it proves Claim rollback, not that
  public Submit can admit that extreme payload. Owner/Host identities in Store
  fixtures are trusted synthetic inputs; no kernel ancestry/exit is claimed.
- Production and final fixture independent reviews PASS. Historical proof after
  M3 release must be distinguished from dispatch/write permission before exposing
  deletion. Control-row exact byte formula was reviewed; one-byte admission
  thresholds for that metadata were not separately tested.

No model/account call, installed-copy replacement, push or release. Test logs,
RED evidence and the tested Linux binary are retained. No temporary source module
was created. Completion of this milestone does not complete F02 or F01–F13.
