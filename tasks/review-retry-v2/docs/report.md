# F01 readonly review failure v2 实施报告

## 结果与范围

已实施并自检。ROOT 回报原实现的独立 code-reviewer spec/quality 审查 PASS、无 blocker，并验收实际 Host 链；本次 LOW 测试质量修正已验证，等待 ROOT 对测试增量轻量复核与公共最终验收。生产改动仅 `tools/orchestrator/internal/host/candidate.go` 和 `tools/orchestrator/internal/store/review_failure_retry.go`；测试仅其 colocated test。未修改公共 DTO/schema、owner recovery、`host.go`。仅本 worktree 写入，未合入、push、发布或调用外部 Provider/model/account/network/native CLI/容器/安装器。

- Host 新 `candidate_review_invalid` 诊断写入整数 `writer_version:2`；其他诊断保持旧格式。
- Store 版本仅来自经过 artifact provenance/hash/size/path/private guards 认证的诊断 body，严格拒绝未知版本、非整数、null、错误字段/重复字段；未版本化旧历史继续旧生命周期规则和正 exit 要求，不升级历史。
- v2 必须唯一 Prepared→Spawned→Running→Failed→Exited，producer sequence 严格增加，完整 run/task/attempt/segment/command/work_revision/producer tuple 一致；Prepared 无 Process，其余持相同完整 Process；过程事件不带 terminal artifact/exit。
- Session 可选且最多一次，仅 Running 后、Failed 前，同 Process/tuple/session identity，且不带 artifact/exit。依据实际 `protocol.go:403`/`reportSession` 的单次回报契约。
- 保留 artifact 认证与显式 owner/readonly-review/原冻结输入/预算约束；没有扩展自动重试或可写 stage 重试。

## ROOT 裁定与代价

原计划的合成终结假设与真实 Host 契约冲突。ROOT 裁定保留实际 worker 退出码：schema-invalid 可以是物理 exit=0，v2 接受相同非负 exit，负值拒绝；legacy 仍需正 exit。Failed.PayloadHash 必须是诊断 artifact SHA256；Exited.Artifact 必须相同，但 Exited.PayloadHash 必须是 `hashText("exited:"+SegmentID)`，与 `host.go:783/846` 实际逻辑一致。没有伪造 Wait 结果或修改公共生命周期。裁定已记录于 `tasks/review-retry-v2/plans/implementation.md`。

## 源码位置

- `tools/orchestrator/internal/host/candidate.go:96`：诊断 writer version。
- `tools/orchestrator/internal/store/review_failure_retry.go:18`：保留现有证明与认证 guards；只有认证 body 的 v2 选择新校验。
- `tools/orchestrator/internal/store/review_failure_retry.go:132`：严格整数版本解析；`:155`：v2 生命周期状态机。
- `tools/orchestrator/internal/host/candidate_test.go:181`：实际本地 worker 生命周期、两条无 session/有 Claude protocol session 路径；`:270`：readonly review 计划、受控实际 dependency workers 的 Store bridge fixture。
- `tools/orchestrator/internal/store/review_failure_retry_test.go:269`：Claude/Grok，exit 0/7，session 有/无，共8个完整 QueueRetry→ClaimReady 正例；`:287`：生命周期/身份/哈希/负退出反例；`:403`：版本反例；`:415`：legacy 部分历史规则保留；`:429`：session 证据；`:487`：保持正确顺序但重复 session 的反例。

## 有效证据

工具链：`/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go`，实际 `go version go1.26.8 darwin/arm64`；全部 `TMPDIR=/private/tmp`、uncached `-count=1`。命令在 `tools/orchestrator` 执行，JSON 输出均在本任务 `data/`。

| 证据 | 命令核心/分类 | package P/F/S | top-level P/F/S | named P/F/S |
|---|---|---|---|---|
| `data/baseline.jsonl` | `go test -json -count=1 ./internal/host ./internal/store -run 'Test(CandidateFailureClassifies|CandidateReviewDiagnostic)'` | 2/0/0 | 4/0/0 | 35/0/0 |
| `data/red-version.jsonl` | v2 新测试最初运行；Host missing writer_version、合法 v2 exit0/7 被旧 Store 拒绝是行为 RED；其余部分失败为 SQL fixture 设置问题（见下），整次不作为干净整体 RED | 0/2/0 | 1/3/0 | 34/17/0 |
| `data/red-lifecycle-corrected.jsonl` | 版本解析最小实施后，同 v2 regex；8个非法生命周期被错误授权（行为 RED） | 1/1/0 | 3/1/0 | 42/9/0 |
| `data/red-session-binding.jsonl` | `go test -json -count=1 ./internal/store -run '^TestCandidateReviewDiagnosticV2RejectsSessionEvidence$'`；other_identity/other_kind 被错误授权（行为 RED） | 0/1/0 | 0/1/0 | 7/3/0 |
| `data/red-duplicate-session.jsonl` | `go test -json -count=1 ./internal/store -run '^TestCandidateReviewDiagnosticV2RejectsDuplicateSession$'`；合法顺序中的重复 session 被错误授权（行为 RED） | 0/1/0 | 0/1/0 | 0/1/0 |
| `data/final-focused-v3.jsonl` | `go test -json -count=1 ./internal/host ./internal/store -run 'Test(CandidateFailureClassifies|CandidateReview|DecodeCandidateReview)'`，原实施验收快照，完整结束 exit0 | 2/0/0 | 30/0/0 | 148/0/0 |
| `data/review-delta-focused-final.jsonl` | `go test -json -count=1 ./internal/store -run '^TestCandidateReviewDiagnosticV2Rejects(InvalidLifecycle|SessionEvidence)$'`；LOW 修正后的聚焦验收，exit0 | 1/0/0 | 2/0/0 | 47/0/0 |
| `data/final-focused-review-delta.jsonl` | 同既有最终 regex，最后测试修改后完整结束 exit0 | 2/0/0 | 30/0/0 | 148/0/0 |

named 包括父测试及子测试，不能当作148个独立顶层测试。本次最终 package elapsed Host 1.594s，Store 26.493s（原 v3 为 1.470s/25.955s）。`go vet ./internal/host ./internal/store` exit0，`data/vet.log` 空（无诊断）。`git diff --check` exit0。Go 文件已 gofmt。

实际 Host 正例不是手写事件：`/usr/bin/true` 或当前 Go test binary 的受控 worker 经 `h.execute`，调用真实 `decodeCandidateReview`，真实 Start/Wait/identity/spool/artifact 写入。两条路径由 SubmitPlan→RegisterHost→ClaimReady 取得真实 readonly review grant；edit/validate 也经实际 local worker/Host 产出 artifact，Store ReviewResult 接受；review 真实 emitted events 逐个 CommitHostEvent，CollectPending→owner QueueRetry→ClaimReady 成功，断言新的 attempt/segment 与原冻结 Input/DependencyInputs 完全相同。

## 设置失败与中间证据

`red-version.jsonl` 中12个 extra-event 子例因新增 SQL 错用不存在的列失败，仅 Host 和合法 v2 正例是有效缺失行为证据。随后 `red-version-corrected.jsonl` 与 `red-lifecycle.jsonl` 的 Store 子例因为故意留 sequence gap 违反 CommitHostEvent 的连续 producer sequence admission，属于 fixture 设置失败，不能计作产品行为 RED。修正为真实连续 admission 后得到 `red-lifecycle-corrected.jsonl` 的有效8个行为失败：missing_prepared、missing_spawned、prepared_process、spawned_process、order_prepared、sequence_collision、exit_hash、session_before_running。所有设置问题已修复，最终日志无失败/错误/跳过。原日志保留追溯；`green-focused.jsonl`、`green-host-owner-retry.jsonl`、`green-session-legacy.jsonl`、`final-focused.jsonl`、`final-focused-v2.jsonl` 是中间成功快照，原实施结论使用 v3，本次测试增量结论使用 `final-focused-review-delta.jsonl`。

## 局限与下一步

已完成：限定生产变更、行为 RED、受控真实 Host→Store→显式 retry/claim 链、自检、聚焦测试和 vet。原实现独立审查已由 ROOT 回报 PASS。未完成/未验证：本次仅测试增量的轻量审查、公共最终验收与合入；按授权未运行全量、race、native app/真实 Provider CLI/安装 Hook/容器。受控 Host 测试直接进入内部 `h.execute`，验证实际 owned worker 生命周期、真实 review decoder、artifact/Store/retry契约；没有宣称 ExecuteLaunch 的完整 Provider binary/profile admission、Git materialization 或 native TUI 验收。依赖业务 body 固定在受控 finalizer，但其进程/事件/artifact 均实际由 Host 生成。

没有创建待清理的任务临时文件；运行时测试 temp dirs 由 Go 测试清理。交付物、plan、JSON 验证证据保留。所有不适用/未获授权验收留给 ROOT，不以本报告代替整体完成。

## 独立审查 LOW 测试质量修正

ROOT 回传独立 code-reviewer 的 spec/quality PASS（无 blocker）及 build66396 exit0；该外部 build session 结果由 ROOT 验收，本报告未自行读取其 session 输出。LOW 反馈指出原 order/kind/session-placement 反例可能同时触发 sequence 碰撞或 SQL/body 排序不一致，不能精确归因于阶段约束。

本次只修改 `review_failure_retry_test.go` 和任务报告/证据，不改生产文件：order_prepared 变为 Spawned→Prepared→Running；order_spawned 变为 Prepared→Running→Spawned；order_running 变为 Running→Prepared→Spawned。非法 kind 放在 Running 后、Failed 前。Session 时段反例仅移动到 Spawned 后/Running 前或 Failed 后/Exited 前，并保持 Failed 的同一 session identity。`rewriteReviewLifecycleOrderForTest`（`:553`）先腾出 SQL UNIQUE sequence 范围，再同步写 SQL sequence 与 body.Sequence 为唯一连续递增值；独立读取并断言 SQL/body sequence 一致、指定行顺序、事件数量/ID唯一、完整 tuple 与 process identity 不变。专门的 sequence_collision 用例仍保留其有意的序列碰撞。

`data/review-delta-focused.jsonl` 是首次修正后47 named pass的中间证据；增加夹具自检后重新跑得到 `review-delta-focused-final.jsonl` 的47 named pass，随后最终范围148 named pass。没有新增测试 case 或删除旧覆盖，没有 race/full/native 运行，没有因回归失败而削弱断言。此 delta 等待 ROOT 轻量复核。
