# Host Slot Filesystem Leaf

Scope: the registered Host slot physical deletion primitive only. Store
eligibility, SQL transitions, refund timing, public surface integration, and
unregistered empty-slot tombstones remain outside this leaf.

The primitive binds a canonical plan to the Host descriptor, exact original
proof, and request ID. It holds the existing `writer.lock`, validates the
spool root/object/intent/lock inode chain, persists `deletion.marker`, and
removes only `data.bin`. Replays use the identical canonical plan; they may
resume after the marker exists and data is absent, but reject recreated data,
foreign descriptors, and namespace replacement.
