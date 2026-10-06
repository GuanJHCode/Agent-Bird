# Provider 与运行时

插件入口为 `scripts/invoke.sh`；生成的 portable 包会解析自身目录并安装私有运行时。所有状态操作绑定已验证的 owner（Codex 原生 thread 或显式 launcher 托管进程），handle 只保存路由元数据，不是授权来源。

## 控制接口

```text
provider enable --provider <claude|grok|agy> --provider-lock <confirmed-lock.json>
provider disable --provider <claude|grok|agy>
provider status
provider model --provider <claude|grok|agy> --model <model-id|cli-default>
provider default-model --provider <claude|grok|agy> --model <model-id|cli-default>
task probe --provider <claude|grok|agy>
```

这些参数由 Skill 自动组织。Provider lock、handle、请求文件生成在 owner-only 任务目录内，不应提交 Git。第一次使用或二进制摘要变化需核对真实指纹；普通 probe 不登录、不执行模型。

## 隔离与配置

`plugins/codex-orchestrator/defaults.json` 声明隔离写策略，不能通过任务参数关闭。写 profile 缺少 `candidate_workspace` 时，公共 adapter、提交层和 Host 都会拒绝。省略 managed task 的 directory 时自动生成目录模板，再由 Host 根据 attempt/segment 创建真实独立 worktree。

Provider 启停与模型配置属于编排器自己的 SQLite 状态。缺少显式开关记录时保留兼容默认；显式关闭持久化为阻断记录。新提交、fallback 和重新排队受检查；关闭中的进程仍占槽，unknown 不等于已退出。

新的会话控制需要协调器能力 `session_provider_lifecycle_v1`，Grok profile 需要 `grok_readonly_v1`。旧协调器不满足时应受控升级，不修改 guard 或切换另一状态目录绕过限制。

## 结果和恢复

继续使用原 handle 检查、收集和处理结果。ACK 只确认消息处置，accept 才是业务验收。恢复原任务需显式 owner 决策、正确 revision、剩余预算和已确认停止的旧执行树；不得将另一会话历史冒充原 Provider 会话。

typed Question/Answer 与显式中断 Resume 使用受信任的原会话和状态封存，继续同一 attempt 并累计各 segment 的预算；它们不通过旧的无绑定 session 接口放开续接。AGY/Claude 已有同会话两轮问答的限定原生证据，公开写任务、中断续写及所有故障组合仍需分别验收。

活着的 Source Host 可以在 Coordinator 重启后有界重连并重放原 spool。Host 已死亡而执行进程仍可能存活时，PID 或进程组 absence 不能补造原始 Wait 证明；未完成原 supervisor/接管能力前保留 UNKNOWN 所有权和预算预留。

保存的 Bird 默认配置使用不可变 generation；任务准备、准入、引用释放绑定原 Store 和 owner。显式 abandon 只适用于未获准入的 preparation，不能用于释放已准入或提交结果不确定的任务引用。这不修改 Provider 自身的认证和全局配置。

Codex 内部协议支持仍处于受保护接入阶段，公共 Worker 入口没有开放。真实范围和未完成项目见[支持矩阵](support-matrix.md)及[迭代说明](iteration-20261006.md)。

详细字段见 [CLI 文档](../tools/orchestrator/README.md) 和插件 [Skill](../plugins/codex-orchestrator/skills/orchestrate/SKILL.md)。

## 大候选分块审查

需要 Coordinator 能力 `indexed_candidate_review_v1`。在 `candidate_action.review_index` 显式设置 `{"version":1,"mode":"indexed_candidate_review_v1","shards":3,"review_ordinal":0}`：validate 生成一次不可变清单，三个 Grok read-only review 分别使用 ordinal 0、1、2，且都依赖同一个 validate。integrate 直接依赖全部三个 review，设置 `dependency_input_version:1`，并让 `source_task` 与 `review_ordinal` 指向同一个审查分块。必须显式规划全部分块；不会自动增加任务或扩大额度。

Host 只把每个分块指定的文本范围写入实际 prompt 文件。Store 接受结果和 Host 合并前都会验证清单、覆盖凭据、候选及配置来源；缺块、重复范围或来源变化会拒绝。合并使用已接受的清单，不重新生成清单。未声明 `review_index` 的任务继续使用原有快照限制。

容量：最多512个路径、路径总字节64KiB、base/candidate两侧逻辑文本及一次建索引的Git blob读取总量8MiB、单块32KiB、最多2048块、每页最多64条且64KiB、最多63个review。每个实际模型输入含元数据与指导文本最多384KiB；单个清单/结果产物最多1MiB。二进制、符号链接、submodule、LFS及超限输入拒绝，不截断后报告完整覆盖。

这些上限同时生效。每个 Grok 分块输入仍携带完整索引元数据；JSON 转义、任务提示词和固定指导文本也计入384KiB。符合1MiB清单上限的候选仍可能因实际输入过大而在审查前被拒绝，增加分块数不能缩小完整元数据。

使用已发布 Bird 配置版本时，Grok profile 需显式填写 `model`，或设置 `cli_model_default:true`，两者互斥。配置准备流程绑定原 generation；当前保存的默认配置格式没有 Grok 字段，省略上述两项会在创建准备记录前拒绝。AGY/Claude/Codex 的默认配置语义保持原样。

本机受控 Provider 验收覆盖真实 CLI、Store、Source Host 恢复、三分块实际文本消费和 Git 合并；这不代表真实模型的分块审查质量或其他平台原生验收。
