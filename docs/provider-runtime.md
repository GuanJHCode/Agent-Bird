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

CLI 协议兼容与二进制确认分开处理。Grok 1.0.34 和 1.0.40 的已核对构建共用既有协议，历史 lock 保持有效；认证预检沿用该次任务绑定的真实版本、路径和摘要。AGY 1.2.5/1.2.7 继续按所需参数检测能力。新版仍需通过每项安全能力检查，未知 Grok 构建继续返回 `grok_version_not_verified`。不要从 probe 成功推导出指纹、写会话或预算已获授权，也不要为兼容新版自动替换旧任务使用的 CLI。验证范围见[支持范围](support-matrix.md)。

## 隔离与配置

`plugins/codex-orchestrator/defaults.json` 声明隔离写策略，不能通过任务参数关闭。写 profile 缺少 `candidate_workspace` 时，公共 adapter、提交层和 Host 都会拒绝。省略 managed task 的 directory 时自动生成目录模板，再由 Host 根据 attempt/segment 创建真实独立 worktree。

Provider 启停与模型配置属于编排器自己的 SQLite 状态。缺少显式开关记录时保留兼容默认；显式关闭持久化为阻断记录。新提交、fallback 和重新排队受检查；关闭中的进程仍占槽，unknown 不等于已退出。

新的会话控制需要协调器能力 `session_provider_lifecycle_v1`，Grok profile 需要 `grok_readonly_v1`。固定 Codex 0.154.0 的轻量 worker 需要新的 `codex_read_edit_worker_v1`；旧 `codex_worker_v1` 不足以准入。旧协调器返回 `coordinator_upgrade_required`，已有任务继续使用原运行时，不强制重启或切换状态目录。

Claude/Grok/AGY 主脑可将隔离代码任务交给 Codex：它自主列举、读取、搜索代码，通过原生 `apply_patch` 编辑。命令、构建和测试由主脑在候选验证阶段执行；worker 不开放 shell、子进程、web、MCP 或递归委派，也不需要 Docker。源 CLI 的审批设置、固定二进制摘要、隔离与退出确认仍受校验。Codex 主脑处理 Codex 子任务时使用满足约束的原生 subagent。

一次真实验收已覆盖 Codex 0.154.0 + `gpt-5.5` 的自主读取、修改、候选冻结及主脑行为测试；CLI 默认或显式模型设置仍保留，但任意模型/版本组合未经验证。该实现已包含于本地并存运行时；默认插件与后台协调器尚未切换，未发布新安装包。

## 结果和恢复

普通委派默认在主脑当前任务内完成收尾：收到 queued/running 后继续有界
`task wait`，超时继续等待；多个句柄公平轮转，一项失败不停止跟踪其他任务。
派发回执的 `continuation` 给出相同入口下的下一步参数，并明确
`automatic_callback=false`。这些提示不自动执行命令或扩大主脑授权。

wait 返回事件时已带 collection proof。主脑读完分页与产物、核对摘要和
验收条件，在已有授权内显式 accept/reject，并单独 ACK，最后在主会话展示
结论、证据和缺口。owner acceptance 是主脑的验收职责，不默认转给用户。
失败、超时、incomplete 和需要用户决定的问题同样必须展示；不自动重试、
加预算或把只读报告当作已实施代码。

此轻量包是 collect-only，没有唤醒已结束/断开主脑回合的原生回调。仅当用户
明确要求只派发、暂停，或宿主结束回合时保留原句柄并交还；不能把保存结果、
终端通知或后台 shell 说成自动回到模型。该流程无需新增 MCP、hooks 或宿主配置。

继续使用原 handle 检查、收集和处理结果。ACK 只确认消息处置，accept 才是业务验收。恢复原任务需显式 owner 决策、正确 revision、剩余预算和已确认停止的旧执行树；不得将另一会话历史冒充原 Provider 会话。

详细字段见 [CLI 文档](../tools/orchestrator/README.md) 和插件 [Skill](../plugins/codex-orchestrator/skills/orchestrate/SKILL.md)。
