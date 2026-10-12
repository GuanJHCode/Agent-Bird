# Filesystem Leaf

Implemented Host slot physical deletion, including registered deletion and the
separate binding discovery lease. The binding lease records canonical namespace
and file snapshots, refuses unowned objects in absent-only mode, and preserves
markers so ordinary Host materialization cannot recreate data.
