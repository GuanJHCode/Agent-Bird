# F13 filtered target current candidate package

`filter-target-current-20261010` accepted clean source
`3e2f24169d79ae519eec4b72e1a3ac512221e52d` on macOS arm64. The repository
tree is `cde4994fd1a629515870be345d19e597e796ca28` and the orchestrator tree is
`abf855fcc9b8c90958fe28cdff8121eec255a310`. This run reused the reviewed F13
runner unchanged and did not modify production, tests, runner, or packaging.

The pinned Go 1.26.8 and Python 3.14.3 paths supplied to the runner produced
version `0.1.0-closure.3e2f24169d79`. All 12 recorded commands exited 0 and
every command stderr file is empty: version, module verification, two
independent builds, build metadata, two packages, task-private install and
replay, Doctor, owned uninstall, and final Doctor. Both binaries have SHA-256
`56decd9f69d91868e0d467a2a1625f8506c1c4ee6b73ac6ef5e04bb83bac486c`; package
manifest SHA-256 is
`16c9245aea59a5f8bb2d4cd3f703633a37f745579c8587839143893a1c8ec3a0`.

The runner independently verified the fixed source closure, equal package
bytes and modes, exact installation replay, healthy Doctor states before and
after uninstall, and preservation of the task-owned runtime-data sentinel.
All raw command files and the complete receipt are retained at
`data/filter-target-current-20261010/`; both build binaries and both package
trees were retained under `tmp/filter-target-current-20261010/` for ROOT
review before the exact cleanup recorded below.

After ROOT's independent review of the raw receipt, cleanup recorded and
fsynced `cleanup-intent.json` with owner, mode, link-count, device/inode,
size, and SHA-256 for each target. It nofollow-rechecked every file and the
package-b directory identities, rechecked that package-b equalled retained
package-a, and removed only `orchestrator-a`, `orchestrator-b`, and
`package-b`. The receipt `cleanup.json` records 30 files and 13 directories
removed, totaling 67,064,412 bytes. Retained package-a still has 28 files and
the runtime-data sentinel is unchanged. The prior `d2260d1` force-add mistake
was corrected by amendment before this cleanup: it removed only Git tracking
of this label's tmp artifacts and did not alter raw data or delete disk files.
No `ps` observation is used as proof of original-command completion; the 12
recorded runner subprocesses had already returned normally.

ROOT subsequently qualified the strengthened target native oracle at
`2ec598fc08e8b3a245d24e4d0985c0f95aed5eb6` (see
[Host preflight qualification](../../filter-target-host/docs/final-validation.md)).
The difference from package source 3e2f241 is only the native test and evidence;
production files are identical. ROOT independently verified all 12 raw exits,
empty stderr, both build hashes, 28 files in each package, 24 source mappings
(the plugin manifest changes only its version), install/replay outputs and
Doctor/uninstall/sentinel results. After cleanup ROOT again verified every
retained package file/hash/mode, all removed paths absent and the unchanged
sentinel. See `data/filter-target-current-20261010/root-verification.json`.

This remains package and task-private installation-management qualification
for its exact source. It does not cover any later production change, real model
quality or complete F01–F13 eligibility. This run did not register hooks,
alter a user installation, run a real Provider/model/account, publish, push,
release, or establish cross-platform or Formal60 resource eligibility.
