# Final private native product acceptance

Date: 2026-09-14. The root-run `native-product-08` case passed the reviewed
G0→G1 lifecycle with a real native Codex TUI and app-server, the installed rc4
distribution, and the owner-bound delivery driver. This is sufficient evidence
for that private synthetic scenario. It is not evidence for the production
authenticated provider route.

## Frozen inputs

- Acceptance source: `640b5539327e52893f682ca0dc7157cab14ac64e`.
- G1/Host Go source: `8139120`; installed binary SHA-256:
  `e6c0bf930a75029d39b38b49cca2ded8999d0ce0545ed8d2fa7e48a1fc0bc3c7`.
- Installed rc4 manifest SHA-256:
  `fae6fe64f52bc8c28b4131a63fef2ae1e2b5bd7352a0baa15ce5f0136082c66b`.
- Installed G0 runtime manifest SHA-256:
  `9b1834834d80618dc9793ce421a8fe0da2dcec65f5a8cb958d24519d7eeaab73`.
- Native Codex SHA-256:
  `4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc`.
- Pinned kernel Python SHA-256:
  `8623f5d3e96ae0279fa09f54ea4b355cdc0daf7da285057311f14c7de8c677a0`.

The package verification archive is
[`package-rc4-verification.json`](../data/package-rc4-verification.json), SHA-256
`970f3f6a6ce4599a87ea656f68d573f5a3f0a291f7285f942e1ca0190bce7e6e`.
It records successful package, project-local install, Doctor, plugin validation,
native case, and postflight operations. The permanent installation and global
configuration flags are both false. The recoverable rc3 pin for
`native-product-task` was retained.

## Result and evidence

The final summary is
[`native-product-08/acceptance.json`](../data/cases/native-product-08/acceptance.json),
SHA-256 `da50a9568c5d7b6d3413593e879caecf67362cf4ae7f787e5d773573c5226545`.
It reports `status: pass`, no failure, unchanged auth14, distinct owner PIDs,
the same origin context, zero-turn owner resume, old Source Host shutdown,
typed ACK completion, and absent public and synthetic-auth sockets/files.

[`result.json`](../data/cases/native-product-08/result.json), SHA-256
`a8cde987a5e8c60afd4e9c9572e3ce80df7ff631b2ae8f94951f9225310a6fec`,
binds the detailed lifecycle:

1. Generation 1 submitted `native-product-08-task` from native owner PID 17304.
2. Normal owner exit produced an interrupted task, an offline old Host, an
   exited old worker, and an `owner_detached` driver.
3. Native owner PID 17590 resumed the same thread and origin context with no
   model turn, then rebound generation 2.
4. The old stopped event obtained exact native history proof, an explicit
   `stale` decision, and a durable G1 ACK.
5. Explicit G1 resume created a new Host and worker. The worker exited with a
   result; the Host was released after its durable terminal event.
6. The result obtained a distinct exact native history proof, an explicit
   owner `accept`, an explicit bridge `handled` decision, and typed G1 ACK
   `d_55dcff5e18ea0df730d1150d5337bddcaee87633`.
7. Final task and driver status were `completed`. Both native backends returned
   zero, were reaped, and had their private sockets removed. The G0 activation
   receipt records two backends and has SHA-256
   `6935979059f221f48440b9a7e26600f407f781b6709d61d2d7cf2e1678289f2b`.

The minimum raw evidence needed to review the stopped and result deliveries is
retained under
[`native-product-08-retained/`](../data/native-product-08-retained/manifest.json).
The manifest records every archived file's source-relative path, byte count,
and source/archive SHA-256. It also records the allowlist, exclusions, and the
credential scan result:

- The exact
  [`native rollout`](../data/native-product-08-retained/native/rollout.jsonl),
  SHA-256 `df079384ffbae13b23211cc3a2a3c8ae8aa073866771dcc470d43098edc775a2`,
  contains the initial READY turn and the two distinct `g0_delivery` turns,
  completed with `DELIVERY_RECEIVED` and `SYNTHETIC_COMPLETE`.
- [`bridge/`](../data/native-product-08-retained/bridge/) contains the two
  immutable intents, source mappings, send claims, owner bindings,
  `controller_acked` statuses, exact-history receipts, explicit decisions, and
  ACK receipts. Each raw ACK request was replaced by a projection that removes
  `control_file` and binds the source request by SHA-256.
- [`g1/state-projection.json`](../data/native-product-08-retained/g1/state-projection.json)
  contains the non-secret G1 task, segment, event, review, delivery, host, and
  rebind tables. The source `state.db` passed `PRAGMA integrity_check` and has
  SHA-256
  `ccc8205c41d49da1b876aff0d6c54b1b690331c32d944300cbf0b5e686dfeae9`.
  The raw database was deliberately not copied because
  `segment_runtime.slot_token` contains an original capability value; the
  projection lists every omitted table and column.
- [`g0/e80b15a58b924acfaff83c56caf9947a/`](../data/native-product-08-retained/g0/e80b15a58b924acfaff83c56caf9947a/)
  contains the service, two owner/helper readiness chains, backend receipts,
  guardian receipts, and final activation receipt.

CLI auth, process environments, control files/tokens, owner capabilities and
attachments, host bootstraps, grants, isolation manifests/policies, sandbox
profiles, and raw G1 capability values are absent from the durable archive.
The original task-owned profile `/private/tmp/g0-auth-product-e5vm4tip` remains
unchanged for recovery while it is present.

The auth snapshots are byte-identical: both
[`auth-before.json`](../data/cases/native-product-08/auth-before.json) and
[`auth-after.json`](../data/cases/native-product-08/auth-after.json) have
SHA-256 `7d606bf7b759823a43655d80c47f8a52f189c1d4f39b88a142c29dc7226f4518`.
The root postflight is
[`root-postflight.json`](../data/cases/native-product-08/root-postflight.json),
SHA-256 `20f17cd36e2ada39d9826c2b64e73ddf2d4454bfd19658b190742f7a0946c5eb`.
It independently checked all 15 recorded PID/birth identities as gone and the
public socket as absent.

## Scope boundary

The provider was the task-owned loopback `g0-delivery-v1` synthetic fixture.
The retained [`synthetic-plan.json`](../data/native-product-08-retained/fixture/synthetic-plan.json), SHA-256
`9659653fa545c5730b760e8b6d41ef647eec8de2b48f3d792e7f40a56aa84075`,
freezes `synthetic_only: true`, `external_model_calls: 0`, and the three request
stages. The isolated provider config has SHA-256
`efcfd79acb6620f3ec7d71e1820351d56d10eb880f390489510f162322352013`.
No production auth file or production provider route was used. The case proves
the real native transport, ownership, lifecycle, rebind, history, and G1
decision/ACK path around that fixture; it does not prove a production model
call or authenticated provider behavior.

The lifecycle samples are milestone evidence rather than a fixed-duration
resource benchmark. Backend summaries contain `tree_stop_unproven: true`.
The independent postflight closes all 15 recorded identities and the public
socket, but it does not establish absence of an unrecorded descendant.

## Failure progression

| Gate/case | Last reached stage | Root cause | Integrated fix |
| --- | --- | --- | --- |
| Pre-case | runner import | `-I` omitted the scripts directory from `sys.path` | `5c2f2f4` |
| Pre-case | runner identity | `sys.executable` named the Homebrew wrapper instead of the kernel executable | `0a1e631` |
| Pre-case | bridge self-check | product entrypoint repeated the wrapper identity error | `64515ff` |
| `native-product-01` | prepare | profile prefix violated the required `/private/tmp/g0-auth-*` boundary | `fe8bc30` |
| `native-product-02` | register | the fixed Python executable was absent from activation artifact pins | `dfb4b49` |
| `native-product-03` | same-thread resume | validator hard-coded owner epoch 1 although helper admission made resumed owner epoch 3 | `c7f02f1` |
| `native-product-04` | stopped delivery | marker-only fixture rejected the real structured bridge envelope with HTTP 409 | `0bfba6c` |
| `native-product-05` | explicit resume | runner observed `running` before the optional process identity became durable | `79a9a92` |
| `native-product-06` | result delivery | history read raced the 11ms item-to-completed transition and treated zero completed matches as permanent ambiguity | `d445ed2` |
| `native-product-07` | prepared only | root preflight found the legacy task ID would conflict with the retained rc3 pin; no native process was started | `640b553` |
| `native-product-08` | complete | unique case-scoped IDs; full reviewed lifecycle passed | `640b553` |

The failed cases and their postflights are archived under
`tasks/g1-g4-delivery/data/cases/native-product-01` through
`native-product-07`. They are evidence of resolved gates, not additional pass
runs.

## Retention and optional cleanup

No cleanup is performed by this archive. Retain the durable
[`native-product-08-retained/`](../data/native-product-08-retained/manifest.json)
archive, all repository case archives, and the rc3 version pin. While the
original final profile remains present, retain its G1 database/spool and other
recovery state; the repository snapshot is an audit archive and does not
replace mutable recovery state.

After the archive is accepted, only the following expired capability and
isolation objects are candidates for deletion. Removing them is optional and
must not be implemented by unpinning rc3 or deleting an entire profile:

- `/private/tmp/g0-auth-product-h7u9lkr7/.auth-isolation-owner-55c61d1c8705d323`,
  `/private/tmp/g0-auth-product-h7u9lkr7/synthetic-isolation.json`, and
  `/private/tmp/g0-auth-product-h7u9lkr7/synthetic.sb`.
- `/private/tmp/g0-auth-product-n7hxpg8e/.auth-isolation-owner-436bf03cf5eef103`,
  `/private/tmp/g0-auth-product-n7hxpg8e/synthetic-isolation.json`, and
  `/private/tmp/g0-auth-product-n7hxpg8e/synthetic.sb`.
- `/private/tmp/g0-auth-product-__mpb9lt/.auth-isolation-owner-18bcd76cd5845988`,
  `/private/tmp/g0-auth-product-__mpb9lt/synthetic-isolation.json`, and
  `/private/tmp/g0-auth-product-__mpb9lt/synthetic.sb`.
- `/private/tmp/g0-auth-product-if5m7eb1/.auth-isolation-owner-b5246f66a758c7df`,
  `/private/tmp/g0-auth-product-if5m7eb1/synthetic-isolation.json`, and
  `/private/tmp/g0-auth-product-if5m7eb1/synthetic.sb`.
- `/private/tmp/g0-auth-product-i0_fxcc8/.auth-isolation-owner-14bb619cf69c8af7`,
  `/private/tmp/g0-auth-product-i0_fxcc8/synthetic-isolation.json`, and
  `/private/tmp/g0-auth-product-i0_fxcc8/synthetic.sb`.
- `/private/tmp/g0-auth-product-e5vm4tip/.auth-isolation-owner-bce3827fd212a71d`,
  `/private/tmp/g0-auth-product-e5vm4tip/synthetic-isolation.json`, and
  `/private/tmp/g0-auth-product-e5vm4tip/synthetic.sb`.
- The exact `grants/` and `owner-capabilities/` directories below
  `/private/tmp/g0-auth-product-n7hxpg8e/native-product-03`,
  `/private/tmp/g0-auth-product-__mpb9lt/native-product-04`,
  `/private/tmp/g0-auth-product-if5m7eb1/native-product-05`,
  `/private/tmp/g0-auth-product-i0_fxcc8/native-product-06`, and
  `/private/tmp/g0-auth-product-e5vm4tip/native-product-08`.
- The exact `c/tui-thread-reference-capabilities/` directories below
  `/private/tmp/g0-auth-product-n7hxpg8e`,
  `/private/tmp/g0-auth-product-__mpb9lt`,
  `/private/tmp/g0-auth-product-if5m7eb1`,
  `/private/tmp/g0-auth-product-i0_fxcc8`, and
  `/private/tmp/g0-auth-product-e5vm4tip`.

Keep `g1-state/control/`, `g1-state/host-bootstrap/`, `g1-state/state.db`, all
delivery ledgers and driver history, and all G0 receipt directories. This
preserves recovery and audit evidence and avoids altering the retained rc3 pin.
