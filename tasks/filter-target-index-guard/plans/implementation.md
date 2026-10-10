# Target index guard implementation plan

**Goal:** Pin the target index and target Git metadata before any target-index-consuming filtered Git command, and reject drift at each process boundary.

**Scope:** `internal/gitops` only; preserve ordinary `OpenFilterGit` behavior and keep observer-held descriptors independent from source-held descriptors.

1. Add a DARWIN native RED test where an intent-journal callback changes `.git/index`; assert the subsequent Git start seam is never reached.
2. Add a source-owned retained target-index guard and `OpenFilterTargetGit`; install it after safe repository resolution and before state capture, and release its FDs from source close.
3. Revalidate that guard after every intent journal callback and before each filtered Git start, covering byte and input-stream runners without Git-based verification.
4. Let `InspectFilteredTarget` independently install a source guard for an ordinary source; retain observer FDs separately. Run the focused test, format, and commit only scoped files.
