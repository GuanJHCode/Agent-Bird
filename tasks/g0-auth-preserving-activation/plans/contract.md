# 已批准的接入实验与认证保护

2026-09-13 用户批准上轮建议，同时明确：不得影响本机几个 CLI 登录账号的 auth 信息。

## 允许范围

- 结果固定回传任务创建时已核验的原主 thread；显示切换不迁移任务 owner。owner 同时绑定工作区、执行/连接身份和账号/profile 上下文，不允许跨账号自动续接。
- 完成离线实现、复审和新鲜预检后，进行一次可撤销的用户级 LaunchAgent 按需注册实验，再对称卸载。用户入口仍是普通 codex，不修改 Codex 本体。
- 注册目标为当前用户 GUI domain、`~/Library/LaunchAgents/org.codex.orchestration.proxy.plist` 与默认 Codex control socket；已有文件、socket 或服务冲突时拒绝覆盖或接管。
- 主会话串行整合和实际实验准入；subagent 各用独立 worktree 实现。真实样本独立登记，失败证据不可覆盖。

## 认证硬边界

- 不执行登录、登出、账号切换、认证写入、凭据迁移或额度重置。不得改动任何现有 CLI 的 auth 文件、Keychain 登录项、账号管理器存储、认证 symlink 或 profile 路由。
- 不复制真实凭据进入测试 home，不输出或持久化 token/API key/完整 auth/config/environment。只读调查仅输出路径类别、profile 隔离信息和安全元数据；必要的私有路径清单保持临时、owner-only，并在收尾删除。
- 不能只依靠“不调用 login/logout”保证不变：原生程序可能刷新 token。实际实验前必须证明真实认证不会被读取后刷新、写回或被其他 profile 的 backend 串用；优先使用隔离的合成测试身份以及明确的文件/网络限制。
- 对已发现的认证存储执行不暴露内容的前后比对；发生变化时停止本实验并保留安全证据，绝不自动用旧备份覆盖认证信息。
- 用户级监听器不能把不同 CODEX_HOME/账号会话无条件接入同一个 backend。实验准入限定自有测试客户端，账号上下文不明的客户端拒绝处理，不能猜测凭据来源。

## 证据与完成判据

- 保留配置/Hook、原始设计与历史 case；所有新 native/backend/endpoint 只按本任务可验证身份清理。launchd 公开 socket 不能由 private backend 清理器删除。
- 离线测试先覆盖真实 socket/FD、每前端独立 backend、失败/退出清理及认证隔离，不以自报字典的绿色测试代替系统接口证据。
- 原生/注册实验使用新的完整 argv、源码/二进制摘要、资源身份和对称撤销记录。本地窗口 10 秒，若确需模型阶段仍为 Luna/medium 且 120 秒；不使用 Astra。本轮按需启动机制优先零模型验证。
- 成功需同时满足：已注册的默认入口按需激活、隔离 backend、任务 owner 不随 UI 迁移、自有退出/记录保留、认证保护比对、注册与自有资源全部撤销。任何子项未验证均如实记录；不提前宣布完整 G0 或 G1–G4 完成。
