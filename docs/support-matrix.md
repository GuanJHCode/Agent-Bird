# 支持范围

| 功能 | 状态 |
|---|---|
| Codex owner 绑定、任务派发、collect/ACK/accept | 已实现 |
| 多任务、依赖 DAG、默认全局并发 2 | 已实现 |
| 按会话开启/关闭 Provider | 已实现；关闭等待执行树退出确认 |
| 代码修改的强制隔离与候选测试/审查/集成 | 三种 worker 共用托管流程；结构化审查仍由 Claude 执行 |
| Claude/Grok/AGY 主脑 | 显式 controller 启动入口；按托管进程归属，不冒充原生会话 ID |
| 会话模型、保存默认、单次覆盖 | 已实现；由实际 CLI 能力决定可用模型 |
| Grok | 1.0.34 只读及逐文件许可下的隔离编码/冻结/行为验收通过；需显式任务会话目录写授权 |
| AGY | 1.2.5 通过一次原生隔离编码及候选冻结；终端工具未建立原生验收 |
| 原生会话 resume | typed profile 未验证，保持拒绝 |
| 自动加预算/自动替换失败任务 | 不支持 |

Grok 只读内置工具限制为 read_file/list_dir/grep；编码增加文件编辑工具，禁用原生子任务和 web search，并传递 MCP 拒绝规则；真实 MCP 负向拒绝尚未完成原生验收。其 artifact 包含公开 assistant 文本，可能含过程说明，不能把最终回答匹配当成整个 artifact 精确匹配。

本仓库的测试使用合成 fixture 和本地受控子进程。多 Provider 的真实模型并发、任意版本/模型组合和无监督长任务不由这些测试证明。真实 Provider 测试默认 opt-in，运行会消耗对应账号额度。

候选开发拒绝 Git LFS/filter、自定义 hooks 等执行扩展。权限不足或输入不明确时明确阻塞，不自动修改用户全局 Git 或 Provider 配置。
