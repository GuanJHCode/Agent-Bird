# F12 review-fix evidence

Independent review found that a generic `ErrNotExist` fallback could recreate
required subdirectories of an existing damaged owner. Publication now performs
a read-only owner existence check: only a missing owner is initialized; a
missing required child remains an error without changing the tree.

The conflict matrix now includes `sealed_active_inode_drift`: a real crash at
the valid `active` checkpoint leaves a valid stage, then replaces `active.json`
with identical bytes under a new inode. Exact replay returns
`config_generation_conflict` and the full tree identity/content/mtime snapshot
is unchanged. `missing_required_directory` removes `generations` after the
crash and likewise proves rejection without recreating it.

Mac ordinary and race selectors both pass with 10 named subtests, zero failures
and skips, and 10 original Cmd SIGKILL Wait records. `go vet ./internal/install`
passes with no output. Their raw logs are `data/review-fix-ordinary.log`,
`data/review-fix-race.log`, and `data/review-fix-vet.log`.

The independent Linux run used the local fixed image digest with `--pull=never`,
`--network none`, a read-only non-root container, and an exact container CID.
`data/linux/f12-reviewfix-20261008-receipt.json` records binary SHA-256
`fa11fdc775e92d8f6fb826d98a13b946607b0b1bdff8e33bffddc7544836bc1f`,
12 reported passes, zero failures/skips, and successful CID removal. Its raw
log reports the 10 named subtests and 10 SIGKILL Wait records. The cross-built
test binary was hash-verified then deleted.

The main-session full install-package run remains reusable evidence:
`data/root-install-package.jsonl`, SHA-256
`dd0a89d12f2f8c82ed46afe244435fd9aa4655a939c87aff0b80d0dbf874e0f3`,
has 111 top-level and 355 named passes, two helper skips, zero failures, and
46.729 seconds elapsed. It predates this narrow repair, so only its unaffected
coverage is reused.
