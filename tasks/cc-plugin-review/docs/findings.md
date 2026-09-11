# 官方 Codex for Claude Code 插件实现核查

核查日期：2026-09-11。来源：[openai/codex-plugin-cc](https://github.com/openai/codex-plugin-cc)，插件版本 1.0.6，固定提交 `db52e28f4d9ded852ab3942cea316258ae4ef346`。本机 marketplace checkout 干净；`git ls-remote origin HEAD` 返回同一提交。本轮为实现研究，未更改本项目既有方案或实现代码，未运行真实模型任务。

主 Agent 负责来源核验、官方文档、隔离清理测试和结论；`cc_plugin_runtime` 只读核查运行层，`cc_plugin_host` 只读核查 Claude 命令与生命周期层。

## 结论

插件证明了一种可借鉴的组合：宿主原生 Agent/Bash 负责派发和结果返回，Node helper 管理自己的 Codex 执行运行时。它没有提供“连接用户已打开的 Codex TUI，并向其可靠注入外部工具结果”的机制。正常 Claude SessionEnd 还会终止当前 session 的任务并删除记录，不满足本项目关闭后持续运行、恢复补交的要求。

因此，本次新增的优先研究候选是 **Codex 原生子 Agent/后台工具能否作为薄转发通道**。该候选尚未在原生 Codex CLI 验证，不能把 Claude 的宿主行为视为 Codex 的能力，也不据此放行 G0。

## 真实调用链

```text
Claude 主控
  → /codex:rescue
  → Claude Agent(codex:codex-rescue)
  → 一次 Bash 调用 codex-companion.mjs task
  → 插件自管 broker（通常复用；也有 direct 回退）
  → 自己启动的 codex app-server（stdio JSONL RPC）
  → thread/start 或 thread/resume
  → turn/start
  ← item/* 与 turn/completed 等通知
  ← helper 提取结果、保存 job/thread/turn 元数据、输出 stdout
  ← Claude 原生 Agent 返回/后台完成通知
```

1. **入口是薄转发器。** `/rescue` 明确调用 Agent，子 Agent 使用一次 Bash 转发请求并返回 helper stdout。源码：[rescue.md](https://github.com/openai/codex-plugin-cc/blob/db52e28f4d9ded852ab3942cea316258ae4ef346/plugins/codex/commands/rescue.md#L7)、[codex-rescue.md](https://github.com/openai/codex-plugin-cc/blob/db52e28f4d9ded852ab3942cea316258ae4ef346/plugins/codex/agents/codex-rescue.md#L22)。
2. **插件掌控的是执行者服务。** 底层直接 `spawn("codex", ["app-server"])`，用 stdin/stdout 通信；broker 对 helper 暴露 Unix socket 或 Windows named pipe。它不寻找既有 TUI。broker 忙时可回退新的 direct app-server，不能宣称始终只有一个执行进程。源码：[spawn](https://github.com/openai/codex-plugin-cc/blob/db52e28f4d9ded852ab3942cea316258ae4ef346/plugins/codex/scripts/lib/app-server.mjs#L190)、[broker 回退](https://github.com/openai/codex-plugin-cc/blob/db52e28f4d9ded852ab3942cea316258ae4ef346/plugins/codex/scripts/lib/codex.mjs#L613)。
3. **执行者会话由返回的 thread ID 绑定。** task 创建可持久 thread；续接调用 thread/resume。默认按当前 Claude session 选择旧 task，不能误称恢复主控本身的工具调用。源码：[thread/start 与 resume](https://github.com/openai/codex-plugin-cc/blob/db52e28f4d9ded852ab3942cea316258ae4ef346/plugins/codex/scripts/lib/codex.mjs#L1095)、[session 过滤](https://github.com/openai/codex-plugin-cc/blob/db52e28f4d9ded852ab3942cea316258ae4ef346/plugins/codex/scripts/codex-companion.mjs#L294)。
4. **结果有两段传输。** Codex 到 helper 使用 RPC 通知；helper 到 Claude 使用 Bash stdout 和宿主子 Agent 完成通道。官方 Claude 文档说明后台子 Agent 的结果以 completion notification 到达后续轮次：[宿主说明](https://code.claude.com/docs/en/sub-agents#run-subagents-in-foreground-or-background)。插件没有独立的 Claude 外部唤醒接口或跨重启 ACK 投递协议。部分完成状态还可能来自 final_answer 后的 250ms 推断，不能全部当作已收原生 turn/completed：[结果捕获](https://github.com/openai/codex-plugin-cc/blob/db52e28f4d9ded852ab3942cea316258ae4ef346/plugins/codex/scripts/lib/codex.mjs#L373)。

## 两种后台执行必须区分

| 路径 | 实际控制者 | 结果方式 |
|---|---|---|
| `/codex:rescue --background` | Claude 原生后台子 Agent；旗标不传入 task | 子 Agent 等待 helper，结果走宿主原生返回 |
| `/codex:review --background` | Claude 的 `Bash(run_in_background:true)` | helper 完成，结果作为宿主后台任务输出 |
| 直接执行 helper 的 `task --background` | helper 自己启动 detached task-worker | 返回 job ID，提供 status/result；不是 `/rescue` 的标准转发方式 |
| `status <job-id> --wait` | helper 显式查询 | 每 2 秒读本地状态，默认最长 240 秒；不是通常的结果推送通道 |

源码：[review 后台语义](https://github.com/openai/codex-plugin-cc/blob/db52e28f4d9ded852ab3942cea316258ae4ef346/plugins/codex/commands/review.md#L38)、[detached worker](https://github.com/openai/codex-plugin-cc/blob/db52e28f4d9ded852ab3942cea316258ae4ef346/plugins/codex/scripts/codex-companion.mjs#L671)、[状态等待](https://github.com/openai/codex-plugin-cc/blob/db52e28f4d9ded852ab3942cea316258ae4ef346/plugins/codex/scripts/codex-companion.mjs#L318)。

## 与当前方案的对应关系

| 方面 | 插件实际行为 | 对本项目的判断 |
|---|---|---|
| 不使用 MCP 派发 | Agent/Bash + App Server | 可借鉴；OpenAI 官方文档也明确插件使用 app-server |
| 模型任务下发 | 自己创建/续接 Codex 执行者 thread | 可借鉴执行者 adapter 的控制方式；不证明能附着原生 Codex 主控 |
| 身份 | workspace、Claude session、job、Codex thread/turn 分开保存 | 可借鉴分层身份；默认选择的过滤不等于本项目完整授权/epoch 合同 |
| 正常退出 | SessionEnd shutdown broker、停止本 session jobs、删除对应记录 | 与关闭后持续执行/恢复补交要求不符；不可照搬 |
| 恢复 | 向已保存的 Codex 执行者 thread 提交新任务 | 不是恢复主控 pending tool call；SessionStart 没有补交队列 |
| Stop hook | Claude 结束一轮响应时，可选启动同步 review gate；gate 关闭只提示运行中任务 | 不构成完成事件推送器 |
| 认证 | 使用本地 Codex 和调用环境，由 Codex 自己读取认证 | 可借鉴，不要求复制凭据 |
| 权限 | 明确传 `approvalPolicy:"never"` 与 read-only/workspace-write sandbox；服务端请求不支持 | 不能直接沿用为“保持既有权限交互”；我们的原生审批要求仍须单独满足 |
| 存储与恢复 | state.json/job JSON/log；最多保留 50 个 job | 不能替代原方案的持久事务、故障对账、容量和幂等验收 |

关键来源：[SessionEnd](https://github.com/openai/codex-plugin-cc/blob/db52e28f4d9ded852ab3942cea316258ae4ef346/plugins/codex/scripts/session-lifecycle-hook.mjs#L42)、[权限参数](https://github.com/openai/codex-plugin-cc/blob/db52e28f4d9ded852ab3942cea316258ae4ef346/plugins/codex/scripts/lib/codex.mjs#L63)、[状态存储](https://github.com/openai/codex-plugin-cc/blob/db52e28f4d9ded852ab3942cea316258ae4ef346/plugins/codex/scripts/lib/state.mjs#L29)、[OpenAI 官方说明](https://learn.chatgpt.com/docs/mcp-server)。

## 建议的下一步验证路线（尚未实施）

先验证“原生 Codex 主控 → 只转发的原生子 Agent/后台工具 → 独立 Go Host → 外部 CLI → 原生任务返回”的在线通道。只用合成结果，检查主控 idle/busy、用户输入竞争、结果实际角色以及子 Agent 提问/续接；不能把当前 Codex 应用里的协作工具行为算作原生 CLI 实测。

离线部分继续由独立 Host 持久保存任务与待交事件，不采用插件 SessionEnd 的删除策略。必须另验主控退出时执行者持续、恢复同一主控身份、无额外用户输入的积压补交，以及原工具结果身份和副作用幂等。原生子 Agent 能力若不能满足这些条件，该路线仍不能替代 G0。

这条路线值得优先核查；现阶段无需把“增加启动包装器”视为唯一下一步，也不能提前认定包装器会解决所有问题。

## 验证边界

- `cc_plugin_host` 运行 `node --test tests/commands.test.mjs`：8/8，通过。它只检查命令文本契约，不证明真实 Claude 调用行为。
- 主 Agent 使用隔离的 TMPDIR、CLAUDE_PLUGIN_DATA 和 Git 配置运行 `node --test --test-name-pattern '^session end fully cleans up jobs for the ending session$' tests/runtime.test.mjs`：1/1，通过。测试使用合成 job 和真实无业务作用的 Node 子进程，验证本 session 记录删除、子进程退出及其他 session 保留；[日志](session-end-test.log)。
- 未运行真实 Codex/Claude 模型任务，未验证原生在线唤醒、跨会话恢复或本项目 G0。
