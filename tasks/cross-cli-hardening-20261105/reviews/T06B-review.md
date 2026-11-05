# T06B 独立复核

结论：PASS。范围限 base `7242fd95a43848f489e162f52a320e39fba9d0c1` 之上的冻结四文件；未发现需要修改的缺陷。未改源码、派生 Agent、commit 或 push。

## 源码合同

- `summary.go:57-95`：第一轮已完成 `summarizeRunFacts` 的 Rows 关闭和 tx Rollback 后释放 mu；随后才等待 DB 级两槽及执行 verifier。`summary.go:229-240` 明确关闭 Rows、在返回时 Rollback，释放 SetMaxOpenConns(1) 的唯一连接。
- `summary.go:95-123`：再次调用既有 `ValidateTaskOwner`，重新读取任务、source event、raw adapter、slot 和 admission；任务或事实不同则仅留 status/collect 并移除候选投影，没有无限重试。owner 查询未加入 owner-live/ancestry。
- `summary.go:250-280`：候选 DB/source facts 独立加载，返工资格失败不阻断不可变候选校验。`owner_rework.go:211-314` 仅拆分 DB source facts、独立 artifact/Git verifier、DB slot；mutation 保留 source→外部证据→slot 的原顺序。
- QueueRequestChanges 整个 function 与 base 字节相同；`decisions.go`（ReviewResult）、`owner.go`、`summary_candidate_test.go` 无 diff；公开 RunSummary/SummaryTask/ReviewableResultBinding v1 声明及注释与 base 字节相同。未引入权限 cache。

## 已实际核验的证据

- READ-01：新 `TestSummaryVerifierReleasesWriterAndConnection` 在真实 Git `spawned` journal callback 内阻塞；同 DB run B 的 CreateRun 在 release 之前成功。实际读取 `data/red-actual-git-baseline.json`：原基线同回归因 `run B write blocked by run A verifier` 失败，包 fail，未把基线红算 PASS。
- READ-02：正常/race 覆盖 verifier 间 accept/rework、budget/provider/Host、adapter/event/owner 变化；变更后不保留旧动作与候选，旧 mutation tuple/admission 拒绝。原 occupied-after-git 回归未删改且继续通过。候选在预算/provider/Host 暂不可用时仍有 OID，而 request-changes 不出现。
- READ-03：两槽上限、排队取消、实际 Git spawned→exited 和 ConfirmGroupExited、Git 失败、状态只读与 SQL/slot 释放均通过。

| JSON | 包 | top pass | subtest pass | fail | skip | 包终态 |
| --- | --- | ---: | ---: | ---: | ---: | --- |
| data/final-normal.json | Store | 169 | 236 | 0 | 0 | pass |
| data/final-normal.json | Coordinator | 31 | 55 | 0 | 0 | pass |
| data/final-race.json | Store | 169 | 236 | 0 | 0 | pass |
| data/final-race.json | Coordinator | 31 | 55 | 0 | 0 | pass |
| review/focused-race.json（本审查独立执行） | Store | 16 | 36 | 0 | 0 | pass |

以上均逐条解析 JSON，run 均有终态，无 pending/unfinished；top 与 subtest 分列，未重复表述为顶层测试数。

独立命令：在 tools/orchestrator 内，Go `/private/tmp/go1.26.10-sdk/bin/go`，`GOENV=off GOTOOLCHAIN=local GOPROXY=off TMPDIR=/private/tmp GOMAXPROCS=2 GOCACHE=/private/tmp/t06b-review-gocache`，PATH 前置固定 Codex 0.166.0 bin；执行 `go test -race -count=1 -timeout=5m -run TestSummary -json ./internal/store`，实际 exit 0，包 elapsed 46.457s。

四源码 SHA256 在审查前与独立测试结束后均匹配 `data/source-manifest.json`。解析结果及结束时源码 hash 见 `review/verification.json`。

## 限制

未独立重复全模块 vet/build；空日志本身不作为成功证据。本次是 Store/Coordinator 与真实本地 Git 进程验收，不声明 native TUI 或 installed-package 验收，不扩大到其他任务或全仓。

## 主会话集成签收

root逐文件核集成四源码SHA相同，复核首轮tx/Rows结束后释放mu、锁外有限并发验证、二次授权/事实相等才投影且返回最后观察点。集成Store+Coordinator受影响race124pass/0fail/skip完整结束，package failures0；命令/源码hash/日志SHA见data/public-evidence/T06B-integrated.json。公开v1契约与原mutation函数保留，允许T06B提交并push。此处不扩为SDK升级后/最终包或live验收。
