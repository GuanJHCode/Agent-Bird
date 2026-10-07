# Schema21 Host physical metadata accounting

ROOT integration is the only writer. Engine commits00c2732/0840184 are integrated;
ROOT closes stage publication content gaps before Host integration. Full M3 and
F01–F13 remain required; this schema does not finish the requested feature.

- Immutable managed_host_artifact_metadata rows bind original reservation/run,
  terminal1024 or progress21x1024. Claim creates both rows in its existing TX.
- Control usage precharges64+UTF8 reservation/run IDs+reserved manifest bytes.
  This charge persists; report accounting is unchanged.
- refs.manifest_bytes defaults0 on migration. Only new Store-verified Host refs
  set exact canonical descriptor length; never caller-supplied. Host-only SQL
  guards bind run/reservation/scope/class, numeric slot0 or0..20, kind, size/hash,
  exact descriptor length and aggregate<=pool. Report refs must remain0.
- Disk outstanding includes remaining raw+remaining manifest pools. Final Claim
  disk-control growth subtracts this new22KiB already present in outstanding;
  storage_usage and its control quota retain the whole22KiB. SQLite row metadata
  is not deducted. Existing outstanding promises remain protected.
- Migration installs quota trigger before backfill, so each historical Host
  reservation is charged exactly once within the migration transaction. No file
  reads/namespace ownership or legacy ref credits: old refs remain manifest0.
- Retention integration must verify original physical objects outside DB mutex,
  then hold object lock through reauthorization/ref/retained/event/admission in
  one TX. Exact ACK replay remains source-free. Nothing here authorizes standalone
  callers to fabricate refs or infer physical retention from legacy JSON.

Validation: original metadata RED74494 included a genuine one-byte-short disk
admission failure. Its first precharge case instead failed from a test helper
assuming the run usage row already existed; corrected to COALESCE zero. Do not
count that first case as product RED. Engine RED48232 proves both intent and data
were poisoned after fsync/before publication; full artifacts race54134 passed
following same-FD typed content validation. Final counts/receipts pending.
