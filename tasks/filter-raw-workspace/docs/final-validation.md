# Original Host raw filter materialization

Production and final fixture: `8e50b88cb41f870c5e787f7336af5c63c3c12218`; Go tree `d7c374f1ec0f07cffbfb91ec6c67ec369cf173e6`. Independent fixed-source production/fixture review: PASS, no remaining blocker. ROOT inspected the raw source/command bindings, test events and physical fixture absence; see `data/root-verification.json`.

The private live API reads only the pinned CandidateOID tree: bounded ls-tree and raw cat-file verify object size/OID/content; candidate attribute blobs must exactly match source pins, and every effective filter path must match the manifest. B→smudge R→clean B2 must round trip in the original Broker's three slots. Raw workspace writes use held parent/root descriptors, bounded current-owner single-link regular files and explicit0600/0700 modes. Actual R bytes are bounded to256MiB; the4096-entry/256KiB path budget includes `.git`. Structural binding is compared against observations immediately after the original no-checkout worktree creation, not adopted as creation proof.

Before mutation the Broker's live ManifestSHA256/ScopeSHA256/ReservationID/AttemptID/PlanRevision must match the source and the original Host's Go-only expected grant identity. Receipt publication is followed by the same live stream/source/child, HEAD/tree/index and exact anchored file verification used by Verify. Receipt deep copies cannot restore the private live object. Close closes only its original workspace handles/child binding, leaving source and Broker to the original Host. No cleanup or rollback deletion of a partial production workspace is introduced.

## Verification

Pinned Go1.26.8, Darwin arm64, TMPDIR=/private/tmp. Each command started at clean8e50b88:

- `go test -race -count=1 -json ./internal/gitops -run '^TestFilterRawMaterializationNativeFixture$'`: **1 top-level +4 subcases =5 named PASS**,0FAIL/0SKIP, exit0. Raw SHA256 `714039906583b6ca45929f631b2ee5e85a562c6cecd312cd2a3bce64bef42a00`.
- `go build ./...`: exit0, empty stdout/stderr.
- Linux arm64 CGO0 `go build ./...`: exit0, empty stdout/stderr; compile-only, no Linux native Git/filter claim.
- `go vet ./internal/gitops ./internal/gitfilter ./internal/gitopsworker`: exit0, empty stdout/stderr.

The four native subcases prove actual XOR B≠R and roundtrip/receipt/exec/control bytes, actual mismatched clean/smudge rejection with retained workspace, candidate attribute bytes differing from valid source pins before target creation, and receipt callback mutation rejection. Scope/reservation/injected-manifest binding rejection shares the successful native fixture and must occur before any Git journal increment, target or filter start. The manifest mismatch is an injected binding observation, not a separate authenticated Broker implementation.

All four actor builders have original Wait/tree success. Fixture Git has13 original successful processes per subcase; materialization journal assertions bind547/194/109/547 original Git intent/spawned/exited records to identical original process identities, respectively. Three Brokers have live known terminal observations; attribute rejection never starts its filter. All four owned fixture roots were removed only after those proofs and independently rechecked absent by ROOT. This is synthetic Host grant fixture evidence, not Store authorization or public Provider qualification.

## Failure provenance

ROOT clean4b872a8 ran `journal-drift-red` against the actual af4e4e8 production: callback changed README after recording the receipt, but materialization returned a live object with nil error. Parent+sub2FAIL, exit1, raw SHA256 `5d647e93524749cdbdc981bc92c1ca3edd309f65a2c64d0a7f2177867e3d9916`. Original builder/Git/Broker proof and known cleanup were retained. 8e50b88 fixes this defect; the raw RED is unchanged.

Earlier worker fixture runs are retained in `tasks/filter-raw-fixture/data`: task-path and manifest failures were fixture defects; source641bdb4's two stub failures used unused stream doubles, and the attribute case returned at OpenFilterGit. They do not prove a native Broker chain. ROOT rejected that fixture, took over the incomplete98ca21d revision, fixed missing context and lifecycle/oracle gaps, and ran the original recorded commands above.

A separate worker-started `go test ./internal/gitops -run TestFilterRawMaterializationNativeFixture -count=1` was interrupted by the worker after observing PID14242. There was no retained session/raw output, no original Wait/tree, no original descendant/PGID evidence, and the kill return was masked by `|| true`. Its later empty tmp listing does not prove creation/deletion. This attempt is excluded from validation and cleanup claims; no cold cleanup was attempted by ROOT.

## Remaining scope

Only the private raw materialization stage is complete. Prepare/freeze R→B→R→B, accepted-candidate validator reconstruction, strict outer filter attachment decoding, Host integration and public admission remain. Existing public filter rejection is unchanged. The most recent F13 package still targets sourcee662aaa and does not cover this delta. F12 Formal60 remains FAILED; this batch establishes neither whole-disk/2GiB bounds nor RSS qualification. Intel Mac/Windows native, real model/TUI/Hook work remain deferred to their required environment/authorization. No user installation, real model, push or release was used.
