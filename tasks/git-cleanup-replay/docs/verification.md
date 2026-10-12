# Verification

原始命令、JSON 事件、stdout、stderr、exit 和统计保存在
`tasks/git-cleanup-replay/data/`。

- `cleanup-replay-race-json-20261012T0010.*`：`go test -json -race -count=1`
  的 focused `internal/gitops` cleanup 回归。JSON `pass` 事件为 5 个 top-level
  tests；无 `skip` 事件。
- `cleanup-history-race-json-20261012T0012.*`：相同模式的 focused
  `internal/gitopsworker` durable-history 回归。JSON `pass` 事件为 2 个
  top-level tests；无 `skip` 事件。

曾运行一次未缓存的全 `internal/gitops`，原始 stdout/stderr/exit 保存在
`full-gitops-test-20261012T0003.*`，exit 为 1。该运行观察到缺少
`tasks/filter-raw-fixture/tmp` 的 native fixture 失败，以及
`TestCandidateErrorWrappingPreservesStandaloneHandleTransfer` 的 timing 断言失败。
没有变更前同环境基线，故这些失败均未归因于既有状态或本次改动；锁修复后未重跑全包。
