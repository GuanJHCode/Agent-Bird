# F01 受控 worker 跨平台夹具修复

已完成最小 test-only 修复与本机 focused 验证，等待 ROOT 重新验收 Linux/Host race。

## 根因证据

ROOT 已实际在 Linux 镜像运行 Host selector，原日志为旧 worktree `tasks/review-retry-v2/data/linux/retry-host.log`。此处只读复制到 `data/linux-fixture/root-linux-host-before.log` 保留证据：5 named pass、3 named fail（失败为 lifecycle 父测试及 session=false/true 两个子例）；两个子例均报 `open /usr/bin/true: no such file or directory`。读取测试 pin 时失败，尚未启动 dependency/review workers，因此属于 test fixture 平台绝对工具路径依赖，不是 Store/Host 生命周期断言失败。ROOT 已清理原容器。本子任务未重开容器或冒充本机 Linux 复现。

本机 `go1.26.8 darwin/arm64` 仍有 `/usr/bin/true`，改前相同实际 worker 测试通过（`data/linux-fixture/macos-before.jsonl`）：1 package、1 top-level、3 named pass；fail/skip 均0。它是 macOS baseline，不是 Linux RED；已有 ROOT Linux 日志才是所报告的平台失败证据。

## 改动

只修改 `tools/orchestrator/internal/host/candidate_test.go` 的 F01 新链与其 dependency fixture：

- 固定 helper 模式统一使用 `filepath.EvalSymlinks(os.Args[0])` 得到当前 Go test executable。`silent` 模式正常 exit0、无 stdout；只有 `session` 模式输出已有 Claude-shaped init/result lines 并 exit0。未知 helper 模式失败，不默默降级。
- edit/validate dependencies 和无 session review 均使用 `silent` helper；有 session review 使用同一 executable 的 `session` helper。
- ProviderLock 测试 pin 的 Path/SHA256 都来自实际 test executable，Version 明确为 `controlled-test-worker`。这不是已安装 Claude CLI，也不证明真实 Provider qualification；仍只证明受控实际 owned process/decoder/event/artifact/Store/retry 契约。
- CandidateAction 的 dependency command 引用同一个 test executable 及固定 helper selector，不再引用 `/usr/bin/true`。
- 保留实际 `h.execute`→真实 artifact/event→Store→QueueRetry→ClaimReady 链、无/有 session、实际 exit0、完整 identity/sequence、冻结 Input/DependencyInputs 和新 attempt/segment 的全部原断言；未改 production/Store source，未放宽断言。

按 ROOT 明确 scope 裁定，文件内两个无关旧测试 `TestStopSegmentCancelsCandidateFreezeAfterProviderExit`、`TestCancellationCannotEraseIntegratedResult` 的原 `/usr/bin/true` 路径保留；Linux 本批 selector 不覆盖它们。未修改旧 report/ledger，避免后续 cherry-pick 冲突。

## 本次验证

在新 worktree `review-retry-linux-fixture` 的 `tools/orchestrator` 执行：

```sh
PATH=/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin:$PATH TMPDIR=/private/tmp go test -json -count=1 ./internal/host -run '^TestCandidateReviewInvalidWorkerEmitsV2Lifecycle$'
```

改后 `data/linux-fixture/macos-after.jsonl` 完整结束 exit0：1 package pass、1 top-level pass、3 named pass，fail/skip 均0（父测试及 session=false/true 两子例）。实际 dependency 和 review worker 都是 Go test executable，没有用外部 true 工具。Go 文件已 gofmt，`git diff --check` exit0。

已完成：根因记录、限定测试修复、本机 focused 验证与证据保留。未执行/未验证：改后 Linux、Host race/full/native/容器及真实 Provider；ROOT 持共享验收资源并将在此测试修复后重新 Host final race/Linux，原 Store race证据是否复用由 ROOT 判断。未覆盖旧 worktree，未修改其任何文件。没有本任务待删除临时文件；保留交付文档与验证证据。
