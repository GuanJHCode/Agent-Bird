# Host physical artifact writers and Store references

Base `440a458`; complete remaining M3, not only the file library. ROOT owns
integration/contract/Host/Store and all native/Docker resources. The isolated
`host-slot-engine` worktree owns only internal/artifacts and its own evidence;
no other writer may edit that worktree. F01–F13 remain active.

## Frozen physical engine API

- `HostSlotBinding{Version,ReservationID,ScopeSHA256,Class,Index}`. Version1;
  terminal/index0 or progress/index0..20. `Validate` and `ObjectID` methods.
- Object ID hashes version/reservation/class/index with `host-slot-v1-` prefix;
  scope/content/kind cannot create alternate namespaces for the same slot.
- `HostSlotDescriptor{HostSlotBinding,Kind,SHA256,Size}`, embedded flat JSON;
  terminal Kind terminal|prelaunch, progress Kind progress. Bounds1MiB/64KiB;
  canonical manifest max1024bytes. `Validate` and `Canonical` methods.
- `MaterializeHostSlot(ctx,root,descriptor,body) (Proof,error)` validates before
  filesystem mutation, descriptor first, same bytes replay, complete-stage
  recovery and physical identity checks. Existing report Proof reused; IntakeID
  is the derived slot object ID. No invented provider SourcePath.
- `VerifyHostSlotAndCommit(ctx,root,descriptor,expectedProof,callback) error`
  creates nothing and holds the verified original object lock during the short
  Store transaction callback.
- `LoadHostSlot(ctx,root,binding) (descriptor,Proof,[]byte,error)` creates nothing,
  strictly reads original intent.json, verifies binding, returns original bounded
  bytes; may publish complete matching data.stage, never partial/different bytes.
  Unpublished/partial intent is conservatively unavailable.
- Host root is its trusted private spoolRoot. Store derives the same root as
  stateDir/host-spool/producer, never from a request path. New managed objects
  live in its managed-artifacts/derivedID child.

## ROOT integration work

1. Define a bounded Host receipt in ArtifactRef carrying canonical descriptor and
   original physical proof; reject invalid authority before any managed write.
2. Migrate all Host normal/candidate/structured/Codex/prelaunch/progress writer
   paths. Freeze grant into launch metadata; prelaunch and terminal compete for
   the same object. Preserve UNKNOWN after cold recovery; an old diagnostic is
   never evidence that a restarted Host did not enter Start.
3. Resolve old direct Execute/ExecuteInvocation APIs explicitly; no silent
   ungranted physical writer or fabricated Store permission.
4. Store validates bound reservation and event scope, verifies private object
   outside its mutex, reauthorizes inside the held-object callback, and commits
   immutable ref/retained bytes/event atomically. Exact durable replay remains
   source-free; terminal + Exited share one charge.
5. Precharge bounded on-disk slot descriptor metadata before offline Host writes;
   new durable accounting changes use schema21, not rewritten schema19/20.
   Preserve legacy claims without ownership. Exact quotas include raw promises,
   physical metadata and control changes.
6. Migrate affected fixtures truthfully, then full affected/race/native/Linux,
   independent review and recorded evidence. Full M3 still includes consumers,
   legacy_pending, diagnostics retained/delete/tombstones and2GiB/flood/RSS.

## Current baseline issue found during integration

The old test-only Host downgrade helpers delete immutable reservation rows after
removing the guard, but schema19 has no reservation DELETE accounting trigger.
Their comment claiming automatic refund was wrong. Add a real RED for removal
of modern metadata from historical fixtures and explicitly refund exact row
costs before dropping rows; do not alter production deletion permissions.

## Current integration checkpoint (2026-10-07)

- Integrated engine commits00c2732/0840184. ROOT stage after-sync poisoning RED
  48232 failed both intent/data; typed same-FD publish fix artifacts race54134
  PASS18top61named, independent reviewPASS. Added Close error propagation.
- Schema21 metadata precharge + migration/ref shape guards implemented and
  reviewedPASS. Full Store before physical-intake changes43701 PASS405top1153named;
  old failed56389 only fixture accounting/boundary updates, retained raw log.
- All six Host production WriteArtifact calls replaced by fixed-slot writer;
  ExecuteLaunch requires matching producer/grant, legacy Execute refuses; metadata
  cloned before callbacks. Cold recovery loads original managed slot, staysUNKNOWN.
  Real public managedCodex prelaunch RED48978 then GREEN31719 (1top). Initial
  normal-canceled actor did not prove expected prelaunch semantics; not productRED.
- Store terminal/ref retention gate implemented: first auth transaction returns
  before FS, original object lock surrounds second auth/ref/retained/event TX.
  RED10138 accepted missing/forged/changed/foreign objects; GREEN47767/6187/34895.
  Runtime managed-raw/control doublecharge RED23216(3238vs3217) now fixed by
  ref-bound schema21 runtime accounting. Progress gate and ref progress cost wired,
  focused verification still required. Exact durable ACK precedes filesystem IO.
- Stronger Store commit currently invalidates old synthetic ArtifactRef fixtures;
  do NOT loosen production guard. Last99840 migration/focused failed6top from
  legacy factories constructing modern unverified artifacts plus one old quota
  construction. Need real managed fixture writer, explicit data-only legacy grants
  BEFORE constructing historical evidence, and managed consumers/review retry.
- Host fixture worker owns ONLY tests (except ROOT host_artifact_writer_test.go)
  in isolated tmp/host-writer-fixtures baseline0bbb512. First tests commitcb0f993,
  still active. Mac native token assigned worker; ROOT only SQL/file tests and no
  Docker/native until returned. Snapshot0bbb512 must not merge; take tests delta.
  Old successful StartSegment-after-result fixture bypassed public resume contract:
  split result/ACK assertions and actual Stop→Publish/ACK→QueueResume→ClaimReady;
  no direct DB status repair or new permissions.
- integration ROOT remains sole writer. Current production is WIP, uncommitted
  beyond0840184; no final acceptance or delivery claim. Full M3/F01–F13 preserved.

## Store intake and consumer checkpoint (2026-10-07 evening)

- Store gate includes progress intake. `84217` PASS 5 top / 9 named.
- Migrated positive Store fixture producers to real immutable slots; historical
  migrations strip authority BEFORE producing old artifacts/decisions. Old
  downgrade now skips already historical grants; old quota migration strips
  modern accounting before dropping its triggers. No production guard weakened.
- Complete current managed Store v1 `19092` FAIL: 392 top passed, 18 failed.
  Most remaining cases were old path/struct-pointer comparisons, legacy fixture
  construction, exact encoded ref lengths, and the diagnostic retry consumer.
- Real accounting bug reproduced (`35838`, 21-byte short refund): an old raw
  event's UPDATE/DELETE dynamically joined a later ref and changed its original
  charge. Schema21 now persists immutable `runtime_events.host_artifact_verified`
  default0, verifies any INSERT1 against the immutable ref, and uses that original
  flag for refunds. No legacy scan/file IO/authority backfill. `84979` PASS20/45;
  follow-up test explicitly asserts old0/new1 and rejects flag rewriting.
- Receipt total4096 mismatch RED50639; contract now shares a4096 total limit with
  Store while keeping1024 descriptor and4096 individual proof caps. Exact total
  boundary and old nil wire verified.
- Review diagnostic managed-path incompatibility RED60210 (both providers):
  correct original files rejected by old launch/attempt/segment path reader.
  Added `ReadVerifiedHostSlot`: no flock/create/recovery, fixed-FD verified snapshot
  and exact original Proof; errors including Close clear returned bytes. Store
  first authenticates its stored immutable ref and retains existing256-byte
  diagnostic content/lifecycle checks. `36413` PASS22 top /82 named including
  tamper and rechecks. This snapshot API never grants/ref-commits ownership.
- Full artifacts+contract race93982 PASS: artifacts19top65named; contract42top282named,
  zero fail/skip. Added held-writer-lock snapshot regression plus changed proof,
  replacement inode and unrecovered stage cases. Root verified raw JSON counts.
- `linux_process_worker` reused as readonly reviewer (no old RPC work integrated),
  reviewed intake/accounting/snapshot consumer. Initial speculative half-commit
  finding explicitly withdrawn; final source review found no blocker. Slow FS
  reads inside DB TX are bounded and take no object lock, avoiding lock inversion.
- Critical lifecycle records without physical artifacts retain their original
  critical disk reserve path; they do not require outstanding raw promises again.
- Store fixture round3 `42388` initially10 pass/1 fail, last indexed historical
  failure corrected before artifact construction; focused17369 PASS. New full
  Store28215 running (`store-managed-full-v2.jsonl`), do not start duplicate run.
- Host worker remains sole Mac native actor runner in its isolated WT. Test-only
  commits through47e00d4 are NOT yet integrated. V2 raw full result253s FAIL,
 377top PASS/26FAIL/16SKIP. Root independently checked terminal JSON and PIDs gone.
  Checkpoint failure was a0755 TempDir spool fixture, not production best-effort:
  explicit0700 fixture47e00d4 passed. Remaining25Host fixture groups still active.
- Root integration remains WIP beyond0840184. Still pending full Store/Host,
  CLI/public/native/Linux verification, other candidate consumers, retirement of
  old Spool.WriteArtifact, legacy_pending/diagnostics retention/deletion/tombstone,
  2GiB/flood/RSS and F01–F13 broader goals. Intel/Windows native environment deferred
  by user. No real Provider, installed-copy replacement, push or release occurred.

## ROOT integration checkpoint (2026-10-07 19:39)

- Host test-only commits integrated through d43124e (worker a4ce0b2). ROOT
  took over the remaining fixtures; old worker WIP is preserved in its WT.
  Mac native token returned to ROOT.
- Full Host 77251 terminated exit1: 403 top PASS, 1 FAIL, 16 SKIP;
  1550 named PASS. Sole failure was real Store source-rejection fixture using
  noncanonical spool namespace. Fixed to state/host-spool/producer. Restored
  launch.json executable SHA/birth-known checks flagged by independent review.
  Targeted 75808 PASS2/2, log host-managed-final-fixtures.jsonl.
- Added managed candidate consumers in Store and Host, requiring original
  physical proof and trusted state spool parent. CLI supplies that parent from
  bootstrap. Same-content inode replacement had real RED, now rejected. Store
  historical nil-receipt paths remain data-only. Read APIs take no writer lock.
  Latest independent source review found no blocker in those consumers.
- Removed obsolete events.Spool.WriteArtifact and unused legacy recovery reader.
  Full events PASS6 top/13 named. Store race85490 PASS27/95 preceded the final
  candidate consumer changes; do not claim it verifies those later changes.
- Current full Store/Host/CLI 88463 is running against the stable source snapshot,
  log store-host-cli-final.jsonl. CLI accept replay exposed /tmp symlink alias
  incompatibility at resolveState; do not weaken artifact O_NOFOLLOW. ROOT will
  add a focused path regression and fix after the current snapshot completes.
- User explicitly requested domain parallel work and allowed relaxed test-agent
  limits. Runtime still permits only ROOT plus two children. F06 protocol
  verification and F04 independent gap mapping delegated read-only; integration
  remains ROOT-only. Intel/Windows native acceptance deferred as authorized.
- This is ongoing work, not M3 or F01-F13 completion. No real model/account,
  installed-copy replacement, push or release.

## 2026-10-07 20:10 milestone

Full Store413/1167 and Host404/1551 (16skip) passed. Final consumer race30/88;
CLI candidate/path/accept replay race5/35 passed after fixture migration and
parent-only resolveState. Two original-owner restart/rebind resume tests still
fail at initial_owner_transport_rejected; no assertion weakened. Exact scope,
raw reports and retained failure archive: docs/validation.md + data/milestone-receipt.json.
Linux artifacts19/65 + Store19/46 passed and containers removed. All ROOT test
sessions are terminal; Mac token currently belongs to linux_process_worker for
ACP post-review actor verification. ROOT owns integration and final-git-process
WT after worker handoff; F04 guard source reviewed and root delta awaiting final
review acknowledgement. F06 isolated ACP client remains a foundation, not a
public auto-injection feature. Full F01–F13/M3 unchanged and incomplete.
