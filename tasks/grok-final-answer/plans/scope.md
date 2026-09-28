# 本轮计划:Grok 结果 artifact 只投影最终回答

## 背景与证据

- 已知失败:`tasks/mvp-grok-readonly/docs/delivery.md` 记载真实 Grok 1.0.34 验收中"整体 artifact 精确匹配"未通过:artifact 115 字节 = 工具调用前说明(73B)+ 最终回答(42B)。
- 原始 spool 产物核对(2026-09-28,只读):`result-output.bin` 内容确为 `"I'll read \`INPUT.txt\` with the file tool and return its contents exactly." + "GROK_READ_e0e29a039157c6b36d4e3f9fe0045c1b"`,无分隔。
- 流形状证据:`tasks/mvp-grok-readonly/data/recovery/native-result-verification.json` 记录真实流含 `tool_started`/`tool_completed` 事件(type 字段),`assistant_message_count=2`。
- 代码根因:`internal/adapter/protocol.go` `parseGrok` 将工具事件落到 default → `EventUnknown` 丢弃;`internal/host/protocol.go` `accept()` 把**所有** Grok `text` 块累加进 `p.message`,终态 `end` 无文本时整段拼接成为结果(L262-268、L326-328)。
- 不一致对照:Codex 只保留最后一条 `agent_message`(L259 覆盖式),Claude 终态 `result` 字段即纯最终回答。

## 目标(行为合同变更)

Grok 成功终态的结果文本 = 最后一个工具边界(`tool_started`/`tool_completed`)之后的公开文本拼接;无工具事件时保持现有整段拼接(兼容)。工具事件不投影任何私有字段(工具名留在 Provider 侧)。`end` 之后的任何公开事件(含工具边界)按既有 `provider_event_after_terminal` 拒绝。

## 验收条件

1. 先红后绿的 adapter + host 回归测试(含多次工具调用、thought 不重置、终态后工具事件拒绝)。
2. `tools/orchestrator` 全量 `go test -count=1 ./...`、受影响包 `go test -race`、`go vet ./...`、`go build ./...` 通过;记录实际测试数。
3. 不改动:预算/revision 绑定、session 绑定与拒绝语义、artifact 大小上限、权限、ACK 合同。
4. 文档同步:支持矩阵/provider-runtime 中 Grok artifact 形状描述与新行为一致;历史失败记录保留不改写。
5. 真实模型复核不在本轮(无预算授权),列入交付记录的下一批验收方案。

## 工具链

固定解释器:`tasks/g0-macos/data/go/bin/go`(go1.26.8 darwin/arm64),`GOMODCACHE=tasks/g1-g4-delivery/tmp/build/gomodcache`,`GOPROXY=off`。基线(本轮改动前,2026-09-28):build/vet 通过,`go test -count=1 ./...` 全部 ok(15 包含 cmd/orchestrator 124.7s)。
