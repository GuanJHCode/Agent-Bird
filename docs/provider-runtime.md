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


## 整合后的新基线 generation

已有 run 的候选完成业务 accept 和预算结算后，可用 `submit --request <fresh-request.json> --source-control-file <source-control.json>` 派生一次新 run。请求带 `new_base_rework`：绑定精确 source integration event，完整映射祖先 task ID，提供 feedback/acceptance 和稳定 command ID。新请求保留原 DAG、Provider pin/profile、路径、策略与额度上限，机械映射依赖和预算组；source TargetHEAD 成为新 BaseOID。source control 仅用于认证，不写入 lineage 或请求摘要。

当前要求 producer、validate/review 已启用 AutoDirectory；所有 Directory 原值保留。支持一个最终 integration 闭包及可选有序 compose；session、fallback、indexed review、config-generation、旧 rework 和已修订预算等形状显式拒绝。Store 注入并认证新上下文，普通提交不能自行提供它。每个 source event 只有一个 successor；下一代使用刚完成的 integration event 及其 control capability。

CLI 在 Submit 前持久化并同步恢复身份，精确重试返回同 control/bootstrap/launch。目标预约从 queued 起覆盖整个新 DAG，未证明终态或 UNKNOWN 时保留；Host 在 common Git 锁内复核 target HEAD/ref/inode。旧 run 的历史和额度不重置。完整请求规则、支持边界和 macOS 受控三代 runtime race 证据见 [新基线验收](../tasks/new-base-rework/docs/final-validation.md)。该证据不代表真实模型质量或 Linux Git 链通过。


## 验证命令的有限未启动重试

对于v1 candidate的`validate`步骤，显式有限execution policy、AutoDirectory和`candidate_action.command_sha256`可启用受限未启动重试。Host将固定native executable复制到独立受保护目录，派发前预约最多32MiB及8192byte持久元数据；不接受脚本或已知shell/env分发器，也不承诺动态库和子命令完全固定。

只有原Host实际`cmd.Start`返回EAGAIN/EINTR且尚未产生Process/Handle的活typed proof，才进入`candidate_validation_retry`。原诊断/终态与工具清理经过认证，Store持久化并ACK原失败后，按有限预算/次数/退避创建fresh attempt。已启动命令仍由原Handle执行Stop/Wait/tree；冷starting、cleanup失败或只有旧physical Host未入Store的spool均保留UNKNOWN与预约。每次工具删除证明成立后释放raw预留，8192byte元数据继续计费。

公开用法与边界见[验收及使用说明](../tasks/validation-start-retry/docs/usage-and-limits.md)。Mac arm64真实受控链及profile访问拒绝已有证据；不代表已启动validate失败或implement/compose/integrate失败可自动重试，也不代表真实内核资源耗尽或Linux Git链通过。


## 有界 Git filter 首形状

Darwin 支持带固定 `git_filter` manifest 的首次 `kind=provider` Claude/AGY implementer，以及消费其已接受附件的独立 `kind=candidate` validate。producer 必须使用新 AutoDirectory、workspace-write profile、固定 ProviderLock 和显式文件路径；validator 必须同 manifest、dependency input v1、单一 source task、独立 sealed validation tool，并将 Target 留为空值。该验证结果不授予目标分支审查或整合权限。

其后可接同 manifest 的 content-only review：Kind 留空、pinned Claude/AGY Reviewer/ReadOnly、action v1/dependency v1/单一 validator source、fresh AutoDirectory、独立 filter reservation、无 ValidationTool。Host 强制原生结构化审查输出，保留原 producer Candidate 和 validator Validation/digest，将新 outer Binding 绑定 reviewer；Target（含 ReviewRevision）始终全零。详见[只读审查验收](../tasks/filter-readonly-review/docs/final-validation.md)。

已接受的 approve content review 可接独立 `kind=candidate` / `filter_target_preflight`：action v1、dependency v1、单一 review source、相同 manifest、新的 attempt/reservation/Broker，AutoDirectory=true；不设置 Provider/profile/lock/command。Target 仅允许原 Candidate.RepoRoot、同一 BaseOID 与命名 `refs/heads/...`。Host 检查实际 base B→R 往返、完整工作树/index/HEAD/ref 与原 source pins，输出新的观察 DTO，完整保留原 review 的三层绑定及零 Target；这项观察不授予目标写入或整合权限。详情见[目标预检验收](../tasks/filter-target-host/docs/final-validation.md)。

预检没有 Provider 进程；成功的 Result/Exited 不携带 Provider identity 或 exit code，Exited 仍必须有原 Host callback 计时和精确 filter cleanup。末态产物写入期间的 Stop 会抑制 Result，Stopped/Exited 引用同一保留产物。冷 journal、未知原进程或清理失败不恢复活权柄、不释放不确定的预约。

Host 仅在本次原进程 Wait/tree 已证明后 freeze/发布结果；旧 journal、续接、返工、重试和其他未列明 filtered action 均拒绝。canonical B 与 rendered R 分别保留摘要；validator 使用新的受管工作树恢复 R，并在结束和结果记录后复验来源 private ref。真实 Provider 返回 technical question 时仍会 waiting_user，但当前 filtered answer/continuation 不可用。

[验收记录](../tasks/filter-host-pipeline/docs/final-validation.md)区分受控 Store/Host 原生链、纯约束、Linux 交叉编译与尚未执行的真实模型/公开 CLI/TUI 验收。已验原生链限 Darwin arm64 上由 Claude-shaped 受控进程驱动的实际 Host：fresh producer → accepted validator → content-only readonly review → 独立 target preflight，原 review 的 Target 精确全零；AGY 在同一生产路由上仅通过准入约束测试，尚无单独原生验收。Linux 仅交叉编译，未取得 native Git/filter 资格。LFS、target-bound filtered review、compose/integrate、rework、retry/continuation 和签名不在此形状内；target preflight 只提供独立观察。当前 [F13 包](../tasks/closure-package-provenance/docs/filter-target-current-20261010.md) source `3e2f24169d79ae519eec4b72e1a3ac512221e52d` 覆盖此生产代码；最终完整 inventory oracle 只增加测试，不改变包内生产代码，Formal60 仍为 FAILED，Intel Mac/Windows native 延期。
