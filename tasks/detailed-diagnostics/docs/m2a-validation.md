# M2a consent and durable state foundation

Public task requests now carry optional strict diagnostics consent through
configuration preparation, frozen plan digests, guard-owned snapshots and Store
admission. Nil fields preserve legacy canonical bytes; explicit summary and
detailed are separate identities. schema19 adds immutable policy rows, artifact
reservation identities and bounded diagnostic delivery state, without inferring
consent or managed authority for legacy rows/artifact JSON.

Delivery rows start idle at revision1 with no request/page/ACK/cursor history.
SQL guards enforce monotonic allocation, failure recovery that burns its revision,
immutable pending pages and atomic ACK-shaped transitions. These guards do not
perform owner authorization or validate receipt hashes: those belong to the
future Store read/ACK methods. Integer accounting columns reject SQLite REALs.

## Scope still open

This is a foundation milestone. DiagnosticsGrant is not yet attached by Claim,
no Host detailed capture/storage driver is connected, and no public log read/ACK
or managed artifact allowance/intake/delete is implemented. No end-to-end detailed
logging, 2GiB total managed quota, Provider/RSS or physical-disk-fault claim is made.
No installed copy was replaced, model/account called, or release pushed.

## Evidence

- Actual public JSON rejection/consent commitment and literal legacy digest.
  Real IPC Submit persists the exact policy; actual owned config preparation,
  install guard and Store transaction reject removed/changed consent and admit
  the unchanged pinned plan. Provider availability and owner identity in the
  config fixture are controlled inputs, not real account/Provider evidence.
- Real v18 migration preserves a legacy task/attempt/event containing an artifact
  and its usage; two reopens create no policy/reservation/delivery authority.
  A late conflicting table causes all new DDL/accounting/version publication to
  roll back. Historical downgrade fixtures now remove empty v19 tables first.
- Admission/control-allowance rejection rolls back all task/run/policy rows.
- Behavioral RED35237 removes only the UPDATE guard, then restores the exact
  file bytes: direct idle revision mutation is wrongly accepted. RED91117 is
  the actual pre-fix initialization/type snapshot: 3top/10named failures cover
  initial histories and fractional accounting. Both failure logs are retained.
- Full pre-final-guard Store/Install40020: Store352top989named, Install108top341named,
  0fail. Install additionally skips TestC11InstallPublicationCrashHelper because
  that helper requires its explicit owned original-process crash context. This
  baseline does not stand in for final schema tests.
- Final Docker Linux74845:8top18named,0fail/skip, original exit0, non-OOM. Exact
  CID0bad2ba757630dda30108528bb8b89d9c99f5694e172f33b19f1cdae979e0dc0 was
  removed and inspect confirmed absence. data/linux-m2a/m2a-v2-receipt.json
  records image, command, binary/log hashes. No Linux race or public IPC claim.
- Final affected macOS race65522 exit0: Store28top45named, Install1/1,
  Coordinator8/21, CLI23/64, total60top131named,0fail/skip. Full module
  build60601/vet28546 exit0;22 changed Go files are gofmt-clean and hashed
  in data/root-m2a-final-receipt.json. Independent reviews
  passed the public/config/admission scope and final initialization/transition/
  integer guards; no source edits followed that final review.

Raw evidence and the tested Linux v2 binary remain in the integration task data
directory. The unused superseded Linux v1 crosscompile output was removed after
recording its SHA in data/root-m2a-removed-unused.json; no run used that binary.
