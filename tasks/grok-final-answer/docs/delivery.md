# 交付:Grok 结果 artifact 只投影最终回答(2026-09-28)

## 修复内容与可观察变化

Grok 流式协议中 `tool_started`/`tool_completed` 此前被解析器丢弃,Host 把**全部**公开 `text` 块整段拼接作为结果,导致 `tasks/mvp-grok-readonly` 真实验收"整体 artifact 精确匹配"失败(115B artifact = 73B 工具调用前说明 + 42B 最终回答)。现在:

- 用户/主脑可观察:新版 Grok 只读任务的 result artifact = 最后一个工具边界之后的公开文本(纯最终回答);无工具事件的运行行为不变(单消息即全量文本)。
- `end` 之后到达的文本**或工具边界事件**均按 `provider_event_after_terminal` 拒绝(旧版工具事件被静默忽略)。
- 工具边界事件携带的 sessionId 参与既有 granted-session 一致性校验,不匹配报 `provider_session_changed`(旧版解析器直接丢弃 session 字段,不校验)。
- 历史 artifact 与失败记录未改写;已交付的旧 artifact 不变。

## 修改文件与关键位置

- `tools/orchestrator/internal/adapter/protocol.go` `parseGrok`:新增 `case "tool_started", "tool_completed"` → `Event{Kind: EventProgress, SessionID, Status: typeName}`,不投影工具名/结果(隐私面仅固定类型名)。
- `tools/orchestrator/internal/host/protocol.go` `accept()`:Grok 终态后拒绝扩展到 EventProgress;Grok EventProgress 重置 `p.message`,使 `Finish()` 回退文本只含最终段。
- 测试:`internal/adapter/adapter_test.go` `TestParseGrokToolEventsProjectBoundaryOnly`;`internal/host/grok_protocol_test.go` `TestGrokToolBoundaryKeepsFinalAnswerOnly`、`TestGrokKeepsTextAfterLastToolBoundary`、`TestGrokRejectsToolEventAfterTerminal`、`TestGrokRejectsToolEventWithDifferentGrantedSession`。
- 文档:`docs/provider-runtime.md`(Grok 增量段新增 2026-09-28 边界说明)、`docs/quick-start.md`(Grok 使用说明改写为最终回答语义)。

## 实际验证(TDD)

- 红灯:`data/adapter-red.log`、`data/host-red.log` —— 5 个新测试全部按预期失败,host 红测直接复现线上拼接缺陷(`Text:I'll read INPUT.txt and return it.GROK_READ_final`)。
- 绿灯:`data/green-focused.log`(adapter+host 全包)、`data/race-host-final.log`(host race 通过)。
- 全量:改动定型后 `go build ./...`、`go vet ./...` 通过;`go test -count=1 ./...` exit 0,**596 具名测试通过 / 0 失败**(全 16 包 ok,含 cmd/orchestrator 124.7s);`go test -race` adapter/host/store 三包通过(`data/race-affected.log`)。证据:`data/go-test-final.jsonl`。
- 基线(改动前同日):build/vet/全量测试 exit 0,记录于 `plans/scope.md`。
- 现场复核(只读):历史 spool `result-output.bin` 115B 内容确认为无分隔拼接,与修复前语义一致。
- 独立只读审查:结论"基本通过,2 处需确认";已按其建议补 session 校验回归测试;其提出的"oversized 结构化行失败闭锁"为**本轮之前既有规则**(`protocol.go:183`,非本轮 diff),"error 终态残文进入失败 artifact"为既有回退路径的语义变化(失败仍按 failed 处理,不构成误验收)。

## 未完成 / 未验证

- 真实 Grok 1.0.34 模型复核未执行(无本轮预算授权)。红→绿证据均为协议 fixture。原生 TUI/安装后验收不在本轮范围。
- 真实流若存在本轮未知类型的新增边界事件(如未来版本改变 tool 事件命名),退化行为等同旧版整段拼接,不会丢文本报错;需真实复核确认。
- 未 push、未更新已安装插件、未替换发布包(授权边界内)。

## 下一批建议

1. 经用户预算授权后,按 `tasks/mvp-grok-readonly/data/native-trial-plan.json` 同形制对新构建做一次只读复核,断言 artifact 哈希 == 最终回答哈希。
2. 候选缺口(本轮调查记录):超时分支仅 fixture(`support-matrix.md` L28)、重试后累计预算验收待补(L39)、Worker 非递归派工 guard 真实效果待验收(L36)。
