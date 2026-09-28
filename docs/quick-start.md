# 在 Codex 中使用 Claude

先按[安装说明](installation.md)安装一次插件。已经安装当前版本的用户无需重装，只需打开新的 Codex CLI 会话。

进入目标项目后启动 Codex：

```sh
cd /path/to/your/project
codex
```

在对话里直接说：

> 用 Claude 检查这个项目的错误处理，列出有源码依据的问题。先只读分析，不改代码。

需要开发时，明确范围和验收条件：

> 用 Claude 实现输入校验，只修改 parser 模块。拒绝 bool 输入，补充测试，测试和审查通过后再集成。

Codex 会准备任务、调用 Claude、查看进度并验收结果。你不需要手填 JSON、会话 PID 或任务句柄，也不需要另开 Claude 主脑。

## 补充要求和查看结果

在原 Codex 会话继续说：

> 给刚才的任务补充要求：空字符串也要拒绝。沿原任务返工，重新测试和审查。

> 查看刚才 Claude 任务的状态和已有进度。

如果换了会话，提供原 Codex 会话 ID 或保存的任务句柄路径，让主脑先核对现场。不要把“继续”理解为自动重跑；中断后能否续接以任务实际允许的动作为准。

## 需要你参与的情况

首次使用或 Claude CLI 升级后可能需要确认版本；具体工具的正常审批仍保留。任务范围、预计耗时或验收要求有歧义时，主脑会澄清。

Claude 返回后，主脑会核验内容。若因结论无依据而拒绝，任务可能显示 `failed`，这不一定是启动或执行故障。停止原因与未完成进度用于定位问题，不能代替最终结果。

未自动触发时输入：

> 使用 codex-orchestrator 插件，让 Claude 处理这个任务：……

如果仍不可用，运行 `codex plugin list`，确认 `codex-orchestrator@codex-bird` 为 installed、enabled，并新开会话。更多情况见[安装与更新](installation.md)。

## AGY 与 Grok（源码开发进度）

新增统一 provider 入口；已发布的 `v0.1.0-preview.1` 安装包尚不包含此改动。
更新后的插件可识别：

> 用 AGY 只读检查这个项目，通过 view_file 读取源码，给出有依据的结论。

AGY 当前只开放只读分析，尚未开放开发和候选集成。Grok 已接入探测入口，但执行仍返回 `execution_profile_unsupported`，登录成功不代表可以派发。不要改用旧请求或放宽权限绕过限制。

开发者可以使用 `task probe --provider agy` 或 `task probe --provider grok` 检查本机能力。日常用户仍只需要在 Codex 中说明任务和所选 CLI。

源码中的新 Grok 1.0.34 只读适配需明确授权本任务新会话目录写入，才可使用 `--grok-session-write`。probe 会分别报告能力和该授权要求。它仍等待新的高层原生验收，已安装插件及公开安装包尚未切换；不要直接修改缓存或跳过授权。

### 本机 Grok 只读使用（2026-09-18）

在新 Codex 会话中说“用 Grok 只读分析这个文件/问题”，由主脑完成任务委派和结果核验。Grok 1.0.34 已实测 read_file、会话绑定和 collect→ACK/accept；任务需要专用会话目录写入许可及明确预算。结果 artifact 只保留最后一次工具调用之后的公开文本（最终回答）；2026-09-18 的历史运行曾保存全部公开拼接文本，该记录保留。当前不支持 Grok 修改代码或续跑，MCP 拒绝尚无原生负向验收。

## 会话启停、模型和自动隔离

新版本支持在 Codex 中直接输入：

```text
$orchestrate 开启 claude
$orchestrate 开启 grok
$orchestrate 开启 agy
$orchestrate 状态
$orchestrate 关闭 grok
$orchestrate 将 claude 的当前会话模型设为 <model-id>
$orchestrate 保存 grok 默认模型为 <model-id>
$orchestrate 将 agy 的当前会话模型设为 CLI 默认
```

开启会检查对应 CLI 和已确认的版本指纹；只启用当前会话的派发资格，有任务才启动进程。关闭只停止当前 Codex 会话委派给该 Provider 的任务，其他会话及自己打开的 CLI 不受影响；状态为 `stopping` 时尚未确认全部退出。重新开启不会重启历史任务。

模型优先级：单次指定 > 当前会话 > 保存的 Provider 默认值 > CLI 默认值。实际模型 ID 必须由对应 CLI 支持；编排器不会自动替换模型，也不修改第三方配置。默认值变更只影响未来任务。

代码修改强制使用任务独立 worktree，测试和构建在候选隔离区执行，无需每次重复说明。原仓库未提交修改必须保留并明确输入基线。Grok、AGY 仍仅支持只读任务。

该功能需要 `session_provider_lifecycle_v1` 协调器；旧协调器会明确要求升级。升级不能中断正在进行的其他委派，新版源码完成验证不等于已替换正在运行的服务。
