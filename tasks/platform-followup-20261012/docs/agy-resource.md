# AGY startup and Formal60 attribution

本轮实际复现的 AGY failure 是故障注入测试过期，不能归类为生产 Host 启动故障。`TestAGYCommittedSubmitAndHostFailuresPreserveOriginalReservation/source_host_start_failed` 在 0 秒以 `exact original Host stage boundary changed` 失败：测试寻找旧 `startSourceHost(...)` 字符串，生产启动流程已抽为 `startHost()`。其他受测 AGY 主链和相邻失败用例通过。原 candidate-retention 观察没有更具体 trace，不能倒推每个历史失败均已解决。

修复只改两份测试的边界锚点；注入先 `_ = startHost` 解决 overlay 中闭包不再引用的编译错误，随后立即注入错误，绝不调用该闭包。start_failed 保持未启动故障，timeout 不重复启动 Host。中间 c254fd3/880283c 的“先调用startHost再注入”改变了语义，独立审查拒绝；b5edd51/954ac63 修正后复审通过，未将中间PASS当作正确性证明。

最终定向命令（Go1.26.8 darwin/arm64）：

```
go test -count=1 -json ./cmd/orchestrator -run '^(TestAGYCommittedSubmitAndHostFailuresPreserveOriginalReservation|TestControllerBridgeHostUnavailablePreservesOriginalErrorAndRecovery)$'
```

2顶层+5子用例PASS，0FAIL/0SKIP。主会话已独立解析原始JSON并核对最终diff；证据是合成协议/真实本地进程，不是真实Provider账号/TUI验收。只修改测试注入，没有生产并发/所有权修改，不新增race。

原始证据摘要：

- `data/agy-baseline-count1.jsonl`：12 named PASS、2 named FAIL（失败父/子），SHA256 `d0285ef2a13375fc7ac1930efb8ee39ff5c2fe8cf473eeda63114c83901a1944`。
- `data/agy-host-stage-semantic-final.jsonl`：7 named PASS，SHA256 `625a4aa3c509040938cfcbe995f7dc103a2d2d498ffafe2ceb20596153b1e149`。

Formal60 仍 FAILED：既有128MiB峰值目标内，但8MiB RSS窗口范围超限。随后1200秒受控诊断观察到稳定HeapAlloc低水位和系统/actor paging，不能归因旧Formal60，也不证明最终产品符合资源预算。参见 `tasks/resource-soak/docs/formal-tail-validation.md` 和 `tasks/resource-memory-diagnostic/docs/final-diagnostic.md`。本轮不调整阈值、不强制GC、不重新运行长资源测试；没有证据支持的产品资源补丁。
