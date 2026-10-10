# F04 durable filter resource layer

Broker production2e649f3/testc767fa0, original terminal boundarye34c3ce, Store production2042436/test0bd3bea and migration fixture092410a have independent PASS reviews. Combined merge1450deb retains their exact source files; ROOT verified every Store delivery receipt hash and Broker source against the final race snapshot. Host closed-gate production49d8789 is a separate integration guard, pending incremental independent review. Public filter admission remains closed.

## Behavior

Darwin Broker seals two bounded native tools, owns three initialized/alias-checked streams and serial transforms, uses the original Host Spool instance and durable bounded journal, retains original live monotonic Start/Wait+tree terminal times, refuses cold adoption and keeps uncertain resources. Exact inventory/held-named identity/hash checks precede operations and cleanup. Partial initialization or failed journal/cleanup retains UNKNOWN resources; no RemoveAll or automatic cold refund.

ROOT found a refund bug during review: unlink alone leaves disk occupied by open original descriptors. Actual refund RED reproduced it with real XOR filter processes. Production2e649f3 closes every unlinked original file and removed directory/root handle before emitting removed journal/cleanup receipt; c767fa0 closes the caller-owned alias in the test before cleanup. Broker never represents proof of a separate Provider process; Host must prove both families before eventual terminal release.

Schema36 reserves112MiB raw and131074 metadata in the existing owner transaction, binds original physical Host, rejects adoption after rebind and releases only once with authenticated settled known terminal cleanup. Metadata remains permanently charged. A separate pinned ValidationTool may coexist; automatic retry remains refused. See tasks/git-filter-reservations/docs/final-validation.md for accounting, migration and controlled Store-event limits.

49d8789 keeps ExecuteLaunch closed before invocation inspection or ledger/state mutation. Actual b5c082a RED exercised both producer and validator: the old code reached Args and returned invalid_launch_grant. Final race proves explicit git_filter_pipeline_unsupported for valid authority, untouched spool/budget/active maps, without starting a process. No public filter support is introduced by decoding the new attachment.

## Evidence

All counts below include named parent tests; subtests are stated separately. Full command/source/raw/exit and SHA values are retained in data/broker-host-summary.json and data/resource-broker-root-verification.json. No full Store or full-module test rerun.

- Broker final race clean c767fa0:2top+8sub=10named PASS,0FAIL/SKIP,exit0; SHAa109b388dbb0fffbe534df2c223be62cdbe1b7ed25bcb538ec2a01e5932753a7. Real native XOR round-trip, known exit7, original Wait/tree, cold/stage/tool drift refusal, initialized/inode-alias slots and original FD closure. Both owned fixture roots independently confirmed absent.
- Store resource race clean0bd3bea:12top/85named PASS (contract8top/65named, Store4top/20named),0FAIL/SKIP,exit0; SHA85898e35cfef36f6941d4136a7d96a31b1b040376dc594f74276c3b9cb10750b. Migration-only green092410a:4top/8named PASS; SHAe0b8a1fea1cc8b6f3d9cbd0374c7b20bf00fa7eb44f8aa0426212fa7f51f145d. Unchanged scoped evidence reused after composition.
- Host gate final race clean49d8789:1top+2sub=3named PASS,0FAIL/SKIP,exit0. Whole build and contract/adapter/Store/events/gitfilter/Host vet at the same clean combined source each exit0 with empty stdout/stderr.
- Original scaffold RED, missing-field compile failure, terminal RED, JSON-oracle failure, open-FD refund RED, historical schema fixture RED and Host gate RED remain failures in the saved raw evidence. They are not counted as successful batches.

Linux Store resource-only verification is isolated in tasks/git-filter-resource-linux; it does not exercise native Broker/Git. Host active-span/terminal cleanup wiring and complete canonical B/materialized R Git implement→validate chain remain subsequent work. No full LFS, review/compose/target/newbase/continuation support, power-loss or formal resource qualification. Formal60 remains FAILED; Intel/Windows native deferred. No real model/account, user installation replacement, push or release.
