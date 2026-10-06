# 2026-10-06 可迭代源码版本

本次交付源码与回归测试，不发布预编译安装包或 Release。既有认证、Provider 配置及 CLI 默认模型保持原契约；未完成的原生能力继续关闭或明确拒绝。

## 本次变化

- 活着的 Source Host 可在 Coordinator 换 epoch 后有界重连、重放原 spool，并恢复原报告能力绑定；重试次数与预算不重置。
- Question、显式 Answer 与手动 Resume 绑定原会话、任务段、工作版本和预算。未启动的拒绝使用独立终态，零执行结算。
- 重试决策固定原事件与任务局部 attempt 编号，保留共享预算容量；UNKNOWN 不作为退出证明。
- 安装发布支持原请求精确恢复。保存的 Bird 默认配置使用不可变 generation，准备、准入和正常回收绑定原 Store 与 owner 证据。显式 abandon 只终结从未获准入的 preparation；已准入或提交结果不确定时保留引用。
- Codex 内部具备有界 duplex RPC、严格配置投影、会话状态及 owned persistence 检查；公共任务入口的 Codex guard 保持关闭。

## 已验证的原生范围

| Provider | 实際范围 | 尚未证明 |
|---|---|---|
| AGY 1.2.16 | 同一会话两次 Question/Answer、三段预算结算及 SQLite 历史封存；限定文件内容访问；隔离候选实现、独立审查、Host 行为验证及精确 OID 集成 | 原生编辑工具 trace、完整公共写任务与中断续写流程 |
| Claude Code 2.1.285 | 同一会话两次 Question/Answer、三段结算；限定原生 Read；隔离候选实现、独立审查、行为验证及精确 OID 集成 | 原生编辑工具 trace、所有公共写任务和故障恢复组合 |
| Codex 0.154.0 | 配置读取、零 turn 握手及内部协议/本地进程 fixture 的限定验收 | 真实 Read/业务 turn 尚未完成，公共委派不可用 |

以上结论来自各自固定源码及原生验收范围，不代表本次整合完成了所有原生场景。合成 fixture、普通 Go 测试和 parent race 不能替代 Provider 原始进程或其子进程的退出证明。

## 后续边界

原始 execution supervisor 的 B2/B3 生命周期、Host 死亡后的真实 owner 接管、完整 crash/replay 矩阵、自动重试及完整 DAG 验收、跨平台支持仍需后续迭代。嵌套 sandbox 的实际限制保留为已知缺口。

本轮复用未受变化影响的已通过证据，并执行必要的公开候选编译、定点回归和构建；没有重跑整模块 race 或全部 Store 组合。历史失败、明确 SKIP 与未验范围保留，不能据此宣称整体完成。
