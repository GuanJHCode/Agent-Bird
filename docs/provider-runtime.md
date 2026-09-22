# Provider 与运行时

插件入口为 `scripts/invoke.sh`；生成的 portable 包会解析自身目录并安装私有运行时。所有状态操作绑定已验证的 owner（Codex 原生 thread 或显式 launcher 托管进程），handle 只保存路由元数据，不是授权来源。

## 控制接口

```text
provider enable --provider <codex|claude|grok|agy> --provider-lock <confirmed-lock.json>
provider disable --provider <codex|claude|grok|agy>
provider status
provider model --provider <codex|claude|grok|agy> --model <model-id|cli-default>
provider default-model --provider <codex|claude|grok|agy> --model <model-id|cli-default>
task probe --provider <codex|claude|grok|agy>
```

这些参数由 Skill 自动组织。Provider lock、handle、请求文件生成在 owner-only 任务目录内，不应提交 Git。第一次使用或二进制摘要变化需核对真实指纹；普通 probe 不登录、不执行模型。

## 隔离与配置

`plugins/codex-orchestrator/defaults.json` 声明隔离写策略，不能通过任务参数关闭。写 profile 缺少 `candidate_workspace` 时，公共 adapter、提交层和 Host 都会拒绝。省略 managed task 的 directory 时自动生成目录模板，再由 Host 根据 attempt/segment 创建真实独立 worktree。

Provider 启停与模型配置属于编排器自己的 SQLite 状态。缺少显式开关记录时保留兼容默认；显式关闭持久化为阻断记录。新提交、fallback 和重新排队受检查；关闭中的进程仍占槽，unknown 不等于已退出。

新的会话控制需要协调器能力 `session_provider_lifecycle_v1`，Grok profile 需要 `grok_readonly_v1`。Codex worker 的 `codex_worker_v1` 当前不再声明：固定 0.154.0 已确认存在嵌套 macOS 沙箱冲突，预检与 Host 执行入口拒绝 `codex_nested_sandbox_unsupported`。登录、确认版本或升级协调器不能修复这一执行协议问题；不得修改 guard 或切换状态目录绕过。Codex 作为主脑的控制入口不受此阻塞影响。

## 结果和恢复

继续使用原 handle 检查、收集和处理结果。ACK 只确认消息处置，accept 才是业务验收。恢复原任务需显式 owner 决策、正确 revision、剩余预算和已确认停止的旧执行树；不得将另一会话历史冒充原 Provider 会话。

详细字段见 [CLI 文档](../tools/orchestrator/README.md) 和插件 [Skill](../plugins/codex-orchestrator/skills/orchestrate/SKILL.md)。
