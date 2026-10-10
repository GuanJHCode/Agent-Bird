# Target transition boundary investigation

Read-only investigation against qualified integration 2e3cbb7. This is not an
implemented transition, public merge contract or native acceptance result.

A new original-owner before-to-after object must independently hold target
parent/root/gitDir/common/ref-parent directory anchors (device, inode, mode,
UID), initial named index FD/hash/vector, symbolic HEAD and base/ref resolution
(loose/packed), private-ref pins, base tree and initial R inventory. Expected
final R inventory comes from the live accepted candidate by path/mode/size/hash;
inodes from another worktree are not comparable. Preserve manifest/stream
binding/tool/config/attributes/repository pins, candidate child gitDir/index/
HEAD/workspace pins, integration lock and initial absence of operation metadata.

The following current guards are strictly readonly and cannot span normal merge:

- `internal/gitops/filter_target.go:273-335`: Verify/final pure check freeze
  HEAD/ref at Base, original index identity/hash and base inventory.
- `filter_target_index_guard.go:66-84,118-175`: refuses index.lock and requires
  original named index inode/hash.
- `filter_context.go:180-215`: Verify recursively invokes that target guard.
- `filter_target_pair.go:152-195`: both direct target verification and candidate
  verification return to the readonly source guard.

Normal merge may create index.lock and atomically replace index, update/create
loose target ref, change tracked files and affected directories, append reflogs
and write ORIG_HEAD. At final acceptance all transient locks and MERGE_HEAD-like
metadata must be absent, and HEAD/ref/tree/index/worktree must match the accepted
candidate. Symbolic HEAD, packed-refs, candidate privateRef, config/attributes/
tools and unchanged tracked paths still retain their initial constraints.

Reuse the existing directory-anchor algorithms, pure source/repository-file
verification, config/attribute pins, index parser and candidate child checks in
a transition-specific composition. Add only a candidate-side pure verifier for
this transition; do not expose a generic skip-target pair verification mode.
Existing Integrate preflight/final checks (`gitops.go:962-1115`) remain required.

Callback boundaries:

1. After PersistIntegrationIntent, BeforeMerge, and Git intent callback, perform
   the complete initial verification.
2. From spawned through original Wait, Git can legitimately mutate target index/
   files. Verify immutable anchors/source/candidate/privateRef plus transition
   generation; readonly index/inventory verification is no longer valid.
3. After original exit zero and full lifecycle proof, establish the candidate
   final semantic state plus final named identities. Unknown/nonzero paths never
   bless a new baseline and retain uncertainty.
4. Before RecordIntegration(Integrated), capture the complete final snapshot.
   After the callback, perform exact no-Git revalidation of that same snapshot
   to close the final callback mutation gap.

A same-state callback write during running Git cannot be attributed to a writer
using only filesystem end-state checks. This contract would constrain the one
authorized before-to-after state envelope; it cannot claim per-write provenance.
Stronger writer attribution needs additional OS enforcement or launch barriers.
Normal hooks and signing remain enabled; durable hook effects outside the
accepted final inventory cannot be silently permitted.
