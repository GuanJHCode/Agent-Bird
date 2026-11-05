# ADR-001：实施边界与兼容基线

状态：采用；具体 schema/新 wire 结构在对应任务实施前另立 ADR。

保留 Go Coordinator + SQLite、现有 owner/thread/epoch、candidate/revision 和 durable receipt；不建立平行业务数据库。旧 v1 max_active_ms 保持每段语义和默认组限额，旧 ReworkContext v1/v2 字节/digest 不回写。新预算/引用仅通过显式版本和 queue/launch 门禁引入，不自动降级。

总预算以 budget_group_id 为权威，retry/rework/split 不能补额，unknown 保留预占。原生 conversation 仅核验后声明，managed_process 无法观察切聊天时主动推送关闭。MCP 薄适配现有授权；accept 与 ACK 显式且独立。typed resume、递归 worker、AGY/Grok coding 保持关闭。

用户要求每功能独审后提交并 push；审查原始 diff/实际证据，不允许实现者自审。工作树隔离，最终测试绑定冻结树和唯一包身份。实现发现合同有关键歧义时先记录具体决策，不静默改变旧语义。
