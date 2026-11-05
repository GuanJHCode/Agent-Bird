T06B 实现及本地验证完成，待 root 安排的独立审查与集成验收；未 commit/push。基线 HEAD 7242fd95a43848f489e162f52a320e39fba9d0c1。唯一源码写入树：/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t06b。未修改其他工作树及 tasks metadata。

- summary.go:50/57/132：private summaryCandidate 保留 DB-only source、admission、slot；锁内授权并完成 Rows/tx 后释放唯一连接和 mutex，锁外进行 artifact/Git 校验。每个 DB 最多两个 verifier 槽；等待槽时不持 mu/Rows/tx/连接。取消退出释放槽，不写 DB。最后重新进行原 bearer owner 校验及 DB admission，比较完整 SummaryTask、原始 adapter 字节、candidate source tuple 与 slot。任一变化时返回最终读取的 status/counts/budget，但该任务仅投影 status/collect 且清 ReviewableResult；不重试。
- owner_rework.go:102/112/203/216/229/304：拆分 admission DB facts、candidate DB source、外部 evidence、slot DB facts。mutation 仍按原事务中的 admission→source→artifact/Git→slot 顺序执行。QueueRequestChanges 函数体逐字保持，ReviewResult 所在 decisions.go 整文件保持；SHA证据见 data/mutation-preservation.json。未增加连接数、schema、公开 DTO 字段或预算决策。
- store.go:34：仅新增 private 2-slot channel 与 test-only verifier barrier。RunSummary version=1、ReviewableResultBinding 原字段不变；无 OID/ref/owner/revision/授权缓存。
- 候选有效性独立于返工资格。预算耗尽、provider disabled 或 Host offline 时仍验证并返回有效 CandidateOID，同时保留真实 blocked reason、不投影 request-changes。既有 legacy、last-attempt、v3、ACK 与 accept/rework 互斥回归保留。

新增 summary_snapshot_test.go 共 6 顶层、17 tests/subtests：

1. READ-01 在真实 Git spawned journal 回调中阻塞 run A；未释放前同一 DB 的 run B CreateRun 成功写入，证明 mutex 和 SetMaxOpenConns(1) 连接均已释放。将原 HEAD summary.go/owner_rework.go 临时恢复后同一测试真实变红：run B write blocked by run A verifier（data/red-actual-git-baseline.json）；恢复实现后最终 normal/race 均通过。
2. READ-02 verifier 暂停期间分别执行真实 accept、rework，以及 budget/provider/Host/adapter/event/owner 变化，验证旧 mutation 动作及 binding 丢弃、旧 tuple/admission 拒绝。初始 availability 不可用时候选仍保留。既有并发 accept/rework CAS 回归纳入全量。
3. READ-03 八个 inspect 验证两槽上限与排队/运行取消，检查槽、唯一连接及任务 revision/decision 均无泄漏或写入；真实 Git spawned 时取消，检查 exited 记录和 ConfirmGroupExited；删除 frozen private ref 制造真实 Git 失败，验证不投影 CandidateOID/request-changes、不写 DB。

最终源码冻结后执行，全部会话已退出：

- Go 实际版本 go1.26.10 darwin/arm64，SDK binary 为 /private/tmp/go1.26.10-sdk/bin/go；给定的 bin/go1.26.10 文件不存在。GOENV=off、GOTOOLCHAIN=local、GOPROXY=off、TMPDIR=/private/tmp、GOMAXPROCS=2；normal/race/vet/build 使用独立 GOCACHE。
- go test -count=1 -timeout=10m ./internal/store ./internal/coordinator -json：store 405 tests/subtests（169 顶层）、coordinator 86（31 顶层），总 491，0 fail/skip；两包 PASS，exit0。
- go test -race -count=1 -timeout=10m ./internal/store ./internal/coordinator -json：相同 491（200 顶层），0 fail/skip，无 DATA RACE；两包 PASS，exit0。
- 新增 6 顶层/17 tests-subtests 另以显式固定 0.166 bin PATH 执行 race：17 PASS，0 fail/skip，exit0。全量运行继承 PATH 已含固定 0.166 codex-path；套件未调用 Codex。未使用真实模型或 key。
- module go vet ./...、go build ./...：均 exit0。git diff --check：exit0。实际 JSON 日志、精确计数及状态在 data/{final-normal.json,final-race.json,final-pinned-path-race.json,test-counts.json,execution-status.json}；复跑脚本 scripts/verify.sh。

交付完整补丁 data/T06B.patch（包含新增未跟踪测试）SHA256 a80cfb333688cf1548bd6fe65afe0ef0f1b85d81dbc826be4c0d9dbf47e5ba07；data/source-manifest.json SHA256 907812e7cdb6eaf557baec6bb168fdf3fb91c1a8b54c8c9264118ce10b10495d。

限制：summary 是最终 DB 重核时点的观察值；锁外 evidence 也可能在返回后变化，mutation 仍按原事务实时核验，summary 不提供授权。第一版无缓存、不无限重读。未运行 native TUI/真实 provider 或整个仓库全量测试；本功能已运行完整 Store/Coordinator normal/race 和整个 orchestrator module vet/build。独立审查及 root 集成未完成，不能据此声明整体签收。未改项目记录，交由 root 维护。已删除仅用于恢复 baseline 的两份 external tmp 源码备份；保留补丁、JSON日志、报告及独立 GOCACHE 供审查复跑。
