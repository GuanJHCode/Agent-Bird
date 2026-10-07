# Resume CLI 接线

唯一 writer：review_retry_v2_worker；base a6ecc08，branch feat/resume-cli-wiring-20261008。只修改 main.go 的 Resume 分支、task_entry.go 转发，新增 resume_control/successor_presence 及测试；不修改 Store/Coordinator/contract，不派生、不启动 native/race/container/Provider。

1. 新 receipt 校验与 successor presence 回调测试，记录 API 缺失编译 RED；最小桩后记录行为 RED。校验 exact fields/non-null/duplicate/unknown、immutable request/cap scope、canonical time。
2. 实现 receipt 校验、单 5s successor 窗口及内存 command selector。仅 matching command + succession_pending + exited 可启动一次；intended/bound 不启动，旧 receipt command 不同只读结束。
3. 实际 private capability 和 durable journal 配合回调 RPC，测试 pending-before-RPC/ACK loss/invalid receipt/Host error，existing receipt 无 bootstrap/Host/Summary 依赖。仅 unsupported + implicit command 才 legacy fallback。
4. low/high Resume command-id flag 接线，显式空 ID 拒绝，仅普通 Resume 高层跳过 bootstrap 前置 gate；typed request 保持旧接口。
5. focused ordinary uncached JSON、vet、gofmt 自检；记录准确计数、限制及源码 hashes，独立 commit 交 ROOT 审查/集成。

严格 capability 的本批含义是调用现有 loadControlCapability 成功先于 journal，不顺带修改其跨 action 既有读取实现；receipt 单独严格解码。旧 legacy fallback 不声称 RPC 幂等，journal 不替代 current Store capability 鉴权。

完成：五步实现/普通 focused 验证结束，六 Go 文件冻结；独立 CLI 审查、race/native/Linux/full 验收交 ROOT。最终准确计数及限制见 `../docs/report.md`，源码 SHA256 见 `../data/source-sha256.json`。

独审 delta：ROOT 修订输出顺序为完整 out.Write 后 complete。实际 journal 故障 writer RED 7 named FAIL → CLI/successor 最终 135 named PASS；short write nil 返回 io.ErrShortWrite；输出错误不调用 complete，complete 错误仍返回且不清理记录。仅两文件源码 delta，证据与剩余验收见报告。
