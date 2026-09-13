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

1. 一次原生 synthetic 验证停在 macOS dyld 启动阶段，尚未进入 Codex 主程序；见 `../docs/native-tripwire-attempt.md`。本机不重复启动，真实刷新保护仍未通过验收。
2. 前端与后台都需要保护；仅给后台设置刷新阻断不足。固定 native 没有在 AuthManager 之前返回完整 effective config 的查询入口，启动前防护与启动后复核必须分别成立。
3. 后续仍需实现可信元数据来源、真实来源环境下的专用私有端点、代理亲自观察的 owner/thread 绑定、临时入口和停止/显式续接流程。纯策略测试不等于这些接线已完成。
4. 低价 Codex adapter 的协议候选可以独立完成；认证保护接线前，通用执行入口必须在启动 native 之前拒绝该 provider。

官方当前文档用于补充接口背景；固定版本行为以本地源码核验为准：

- [App Server](https://learn.chatgpt.com/docs/app-server)：managed ChatGPT 模式由原生程序自动刷新和持久化 token。
- [Developer commands](https://learn.chatgpt.com/docs/developer-commands?surface=cli)：原生 `--remote` 支持私有 Unix socket。
