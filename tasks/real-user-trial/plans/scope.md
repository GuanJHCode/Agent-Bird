# 项目内真实试用

用户已批准补齐可亲自操作的临时试用入口。目标是用已有登录环境验证真实小任务的下发、退出停止、同任务重新打开、显式续接和结果回传；不得以合成响应代替真实试用。

## 执行边界

- 主会话编排与验收，子 Agent 在独立 worktree 实现；当前先核验接入与认证约束。
- 不登录、登出、切换账号、复制凭据、修改 auth/Keychain/profile 路由；不输出或持久化完整配置、环境和秘密。
- 不修改原有启动器或全局配置，不永久注册服务。用户最新“可以”按允许试用时使用一次性原生 `--remote` 参数处理，不视为允许修改认证或永久注册服务。
- 不能把已有 synthetic `IsolationSpec` 改成真实 HOME 后宣称安全，也不能把只读 thread 存在性当作 owner 归属证明。
- 模型验证选低价模型，不用 Astra；先完成固定输入、写入范围、保护措施及停止/回收流程，再开放真实试用。
- 退出停止自有执行，保留任务和结果；不自动 resume、不迁移 owner、不删除其他任务记录。
- 只增加新行为的必要回归和一次对应的实际链路验证，不重复旧资源长测。

## 已确认的入口事实

固定 0.154 的原生 TUI 可用 `codex --remote unix://ABS` 连接私有端点；项目配置不能选择任意端点。无参数启动只会探测自身默认 control socket。现有已打开的 embedded TUI 不能通过事后创建 listener 迁入代理。

新客户端可连接既有 daemon 并验证服务进程身份，但 `thread/read` 只能证明线程存在，不能证明该线程属于另一个 TUI 连接。因此只读 probe 不得产生可供 G1 submit 的 owner capability。

## 正在核验

1. 原生 synthetic 验证曾停在 macOS dyld 启动阶段；后续根据本机 Apple `dyld-support.sb` 增加一组启动规则后，`--version` 已正常完成，app-server 则快速退出，尚未完成刷新保护验收。只有新增证据支持的有界实验才继续，不能重复旧失败或随意放开隔离。
2. 前端与后台都需要保护；仅给后台设置刷新阻断不足。固定 native 没有在 AuthManager 之前返回完整 effective config 的查询入口，启动前防护与启动后复核必须分别成立。
3. 后续仍需实现可信元数据来源、真实来源环境下的专用私有端点、代理亲自观察的 owner/thread 绑定、临时入口和停止/显式续接流程。纯策略测试不等于这些接线已完成。
4. 低价 Codex adapter 的协议候选可以独立完成；认证保护接线前，通用执行入口必须在启动 native 之前拒绝该 provider。

## 当前最小实现决定

用户要求保护现有认证，不等于禁止原生程序正常读取认证，也不要求 Keychain 必须由内核区分读取和写入。保留正常来源环境和既有 store，不复制或切换账户。前端、后台和 supervisor 启动的 SourceHost 子树都应用刷新阻断、禁用 Agent Identity 和认证文件写保护；认证 RPC 与可改变相关配置的覆盖须在转发前被限制。

supervisor 在任何线程前从自己启动并核验的固定 backend 投影有效配置和账户类别；原始配置、账户响应和环境值不落盘或进入日志。临时任务许可绑定既有 owner attachment proof、固定 binary、run/task、prompt hash 和预算，由 SourceHost 每次启动前复核，不能由 adapter 请求自报。初始真实试用仅单条只读 README 任务、Luna/medium、owner_review，无 fallback，最大一次尝试和 120 秒活动预算。

新增实现分属独立 worktree：auth wire gate、Python supervisor/metadata/owner bridge、Go trial admission；主会话统一验收和集成。旧 synthetic IsolationSpec、既有账号和全局配置保持不变。

官方当前文档用于补充接口背景；固定版本行为以本地源码核验为准：

- [App Server](https://learn.chatgpt.com/docs/app-server)：managed ChatGPT 模式由原生程序自动刷新和持久化 token。
- [Developer commands](https://learn.chatgpt.com/docs/developer-commands?surface=cli)：原生 `--remote` 支持私有 Unix socket。
