# Final private native product acceptance

Date: 2026-09-14. The root-run `native-product-10` case passed the reviewed
G0→G1 lifecycle with a real native Codex TUI and app-server, the installed rc6
distribution, the owner-bound delivery driver, and two same-frame resource
diagnostics. This is the current private synthetic acceptance result. It is
not evidence for the production authenticated provider route or a long-duration
resource workload.

## Frozen inputs

- Acceptance source: `ebf9558727c1dfe9530874668498d5e7e7fd89c5`.
- Installed Go binary SHA-256:
  `f258ab25f5fd8eafa8e08fddc28d630bb342378f7c9fa65cd8c259d6da8b28c4`.
- Installed rc6 manifest SHA-256:
  `5583bc69b4b761e6f36ca2848029ae7b12860a1e2bc772ebcd17451075bd7992`.
- Installed G0 runtime manifest SHA-256:
  `9b1834834d80618dc9793ce421a8fe0da2dcec65f5a8cb958d24519d7eeaab73`.
- Native Codex SHA-256:
  `4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc`.
- Pinned kernel Python SHA-256:
  `8623f5d3e96ae0279fa09f54ea4b355cdc0daf7da285057311f14c7de8c677a0`.

The frozen
[`private-product-plan.json`](../data/cases/native-product-10/private-product-plan.json),
SHA-256 `d383e73ecf5bfd9e26152fd1fe7ce999c7063ddde7ac674c381662065e718984`,
binds the installed binary, package/runtime manifests, interpreter, native
Codex, 41 source paths, synthetic-only mode, and zero external model calls.
The permanent installation and global configuration flags remain false. The
recoverable rc3 pin remains retained.

## Current result and evidence

The final summary is
[`native-product-10/acceptance.json`](../data/cases/native-product-10/acceptance.json),
SHA-256 `70997c14369845c263d7fd2ac2762feb329b4254650e468518304504d6a90b9c`.
It reports `status: pass`, no failure, unchanged auth14, distinct owner PIDs,
the same origin context, zero-turn owner resume, old Source Host shutdown,
typed ACK completion, both same-frame RSS checks below 128 MiB, and absent
public and synthetic-auth sockets/files.

[`result.json`](../data/cases/native-product-10/result.json), SHA-256
`0a4088ebe8f44e413f85e0cdebf4dcb5b5f1d5e263b748a930343373cacb1e8c`,
binds the lifecycle:

1. Generation 1 submitted `native-product-10-task` from the attached native
   owner. The task reached `running` with durable coordinator, Host, and worker
   identities.
2. Normal owner exit produced an interrupted task, an offline old Host, an
   exited old worker, and an `owner_detached` driver.
3. A new native owner resumed the same thread and origin context with no model
   turn, then rebound generation 2.
4. The old stopped event obtained exact native history proof, an explicit
   `stale` decision, and durable G1 ACK.
5. Explicit G1 resume created a new Host and worker. The worker exited with a
   result; the Host was released after its durable terminal event.
6. The result obtained distinct exact history proof, explicit owner `accept`,
   an explicit bridge `handled` decision, and typed ACK
   `d_564f60fc9f152a0cd193b06b85c402a2da478cba`.
7. Final task and driver status were `completed`. Both native backends returned
   zero, were reaped, and had their private sockets removed. The final G0
   activation receipt records two backends and has SHA-256
   `8b1e4ec0366b489c05e58ecc35e94bcac2c93c1ae63982111fafcfd684737e2c`.

The minimum raw transport and delivery evidence is retained under
[`native-product-10-retained/`](../data/native-product-10-retained/manifest.json).
Its manifest records each archived file's source-relative path, byte count,
source/archive SHA-256, allowlist, exclusions, and credential scan result:

- The exact
  [`native rollout`](../data/native-product-10-retained/native/rollout.jsonl),
  SHA-256 `a40fa4f472416d63ef95e34a17b830ed430526dcd56a6c6e7104816020430aa6`,
  contains the initial READY turn and the two completed `g0_delivery` turns.
- [`bridge/`](../data/native-product-10-retained/bridge/) contains both immutable
  intents, source mappings, send claims, owner bindings, transport/history
  receipts, explicit decisions, and ACK receipts. Each raw ACK request is
  represented by a projection that removes `control_file` and binds the source
  request by SHA-256.
- [`g1/state-projection.json`](../data/native-product-10-retained/g1/state-projection.json)
  contains the non-secret task, segment, event, decision, delivery, host, and
  rebind rows. The source DB passed `PRAGMA integrity_check` and has SHA-256
  `bea2227311031e3ca7c7083fa4bba27121b9f5fb85d8ea1088e83cdd1eb20ad2`.
  The raw DB was not copied because `segment_runtime.slot_token` contains an
  original capability value; the projection lists every omitted table/column.
- [`g0/76505700c5674f62b2bee24c68889e7b/`](../data/native-product-10-retained/g0/76505700c5674f62b2bee24c68889e7b/)
  contains the service, both owner/helper readiness chains, backend receipts,
  guardian receipts, and final activation receipt.

CLI auth, process environments, control files/tokens, owner capabilities and
attachments, host bootstraps, grants, isolation manifests/policies, sandbox
profiles, and raw G1 capability values are absent from this archive. The
original task-owned profile `/private/tmp/g0-auth-product-ne4djhhw` remains
unchanged while it is present.

The auth snapshots are byte-identical: both
[`auth-before.json`](../data/cases/native-product-10/auth-before.json) and
[`auth-after.json`](../data/cases/native-product-10/auth-after.json) have
SHA-256 `7d606bf7b759823a43655d80c47f8a52f189c1d4f39b88a142c29dc7226f4518`.
The root
[`postflight`](../data/cases/native-product-10/root-postflight.json), SHA-256
`257f01acb9f3a014e8f110ba710402d65f5ab55f768909af624a24ae9902f4f1`,
independently checked all 15 recorded PID/birth identities as gone and the
public socket as absent.

## Resource and provider scope

The two
[`resource-samples.jsonl`](../data/cases/native-product-10/resource-samples.jsonl)
frames, SHA-256
`01c1a4137b14b41b5364851bf1c8ef5cb1c9378e0f20b73c9acdb5016d6827e9`,
measure the five included tool roles in one `/bin/ps` frame after verifying each
PID/UID/birth/executable/hash both before and after collection. Initial running
used `123312 KiB`; rebound running used `125888 KiB`. Both are below the frozen
`131072 KiB` diagnostic gate. The case invoked no report helpers, so the
reported helper-rusage contribution is explicitly empty.

These are two lifecycle snapshots, not a concurrent long-duration or combined
5/10/20-minute resource test. They do not prove sustained RSS, CPU average, or
memory-growth slope. The recorded CPU percentages are instantaneous samples.

The provider was the task-owned loopback `g0-delivery-v1` fixture. The retained
[`synthetic-plan.json`](../data/native-product-10-retained/fixture/synthetic-plan.json),
SHA-256 `2b91a077a776dfcca9637164f83ea38900fa4fb386eb679a568e2a49d6d4ddd4`,
freezes `synthetic_only: true`, `external_model_calls: 0`, and the three request
stages. The isolated provider config has SHA-256
`3b1ddd95b2c30d88a8f374dfe54b02b225cac4ad0fcb261d74fe85581ec3179c`.
No production auth file or production provider route was used. The result
proves the real native transport, ownership, lifecycle, rebind, exact history,
and G1 decision/ACK path around the fixture. It does not prove a production
model call or authenticated provider behavior.

Backend summaries retain `tree_stop_unproven: true`. The postflight closes all
15 recorded identities and the public socket, but cannot prove absence of an
unrecorded descendant.

## Earlier baseline and failure progression

`native-product-08` remains a retained functional baseline. Its
[`acceptance.json`](../data/cases/native-product-08/acceptance.json) and
[`raw archive`](../data/native-product-08-retained/manifest.json) preserve the
previous complete lifecycle. Subsequent review of its
[`resource-identities.jsonl`](../data/cases/native-product-08/resource-identities.jsonl),
SHA-256 `101f02acae9dbb376ce41e093e1af2aaa0de6222894039d15d2b25996b125db0`,
found each bridge helper at about `269 MiB` and the visible tool roles at about
`343 MiB`. The bridge `digest()` had loaded the entire 222,655,232-byte native
Codex executable into one Python `bytes` object for every identity check.
Commit `bc628fc` changed that verification to a one-FD, 1 MiB streaming hash
while retaining the size, EOF, inode, owner, mode, link-count, timestamp, and
SHA checks. Case10's approximately `40 MiB` helper samples are the native
confirmation of that fix.

| Gate/case | Last reached stage | Root cause | Integrated fix |
| --- | --- | --- | --- |
| Pre-case | runner import | `-I` omitted the scripts directory from `sys.path` | `5c2f2f4` |
| Pre-case | runner identity | `sys.executable` named the Homebrew wrapper instead of the kernel executable | `0a1e631` |
| Pre-case | bridge self-check | product entrypoint repeated the wrapper identity error | `64515ff` |
| `native-product-01` | prepare | profile prefix violated the required `/private/tmp/g0-auth-*` boundary | `fe8bc30` |
| `native-product-02` | register | fixed Python was absent from activation artifact pins | `dfb4b49` |
| `native-product-03` | same-thread resume | validator hard-coded owner epoch 1 although helper admission made it epoch 3 | `c7f02f1` |
| `native-product-04` | stopped delivery | marker-only fixture rejected the structured bridge envelope | `0bfba6c` |
| `native-product-05` | explicit resume | runner observed `running` before process identity became durable | `79a9a92` |
| `native-product-06` | result delivery | history read raced the item-to-completed transition | `d445ed2` |
| `native-product-07` | prepared only | legacy task ID conflicted with the retained rc3 pin; native was not started | `640b553` |
| `native-product-08` | complete | functional private lifecycle passed; later resource review exposed the bridge hash allocation | `640b553`, `bc628fc` |
| `native-product-09` | prepare | the harness required the new sampler pin but the private plan omitted it; no owner was started | `ebf9558` |
| `native-product-10` | complete | case-scoped IDs, rc6, streaming verification, lifetime contract, and sampler pin all integrated | `ebf9558` |

The case09 failure remains in
[`result.json`](../data/cases/native-product-09/result.json), SHA-256
`4b88695b94e69b9bf30a18561c82eb877504316b7c361e7f85ac5e200184cbeb`.
Its message hash is the exact SHA-256 of `harness source not frozen`; auth14 was
unchanged and no native owner was created. Failed cases remain evidence of
resolved gates rather than additional pass runs.

## Retention and optional cleanup

No cleanup is performed here. Retain both
[`native-product-10-retained/`](../data/native-product-10-retained/manifest.json)
and [`native-product-08-retained/`](../data/native-product-08-retained/manifest.json),
all repository case archives, and the recoverable rc3 pin. While the original
profiles remain present, retain their G1 DB/spool and other recovery state; the
repository snapshots are audit archives, not replacements for mutable recovery
state.

Only expired capability and isolation objects are optional deletion candidates.
Do not delete an entire profile or unpin rc3. The current additional candidates
are:

- `/private/tmp/g0-auth-product-o72u7_pj/.auth-isolation-owner-d316f48bef61d9ad`,
  `synthetic-isolation.json`, `synthetic.sb`, and its exact
  `native-product-09/grants/` and `native-product-09/owner-capabilities/`.
- `/private/tmp/g0-auth-product-ne4djhhw/.auth-isolation-owner-f17bb0c78d863ab4`,
  `synthetic-isolation.json`, `synthetic.sb`, its exact
  `native-product-10/grants/`, `native-product-10/owner-capabilities/`, and
  `c/tui-thread-reference-capabilities/`.

Keep each profile's `g1-state/control/`, `g1-state/host-bootstrap/`, raw
`state.db`, delivery ledgers, driver history, and G0 receipt directories. The
older optional cleanup candidates recorded before case09 remain unchanged.
